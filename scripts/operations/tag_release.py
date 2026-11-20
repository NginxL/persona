#!/usr/bin/env python3
"""Consume version tags on a private release machine, without copying its credentials.

A private JSON config selects one platform per process. The command is an argv
array, never a shell string, and receives {repo}, {worktree}, {state_dir},
{tag}, {commit}, and {version}. The command uses the machine's existing signing
configuration and returns only after its platform-specific publication checks.
Its private artifact receipt lists files to attach to the GitHub Release.

A separate Claude loop may invoke this script for every new tag. This script
owns source/tag identity, exclusive execution, the exact worktree, and uploads;
it never creates or moves a public tag and never builds without an explicit
publication command. Use --once for one scheduler tick; --watch polls forever.
"""
import argparse
from contextlib import contextmanager
from datetime import datetime, timezone
import fcntl
import gzip
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import string
import subprocess
import sys
import tempfile
import time
import urllib.request

from windows_release import check_windows_artifacts

TAG = re.compile(r"v[0-9A-Za-z][0-9A-Za-z._-]*\Z")
COMMIT = re.compile(r"[0-9a-f]{40}|[0-9a-f]{64}")
ALLOWED_KEYS = {"repo", "worktree", "state_dir", "repository", "platform", "command", "receipt", "agent"}
FIELDS = {"repo", "worktree", "state_dir", "tag", "commit", "version"}
REPOSITORY = re.compile(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+")


class ConfigError(ValueError):
    """Configuration or tag identity is invalid; no publication was started."""


def argv(value, fields=FIELDS):
    if not isinstance(value, list) or not value or any(not isinstance(item, str) or not item or "\0" in item for item in value):
        raise ConfigError("command must be a nonempty argv array")
    for item in value:
        try:
            parsed = list(string.Formatter().parse(item))
        except ValueError as error:
            raise ConfigError("invalid command placeholder") from error
        for _, field, spec, conversion in parsed:
            if field is not None and (field not in fields or spec or conversion):
                raise ConfigError("unknown command placeholder: " + str(field))
    return value


def private_file(path):
    if not path.is_file() or path.is_symlink():
        raise ConfigError("private config must be a regular file")
    if path.stat().st_mode & 0o077:
        raise ConfigError("private config permissions must be 0600")
    return path


def load_config(path):
    path = private_file(path.expanduser().resolve())
    config = json.loads(path.read_text())
    if not isinstance(config, dict) or set(config) - ALLOWED_KEYS:
        raise ConfigError("unsupported private release configuration")
    for key in ("repo", "worktree", "state_dir", "repository", "platform"):
        if key not in config:
            raise ConfigError("missing private release configuration: " + key)
    for key in ("repo", "worktree", "state_dir"):
        if not isinstance(config[key], str) or not Path(config[key]).is_absolute():
            raise ConfigError(key + " must be an absolute path")
        config[key] = Path(config[key]).resolve()
    if config["repo"] == config["worktree"]:
        raise ConfigError("publication worktree must not be the shared checkout")
    if config["state_dir"] == config["worktree"] or config["worktree"] in config["state_dir"].parents:
        raise ConfigError("state directory must not be inside the publication worktree")
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", config["repository"]):
        raise ConfigError("repository must be owner/name")
    if not re.fullmatch(r"[a-z][a-z0-9-]*", config["platform"]):
        raise ConfigError("platform must be a lowercase identifier")
    argv(config.get("command"))
    if not config.get("receipt"):
        raise ConfigError("receipt is required; use an empty asset list for store-only publication")
    if not isinstance(config["receipt"], str) or not Path(config["receipt"]).is_absolute():
        raise ConfigError("receipt must be an absolute path")
    if "agent" in config:
        argv(config["agent"], FIELDS | {"prompt_file", "log_file"})
    return config


def git(repo, *args):
    result = subprocess.run(["git", "-C", str(repo), *args], check=True, capture_output=True, text=True, timeout=120)
    return result.stdout.strip()


def resolve_tag(repo, repository, tag):
    if not TAG.fullmatch(tag) or ".." in tag or tag.endswith("."):
        raise ConfigError("invalid version tag")
    remote = git(repo, "ls-remote", "--tags", "https://github.com/" + repository + ".git",
                 "refs/tags/" + tag, "refs/tags/" + tag + "^{}")
    rows = [line.split("\t", 1) for line in remote.splitlines() if "\t" in line]
    if any(len(row) != 2 or not COMMIT.fullmatch(row[0]) for row in rows):
        raise ConfigError("remote tag identity is not a Git object")
    peeled = [row for row in rows if row[1] == "refs/tags/" + tag + "^{}"]
    direct = [row for row in rows if row[1] == "refs/tags/" + tag]
    if len(rows) > 2 or len(peeled) > 1 or len(direct) != 1:
        raise ConfigError("remote tag identity is ambiguous")
    # A lightweight tag names its commit directly; an annotated tag is peeled
    # to its commit by the second ls-remote row.
    commit = peeled[0][0] if peeled else direct[0][0]
    if not COMMIT.fullmatch(commit):
        raise ConfigError("remote tag does not resolve to a commit")
    git(repo, "fetch", "--no-tags", "https://github.com/" + repository + ".git", commit)
    if git(repo, "rev-parse", commit + "^{commit}") != commit:
        raise ConfigError("fetched tag object is not the requested commit")
    # Fail if the tag moved while its commit was being fetched.
    again = git(repo, "ls-remote", "--tags", "https://github.com/" + repository + ".git",
                "refs/tags/" + tag, "refs/tags/" + tag + "^{}")
    if again != remote:
        raise ConfigError("tag moved during release discovery; retry")
    return commit


def prepare_worktree(config, commit):
    repo, worktree = config["repo"], config["worktree"]
    if not (repo / ".git").exists():
        raise ConfigError("configured repo is not a Git repository")
    if not worktree.exists():
        git(repo, "worktree", "add", "--detach", str(worktree), commit)
    else:
        if git(worktree, "rev-parse", "--show-toplevel") != str(worktree):
            raise ConfigError("publication worktree is not its root")
        branch = subprocess.run(["git", "-C", str(worktree), "symbolic-ref", "-q", "HEAD"],
                                capture_output=True, text=True, timeout=30)
        if branch.returncode == 0:
            raise ConfigError("publication worktree must be detached")
        if branch.returncode != 1:
            raise ConfigError("cannot verify publication worktree HEAD")
        if (worktree / git(worktree, "rev-parse", "--git-common-dir")).resolve() != (repo / git(repo, "rev-parse", "--git-common-dir")).resolve():
            raise ConfigError("publication worktree belongs to another repository")
        if git(worktree, "status", "--porcelain=v1", "--untracked-files=all"):
            raise ConfigError("publication worktree is not clean; refusing to discard changes")
        git(worktree, "checkout", "--detach", commit)
    if git(worktree, "rev-parse", "HEAD") != commit:
        raise ConfigError("publication worktree source differs from the tag")
    return worktree


@contextmanager
def exclusive(path):
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    with path.open("a") as stream:
        os.chmod(path, 0o600)
        fcntl.flock(stream.fileno(), fcntl.LOCK_EX)
        yield


def read_receipt(path, commit):
    data = json.loads(path.read_text())
    if not isinstance(data, dict) or data.get("commit") != commit:
        raise ConfigError("artifact receipt must identify the exact tag commit")
    assets = data.get("assets")
    if not isinstance(assets, list):
        raise ConfigError("artifact receipt must contain an asset list")
    if not assets and data.get("store_verified") is not True:
        raise ConfigError("an empty asset list requires store_verified: true")
    result = []
    for item in assets:
        if not isinstance(item, dict) or not isinstance(item.get("path"), str):
            raise ConfigError("invalid artifact receipt asset")
        path = Path(item["path"])
        name = item.get("name", path.name)
        if (not path.is_absolute() or not path.is_file() or path.is_symlink()
                or not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]*", name)):
            raise ConfigError("artifact must be a regular absolute file with a safe name")
        if "sha256" in item:
            digest = hashlib.sha256(path.read_bytes()).hexdigest()
            if item["sha256"] != digest:
                raise ConfigError("artifact digest differs from its receipt")
        result.append({"path": str(path), "name": name})
    return result


# The release index files hubs and install.sh read: uploaded after the downloads they list, latest.json the very last,
# so a hub never reads a manifest whose files are not there yet (GitHub serves an asset as soon as it is uploaded).
# android.json is the Android platform's: the signed APK hubs offer their phones (android_tag_release.py).
INDEX_FILES = ("VERSION", "SHA256SUMS", "android.json", "latest.json")


def host_platform():
    """This machine as a release names its platforms: linux-amd64, darwin-arm64…"""
    machine = platform.machine().lower()
    return platform.system().lower() + "-" + {"x86_64": "amd64", "aarch64": "arm64"}.get(machine, machine)


def scan_gzip(path, token, out=None):
    """The sha256 of what the gzip at path unpacks to, and whether it holds token with no digit right before or
    after it (the build number -X main.build leaves in every binary, whatever its platform). out gets the bytes."""
    pattern = re.compile(rb"(?<![0-9])" + re.escape(token) + rb"(?![0-9])")
    digest, carry, found, first = hashlib.sha256(), b"", False, True
    with gzip.open(path, "rb") as stream:
        while chunk := stream.read(1 << 20):
            digest.update(chunk)
            if out is not None:
                out.write(chunk)
            if not found:
                window = carry + chunk
                # A match at the window's end waits for the byte after it; one at its start was decided before.
                found = any(m.end() < len(window) and (m.start() > 0 or first) for m in pattern.finditer(window))
                carry, first = window[-(len(token) + 1):], first and len(window) <= len(token) + 1
    found = found or any(m.end() == len(carry) and (m.start() > 0 or first) for m in pattern.finditer(carry))
    return digest.hexdigest(), found


def carries_update_url(data, update_url):
    """The exact URL, not a prefix of a longer feed path, query or fragment."""
    continuation = rb"[A-Za-z0-9._~:/?#@!$&()*+,;=%-]"
    return re.search(rb"(?<!" + continuation + rb")" + re.escape(update_url.encode()) +
                     rb"(?!" + continuation + rb")", data) is not None


def drop_missing_helpers(assets, state_dir):
    """Helpers (persona-iphone-bridge, docs/extensions/iphone-bridge.md "Releases") are optional: a receipt whose
    latest.json lists one it does not carry (a private upload command that names its files and predates them) goes up
    with a copy of latest.json that leaves those out, with a warning, rather than failing the release. Hubs reading
    this release then simply find no build of that helper. Returns the assets to upload."""
    by_name = {item["name"]: item for item in assets}
    if "latest.json" not in by_name:
        return assets
    try:
        manifest = json.loads(Path(by_name["latest.json"]["path"]).read_text())
    except (OSError, ValueError):
        return assets  # check_release_manifest says why
    helpers = manifest.get("helpers")
    if not isinstance(helpers, dict):
        return assets
    if helpers:
        kept, dropped = {}, []
        for program, platforms in helpers.items():
            for plat, item in (platforms or {}).items():
                name = item.get("name") if isinstance(item, dict) else None
                if name in by_name:
                    kept.setdefault(program, {})[plat] = item
                else:
                    dropped.append(str(name))
        if not dropped:
            return assets
    print("warning: latest.json lists helper builds this receipt does not carry; uploading it without them: "
          + ", ".join(sorted(dropped)), file=sys.stderr, flush=True)
    if kept:
        manifest["helpers"] = kept
    else:
        manifest.pop("helpers", None)
    path = Path(state_dir) / "latest.json"
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    path.write_text(json.dumps(manifest, indent=2) + "\n")
    return [dict(item, path=str(path)) if item["name"] == "latest.json" else item for item in assets]


def check_release_manifest(assets, repository=None, expected_version=None):
    """A receipt with latest.json (the hub's) is what every release hub installs from once it is uploaded: latest.json
    must describe the downloads uploaded with it, each must be its build, and the personad among them that runs here
    must say its version and build. For a GitHub tag publication, that binary must also carry the Release installer's
    update URL. v0.2.1's latest.json said another build than its binaries, written by a later run than theirs (#4522)."""
    by_name = {item["name"]: Path(item["path"]) for item in assets}
    if "latest.json" not in by_name:
        return
    update_url = ("https://github.com/%s/releases/latest/download" % repository) if repository else None
    if update_url and "install.sh" in by_name:
        installer = by_name["install.sh"].read_text()
        match = re.search(r'^DL="\$\{PERSONA_DL:-([^"\r\n]+)\}"$', installer, re.M)
        if match is None or match.group(1).rstrip("/") != update_url:
            raise ConfigError("install.sh uses another update URL than the Release's GitHub download base")
    try:
        manifest = json.loads(by_name["latest.json"].read_text())
    except (OSError, ValueError) as error:
        raise ConfigError("latest.json does not read: " + str(error)) from error
    version, build = manifest.get("version"), manifest.get("build")
    if not isinstance(version, str) or not version or not isinstance(build, int) or not re.fullmatch(r"[0-9]{12}", str(build)):
        raise ConfigError("latest.json has no version or build number")
    if expected_version is not None and version != expected_version:
        raise ConfigError("latest.json reports version %s, not the Release tag's %s" % (version, expected_version))
    listed = [("personad", plat, item) for plat, item in (manifest.get("files") or {}).items()]
    for group in ("programs", "nodes"):
        for program, platforms in (manifest.get(group) or {}).items():
            listed += [(program, plat, item) for plat, item in (platforms or {}).items()]
    listed += [("persona-link", plat, item) for plat, item in (manifest.get("links") or {}).items()]
    # Helpers (persona-iphone-bridge) carry their own version, not the build number: only their bytes are checked.
    helpers = set()
    for program, platforms in (manifest.get("helpers") or {}).items():
        for plat, item in (platforms or {}).items():
            listed.append((program, plat, item))
            helpers.add((program, plat))
    here, ran = host_platform(), False
    if not any(program == "personad" for program, _, _ in listed):
        raise ConfigError("latest.json lists no personad")
    with tempfile.TemporaryDirectory(prefix="persona-release-check-") as directory:
        for program, plat, item in listed:
            name = item.get("name") if isinstance(item, dict) else None
            path = by_name.get(name)
            if path is None:
                raise ConfigError("latest.json lists %s, which is not in the receipt" % name)
            if path.stat().st_size != item.get("size") or hashlib.sha256(path.read_bytes()).hexdigest() != item.get("sha256"):
                raise ConfigError("%s is not the file latest.json lists (size or sha256)" % name)
            run = Path(directory) / name.removesuffix(".gz") if program == "personad" and plat == here else None
            if run is None:
                inner, stamped = scan_gzip(path, str(build).encode())
            else:
                with run.open("wb") as out:
                    inner, stamped = scan_gzip(path, str(build).encode(), out)
            if inner != item.get("binary_sha256"):
                raise ConfigError("%s does not unpack to the binary latest.json lists" % name)
            if run is None:
                if not stamped and (program, plat) not in helpers:
                    raise ConfigError("%s is not build %d: latest.json was written for another build than it" % (name, build))
                if program == "personad" and update_url:
                    with gzip.open(path, "rb") as stream:
                        data = stream.read()
                    if not carries_update_url(data, update_url):
                        raise ConfigError("%s carries another compiled update URL; the Release installer uses %s" % (name, update_url))
                continue
            run.chmod(0o755)
            said = subprocess.run([str(run), "version", "-v"], capture_output=True, text=True, timeout=60).stdout.strip()
            want = "%s\nchannel release, build %d" % (version, build)
            if said != want:
                raise ConfigError("%s says %r, not %r: latest.json was written for another build than it" % (name, said, want))
            if update_url and not carries_update_url(run.read_bytes(), update_url):
                raise ConfigError("%s carries another compiled update URL; the Release installer uses %s" % (name, update_url))
            ran = True
    if not ran:
        raise ConfigError("latest.json has no personad that runs on this machine (%s) to check it with" % here)


def check_android_index(assets, version):
    """A receipt with android.json (the Android platform's) must ship the APK it names, at its size and sha256, for
    this version: hubs download that APK and offer it to every phone (docs/operations/release.md "Android on version tags")."""
    by_name = {item["name"]: Path(item["path"]) for item in assets}
    if "android.json" not in by_name:
        return
    try:
        index = json.loads(by_name["android.json"].read_text())
    except (OSError, ValueError) as error:
        raise ConfigError("android.json does not read: " + str(error)) from error
    if not isinstance(index, dict) or index.get("version") != version:
        raise ConfigError("android.json is not version " + version)
    code = index.get("version_code")
    if type(code) is not int or not 0 < code <= 2100000000 or not isinstance(index.get("package_name"), str) \
            or not index["package_name"] or not re.fullmatch(r"[0-9a-f]{64}", str(index.get("certificate_sha256"))):
        raise ConfigError("android.json has no valid package, version code or certificate")
    name = index.get("name")
    path = by_name.get(name) if isinstance(name, str) and name.endswith(".apk") else None
    if path is None:
        raise ConfigError("android.json names %s, which is not an APK in the receipt" % name)
    if path.stat().st_size != index.get("size") or hashlib.sha256(path.read_bytes()).hexdigest() != index.get("sha256"):
        raise ConfigError("%s is not the APK android.json describes (size or sha256)" % name)


def verify_remote_assets(repository, tag, assets, listed, gh="gh", missing=False):
    """Verify exact remote bytes, also on gh versions whose release view has no digest field.

    GitHub CLI 2.23.0 omits digest from --json assets. Its REST response has the
    digest; if an older GitHub API omits that too, download the asset instead of
    accepting its name or size as proof.
    """
    actual = {}
    for remote in listed:
        if not isinstance(remote, dict) or not isinstance(remote.get("name"), str):
            raise ConfigError("GitHub returned an invalid Release asset")
        actual[remote["name"]] = remote
    if missing and not actual:
        raise ConfigError("uploaded asset digest verification failed: " + assets[0]["name"])
    fallback = None
    for item in assets:
        path = Path(item["path"])
        digest = "sha256:" + hashlib.sha256(path.read_bytes()).hexdigest()
        remote = actual.get(item["name"])
        if remote is not None and remote.get("digest") is not None:
            if remote["digest"] != digest:
                raise ConfigError("another artifact already owns Release asset name: " + item["name"])
            continue
        if remote is None and not missing:
            # Before uploading, an absent asset is what pending is for. The
            # post-upload view must contain it, through this REST fallback.
            continue
        if fallback is None:
            fallback = {}
            data = json.loads(subprocess.check_output(
                [gh, "api", "repos/" + repository + "/releases/tags/" + tag], text=True, timeout=60))
            if not isinstance(data, dict) or not isinstance(data.get("assets"), list):
                raise ConfigError("GitHub REST returned no Release asset list")
            fallback = {remote.get("name"): remote for remote in data["assets"] if isinstance(remote, dict)}
        remote = fallback.get(item["name"])
        if not isinstance(remote, dict) or remote.get("size") != path.stat().st_size:
            raise ConfigError("uploaded asset digest verification failed: " + item["name"])
        if remote.get("digest") is not None:
            if remote["digest"] != digest:
                raise ConfigError("uploaded asset digest verification failed: " + item["name"])
            continue
        url = remote.get("url")
        prefix = "https://api.github.com/repos/" + repository + "/releases/assets/"
        if not isinstance(url, str) or not url.startswith(prefix) or not url[len(prefix):].isdigit():
            raise ConfigError("GitHub returned an invalid asset download URL: " + item["name"])
        body = subprocess.check_output([gh, "api", "-H", "Accept: application/octet-stream", url], timeout=1800)
        if hashlib.sha256(body).hexdigest() != digest.removeprefix("sha256:"):
            raise ConfigError("uploaded asset digest verification failed: " + item["name"])


def release_build(repository, tag, gh="gh"):
    """One UTC-minute build identity shared by every publisher of this exact Release."""
    data = json.loads(subprocess.check_output(
        [gh, "release", "view", tag, "--repo", repository, "--json", "createdAt"], text=True, timeout=60))
    value = data.get("createdAt") if isinstance(data, dict) else None
    try:
        created = datetime.fromisoformat(value.replace("Z", "+00:00"))
    except (AttributeError, ValueError) as error:
        raise ConfigError("GitHub Release has no valid UTC creation timestamp") from error
    return int(created.astimezone(timezone.utc).strftime("%Y%m%d%H%M"))


def is_beta_tag(tag):
    return bool(re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+-beta\.[1-9][0-9]*", tag))


def upload(repository, tag, assets, gh="gh"):
    # The workflow owns Release creation; never create a tag from this machine.
    release = json.loads(subprocess.check_output(
        [gh, "release", "view", tag, "--repo", repository, "--json", "tagName,isDraft,assets"],
        text=True, timeout=60))
    if release.get("tagName") != tag or release.get("isDraft") is not False:
        raise ConfigError("published GitHub Release for the exact tag is required")
    if is_beta_tag(tag):
        details = json.loads(subprocess.check_output(
            [gh, "release", "view", tag, "--repo", repository, "--json", "isPrerelease"], text=True, timeout=60))
        if details.get("isPrerelease") is not True:
            raise ConfigError("beta tags require a prerelease, never the stable Release")
    if not assets:
        print("Store publication verified; this platform has no Release assets", flush=True)
        return
    if len({item["name"] for item in assets}) != len(assets):
        raise ConfigError("duplicate Release asset name in receipt")
    verify_remote_assets(repository, tag, assets, release["assets"], gh)
    pending = [item for item in assets if item["name"] not in {remote["name"] for remote in release["assets"]}]
    if not pending:
        print("Release assets already have the verified digests", flush=True)
        return
    with tempfile.TemporaryDirectory(prefix="persona-release-assets-") as directory:
        staging = Path(directory)
        for item in pending:
            destination = staging / item["name"]
            # The # suffix in gh is a display label, not an uploaded filename.
            # A hard link preserves exact bytes while supplying the actual name.
            try:
                os.link(item["path"], destination)
            except OSError:
                import shutil
                shutil.copyfile(item["path"], destination)
        # The downloads in one upload (gh sends them side by side), then each index file alone, in INDEX_FILES order.
        names = [item["name"] for item in pending]
        batches = [[n for n in names if n not in INDEX_FILES]] + [[n] for n in INDEX_FILES if n in names]
        for batch in (b for b in batches if b):
            subprocess.run([gh, "release", "upload", tag, "--repo", repository,
                            *(str(staging / name) for name in batch)], check=True, timeout=1800)
    remote = json.loads(subprocess.check_output(
        [gh, "release", "view", tag, "--repo", repository, "--json", "assets"], text=True, timeout=60))
    # gh returns {"assets": [...]}; older callers may return the bare list.
    listed = remote if isinstance(remote, list) else remote.get("assets") if isinstance(remote, dict) else None
    if not isinstance(listed, list) or not all(isinstance(item, dict) and isinstance(item.get("name"), str) for item in listed):
        raise ConfigError("gh release view returned no asset list")
    verify_remote_assets(repository, tag, assets, listed, gh, missing=True)


def check_tag_feed(url, version, build):
    """The immutable tag's served manifest must name this release before its stable Latest pointer can move."""
    try:
        with urllib.request.urlopen(url, timeout=60) as response:
            body = response.read((4 << 20) + 1)
        if len(body) > 4 << 20:
            raise ConfigError("latest.json is too large")
        manifest = json.loads(body)
    except (OSError, ValueError) as error:
        raise ConfigError("Release latest.json does not read: " + str(error)) from error
    if not isinstance(manifest, dict) or manifest.get("version") != version or manifest.get("build") != build:
        raise ConfigError("Release latest.json reports another version or build")


def stable_version(tag):
    match = re.fullmatch(r"v([0-9]+)\.([0-9]+)\.([0-9]+)", tag)
    return tuple(int(part) for part in match.groups()) if match else None


def promote_latest(repository, tag, assets, platform, gh="gh"):
    """Only the complete, verified stable hub receipt owns the Latest pointer."""
    if platform != "hub" or not any(item["name"] == "latest.json" for item in assets):
        return
    details = json.loads(subprocess.check_output(
        [gh, "release", "view", tag, "--repo", repository, "--json", "isPrerelease"], text=True, timeout=60))
    if details.get("isPrerelease") is True:
        return
    if details.get("isPrerelease") is not False:
        raise ConfigError("GitHub did not say whether this Release is a prerelease")
    candidate = stable_version(tag)
    if candidate is None:
        raise ConfigError("stable Latest promotion requires a vX.Y.Z tag")
    releases = json.loads(subprocess.check_output(
        [gh, "release", "list", "--repo", repository, "--exclude-drafts", "--exclude-pre-releases",
         "--limit", "1000", "--json", "tagName"], text=True, timeout=60))
    versions = [stable_version(item.get("tagName", "")) for item in releases if isinstance(item, dict)] \
        if isinstance(releases, list) else []
    versions = [version for version in versions if version is not None]
    if candidate not in versions:
        raise ConfigError("GitHub's stable Release list does not contain the exact tag")
    if max(versions) > candidate:
        print("Not moving Latest back to older stable Release " + tag, flush=True)
        return
    latest = next(Path(item["path"]) for item in assets if item["name"] == "latest.json")
    manifest = json.loads(latest.read_text())
    version, build = manifest.get("version"), manifest.get("build")
    if version != tag[1:]:
        raise ConfigError("latest.json reports another version than the Release tag")
    # releases/latest/download still names the previous release here. Check the immutable tag asset whose bytes were
    # just verified, then make that same tag Latest as the final publication action.
    url = "https://github.com/%s/releases/download/%s/latest.json" % (repository, tag)
    check_tag_feed(url, version, build)
    subprocess.run([gh, "release", "edit", tag, "--repo", repository, "--latest"], check=True, timeout=60)


def write_state(path, state):
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    temporary = path.with_suffix(".tmp")
    temporary.write_text(json.dumps(state, indent=2, sort_keys=True) + "\n")
    temporary.chmod(0o600)
    temporary.replace(path)


def check_beta_manifest(assets, version):
    """Independent beta platforms need no hub manifest; a supplied one must be this exact beta."""
    manifests = [Path(item["path"]) for item in assets if item["name"] == "latest.json"]
    if len(manifests) > 1:
        raise ConfigError("beta publication requires at most one latest.json")
    if not manifests:
        return
    manifest = json.loads(manifests[0].read_text())
    if manifest.get("channel") != "beta" or manifest.get("version") != version:
        raise ConfigError("beta latest.json must carry this beta version/channel")


def ci_signing_conclusion(repository, tag, gh="gh", commit=None):
    """The signing job's conclusion, or None when this tag's workflow has no such job."""
    runs = json.loads(subprocess.check_output(
        [gh, "run", "list", "--workflow", "release.yml", "--branch", tag, "--json", "databaseId,status,headSha", "--limit", "1"],
        text=True, timeout=60))
    if not runs:
        return None
    run = runs[0]
    if commit is not None and run.get("headSha") != commit:
        raise ConfigError("Darwin signing workflow is for another source commit")
    run_id = run["databaseId"]
    for _ in range(360):
        data = json.loads(subprocess.check_output(
            [gh, "api", "repos/%s/actions/runs/%s/jobs" % (repository, run_id), "--paginate"], text=True, timeout=60))
        job = next((item for item in data.get("jobs", []) if item.get("name") == "Sign Darwin command-line programs"), None)
        if job is None:
            status = json.loads(subprocess.check_output(
                [gh, "run", "view", str(run_id), "--repo", repository, "--json", "status,headSha"], text=True, timeout=60))
            if commit is not None and status.get("headSha") != commit:
                raise ConfigError("Darwin signing workflow is for another source commit")
            if status.get("status") != "completed":
                time.sleep(10)
                continue
            return None
        conclusion = job.get("conclusion")
        if conclusion in ("success", "skipped"):
            return conclusion
        if conclusion is not None:
            raise ConfigError("Darwin signing job failed: " + str(conclusion))
        time.sleep(10)
    raise ConfigError("Darwin signing job did not complete")


def manifest_descriptors(node):
    if isinstance(node, dict):
        if isinstance(node.get("name"), str) and isinstance(node.get("sha256"), str):
            yield node
        else:
            for value in node.values():
                yield from manifest_descriptors(value)
    elif isinstance(node, list):
        for value in node:
            yield from manifest_descriptors(value)


def merge_ci_windows(manifest, assets, directory):
    """Keep the locally verified Windows bytes/reports beside CI's other files, with one coherent manifest."""
    local_path = next((Path(item["path"]) for item in assets if item["name"] == "latest.json"), None)
    if local_path is None:
        raise ConfigError("the local hub receipt has no latest.json for Windows verification")
    local_manifest = json.loads(local_path.read_text())
    if any(item["name"].endswith(".exe.gz") for item in manifest_descriptors(manifest)) \
            and local_manifest.get("build") != manifest.get("build"):
        raise ConfigError("Windows verification requires the same CI and local build number")
    local_windows = {item["name"]: item for item in manifest_descriptors(local_manifest)
                     if item["name"].endswith(".exe.gz")}

    def replace(node):
        if isinstance(node, dict):
            name = node.get("name")
            if isinstance(name, str) and name.endswith(".exe.gz"):
                if name not in local_windows:
                    raise ConfigError("local receipt has no verified Windows artifact for " + name)
                return local_windows[name]
            return {key: replace(value) for key, value in node.items()}
        if isinstance(node, list):
            return [replace(value) for value in node]
        return node

    merged = replace(manifest)
    (directory / "latest.json").write_text(json.dumps(merged, indent=2) + "\n")
    lines = ["%s  %s\n" % (item["sha256"], item["name"]) for item in manifest_descriptors(merged)]
    (directory / "SHA256SUMS").write_text("".join(sorted(lines, key=lambda line: line.split(" ", 1)[1])))


def final_hub_manifest_exists(repository, tag, gh):
    data = json.loads(subprocess.check_output(
        [gh, "release", "view", tag, "--repo", repository, "--json", "assets"], text=True, timeout=60))
    assets = data.get("assets") if isinstance(data, dict) else None
    if not isinstance(assets, list):
        raise ConfigError("GitHub returned no Release asset list")
    return any(isinstance(item, dict) and item.get("name") in {"latest.json", "SHA256SUMS"} for item in assets)


def use_ci_hub_artifacts(config, tag, commit, assets, gh="gh"):
    """When signing is enabled, use its complete CI artifact set instead of an independently built private set."""
    if config["platform"] != "hub":
        return assets
    conclusion = ci_signing_conclusion(config["repository"], tag, gh, commit=commit)
    if conclusion != "success":
        return assets
    if final_hub_manifest_exists(config["repository"], tag, gh):
        return assets
    directory = Path(tempfile.mkdtemp(prefix="ci-artifacts-", dir=config["state_dir"]))
    remote = json.loads(subprocess.check_output(
        [gh, "release", "view", tag, "--repo", config["repository"], "--json", "assets"], text=True, timeout=60))
    digests = {item.get("name"): item.get("digest") for item in remote.get("assets", []) if isinstance(item, dict)}
    if "ci-SHA256SUMS" not in digests:
        raise ConfigError("CI SHA256SUMS is missing")

    def download(name):
        path = directory / name
        subprocess.run([gh, "release", "download", tag, "--repo", config["repository"],
                        "--pattern", name, "--dir", str(directory)], check=True, timeout=300)
        expected = digests.get(name)
        if not isinstance(expected, str) or hashlib.sha256(path.read_bytes()).hexdigest() != expected.removeprefix("sha256:"):
            raise ConfigError("downloaded CI asset digest differs: " + name)
        return path

    manifest_path = download("ci-latest.json")
    manifest = json.loads(manifest_path.read_text())
    if manifest.get("commit") != commit:
        raise ConfigError("CI latest.json is for another source commit")
    names = {"VERSION", "install.sh", "ci-SHA256SUMS"}
    for item in manifest_descriptors(manifest):
        if not item["name"].endswith(".exe.gz"):
            names.add(item["name"])
    downloaded = {"ci-latest.json": manifest_path}
    downloaded.update({name: download(name) for name in sorted(names)})
    merge_ci_windows(manifest, assets, directory)
    downloaded["latest.json"] = directory / "latest.json"
    downloaded["SHA256SUMS"] = directory / "SHA256SUMS"
    result = [dict(item, path=str(downloaded[item["name"]])) if item["name"] in downloaded else item for item in assets]
    present = {item["name"] for item in result}
    result += [{"path": str(path), "name": name} for name, path in downloaded.items()
               if name not in present and name != "ci-latest.json"]
    return result


def validate_assets(config, tag, assets):
    """Repeat the deterministic receipt checks on initial publication and on every uploaded-state resume."""
    if config["platform"] == "hub" and any(item["name"] == "latest.json" for item in assets) \
            and "-" not in tag and stable_version(tag) is None:
        raise ConfigError("stable Latest promotion requires a vX.Y.Z tag")
    try:
        check_windows_artifacts(assets, tag[1:])
    except ValueError as error:
        raise ConfigError(str(error)) from error
    if is_beta_tag(tag):
        # A platform receipt is independent: Android has its signed APK and index, iOS only its store receipt.
        # Only the hub worker has latest.json; if present it can never point beta at a stable or another version.
        check_beta_manifest(assets, tag[1:])
    check_release_manifest(assets, config["repository"], tag[1:])
    check_android_index(assets, tag[1:])


def finish_publication(config, tag, commit, platform, assets):
    validate_assets(config, tag, assets)
    upload(config["repository"], tag, assets)
    # Once remote digests verify, retain the exact receipt. A transient feed/promotion failure must not rebuild new
    # binaries under the same Release asset names on the next tick.
    checkpoint = [dict(item, sha256=hashlib.sha256(Path(item["path"]).read_bytes()).hexdigest()) for item in assets]
    state = config["state_dir"] / "state.json"
    write_state(state, {"tag": tag, "commit": commit, "platform": platform, "phase": "uploaded",
                        "store_verified": True, "assets": checkpoint})
    promote_latest(config["repository"], tag, assets, platform)
    write_state(state, {"tag": tag, "commit": commit, "platform": platform, "phase": "complete", "assets": assets})
    print("Published " + platform + " " + tag, flush=True)
    return 0


def publish_once(config, tag, commit):
    platform = config["platform"]
    state = config["state_dir"] / "state.json"
    if state.exists():
        previous = json.loads(state.read_text())
        if previous.get("tag") == tag and previous.get("commit") == commit and previous.get("phase") == "complete":
            print("Already published " + platform + " " + tag, flush=True)
            return 0
        if previous.get("tag") == tag and previous.get("commit") == commit and previous.get("phase") == "uploaded":
            assets = read_receipt(state, commit)
            return finish_publication(config, tag, commit, platform, assets)
    prepare_worktree(config, commit)
    fields = {key: str(config[key]) for key in ("repo", "worktree", "state_dir")}
    fields.update(tag=tag, commit=commit, version=tag[1:])
    write_state(state, {"tag": tag, "commit": commit, "platform": platform, "phase": "publishing"})
    command = [item.format(**fields) for item in config["command"]]
    env = dict(os.environ, PERSONA_RELEASE_TAG=tag, PERSONA_RELEASE_COMMIT=commit,
               PERSONA_RELEASE_VERSION=tag[1:], PERSONA_RELEASE_BUILD=str(release_build(config["repository"], tag)),
               PERSONA_RELEASE_REPOSITORY=config["repository"],
               PERSONA_RELEASE_PLATFORM=platform, PERSONA_RELEASE_WORKTREE=str(config["worktree"]))
    subprocess.run(command, cwd=config["worktree"], env=env, check=True)
    assets = read_receipt(Path(config["receipt"]).expanduser(), commit)
    assets = drop_missing_helpers(assets, config["state_dir"])
    assets = use_ci_hub_artifacts(config, tag, commit, assets)
    return finish_publication(config, tag, commit, platform, assets)


def invoke_agent(config, tag, commit):
    if "agent" not in config:
        return publish_once(config, tag, commit)
    state = config["state_dir"] / "state.json"
    if state.exists():
        previous = json.loads(state.read_text())
        if previous.get("tag") == tag and previous.get("commit") == commit and previous.get("phase") == "uploaded":
            assets = read_receipt(state, commit)
            return finish_publication(config, tag, commit, config["platform"], assets)
    # Prepare the source before the Agent starts, and retain this process's lock
    # across both its decision and the deterministic publication command.
    prepare_worktree(config, commit)
    fields = {key: str(config[key]) for key in ("repo", "worktree", "state_dir")}
    log = config["state_dir"] / "agent.log"
    log.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    prompt = Path(__file__).with_name("tag_release_prompt.md")
    fields.update(tag=tag, commit=commit, version=tag[1:], prompt_file=str(prompt), log_file=str(log))
    command = [item.format(**fields) for item in config["agent"]]
    environment = dict(os.environ, PERSONA_RELEASE_TAG=tag, PERSONA_RELEASE_COMMIT=commit,
                       PERSONA_RELEASE_VERSION=tag[1:], PERSONA_RELEASE_REPOSITORY=config["repository"],
                       PERSONA_RELEASE_PLATFORM=config["platform"], PERSONA_RELEASE_WORKTREE=str(config["worktree"]),
                       PERSONA_RELEASE_COMMAND=json.dumps(config["command"]),
                       PERSONA_RELEASE_RECEIPT=config.get("receipt", ""))
    with log.open("a") as output:
        subprocess.run(command, cwd=config["repo"], env=environment, stdin=subprocess.DEVNULL,
                       stdout=output, stderr=subprocess.STDOUT, check=True)
    # The command runs under the same lock and source identity even when an Agent
    # chooses the concrete platform commands. It never receives credentials here.
    return publish_once(config, tag, commit)


def tick(config):
    for tag in latest_tags(config["repo"], config["repository"]):
        commit = resolve_tag(config["repo"], config["repository"], tag)
        with exclusive(config["state_dir"] / "release.lock"):
            state = config["state_dir"] / "state.json"
            if state.exists():
                previous = json.loads(state.read_text())
                if previous.get("tag") == tag and previous.get("commit") == commit and previous.get("phase") == "complete":
                    continue
            invoke_agent(config, tag, commit)


def latest_tags(repo, repository):
    output = git(repo, "ls-remote", "--tags", "--refs", "https://github.com/" + repository + ".git", "refs/tags/v*")
    result = set()
    for row in output.splitlines():
        object_id, ref = row.split("\t", 1)
        if COMMIT.fullmatch(object_id) and TAG.fullmatch(ref.removeprefix("refs/tags/")):
            result.add(ref.removeprefix("refs/tags/"))
    return sorted(result)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", required=True, type=Path)
    parser.add_argument("--tag", help="publish exactly this existing remote version tag")
    parser.add_argument("--repository", help="print the Claude-loop prompt for this owner/name repository")
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--once", action="store_true", help="process all uncompleted remote version tags once")
    mode.add_argument("--watch", action="store_true", help="poll for version tags")
    parser.add_argument("--interval", type=int, default=60)
    args = parser.parse_args()
    if args.repository is not None:
        if not REPOSITORY.fullmatch(args.repository) or args.tag:
            parser.error("--repository requires the default --once or --watch mode")
        prompt_path = Path(__file__).with_name("tag_release_prompt.md")
        prompt = prompt_path.read_text().replace("<owner>/<name>", args.repository)
        print(prompt, end="")
        return 0
    config = load_config(args.config)
    if args.interval < 1:
        parser.error("interval must be positive")
    if args.tag:
        with exclusive(config["state_dir"] / "release.lock"):
            invoke_agent(config, args.tag, resolve_tag(config["repo"], config["repository"], args.tag))
        return 0
    if not args.once and not args.watch:
        parser.error("choose --tag, --once, or --watch")
    while True:
        tick(config)
        if not args.watch:
            return 0
        time.sleep(args.interval)


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (ConfigError, OSError, ValueError, subprocess.CalledProcessError, subprocess.TimeoutExpired) as error:
        print("Tag publication failed: " + str(error), file=sys.stderr, flush=True)
        sys.exit(1)
