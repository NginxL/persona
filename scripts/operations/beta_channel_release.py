#!/usr/bin/env python3
"""Publish only the pointer for verified immutable beta artifacts.

The beta-channel prerelease contains latest.json and optionally android.json,
not replacement binaries. Those index files belong to the immutable beta tag
whose bytes have already been uploaded and verified. The pointer never becomes
GitHub's stable releases/latest.
"""
import argparse
import hashlib
import json
from pathlib import Path
import re
import subprocess
import sys
import tempfile

from tag_release import ConfigError, is_beta_tag, read_receipt, resolve_tag, verify_remote_assets

POINTER = "beta-channel"
BETA = re.compile(r"0|[1-9][0-9]*")


def get_json(gh, path):
    return json.loads(subprocess.check_output([gh, "api", path], text=True, timeout=60))


def previous_source(repo, repository, pointer, gh):
    """Authenticate the pointer's current index before following its immutable source to a newer beta."""
    if pointer.get("draft") or not pointer.get("prerelease") or pointer.get("tag_name") != POINTER:
        raise ConfigError("beta-channel must be a prerelease, never a stable Release")
    assets = pointer.get("assets", [])
    if not assets and pointer.get("target_commitish") in ("", None):
        return None, None  # the pointer Release has just been created; its first index establishes the source
    latest = next((item for item in assets if item.get("name") == "latest.json"), None)
    if not isinstance(latest, dict):
        raise ConfigError("the existing beta pointer has no latest.json to authenticate")
    with tempfile.TemporaryDirectory(prefix="persona-beta-previous-") as directory:
        path = Path(directory) / "latest.json"
        subprocess.run([gh, "release", "download", POINTER, "--repo", repository,
                        "--pattern", "latest.json", "--output", str(path)], check=True, timeout=120)
        if latest.get("digest") and latest["digest"] != "sha256:" + hashlib.sha256(path.read_bytes()).hexdigest():
            raise ConfigError("the existing beta pointer index differs from its listed digest")
        manifest = json.loads(path.read_text())
    tag = "v" + str(manifest.get("version", ""))
    if not is_beta_tag(tag) or manifest.get("channel") != "beta" or not re.fullmatch(r"[0-9a-f]{40}", str(manifest.get("commit", ""))):
        raise ConfigError("the existing beta pointer does not identify an immutable beta")
    previous = resolve_tag(repo, repository, tag)
    if previous != manifest["commit"]:
        raise ConfigError("the existing beta pointer source does not match its immutable tag")
    if subprocess.run(["git", "-C", str(repo), "merge-base", "--is-ancestor", previous, "origin/main"]).returncode != 0:
        raise ConfigError("the existing beta pointer source is not on origin/main")
    return previous, tag


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", required=True, type=Path)
    parser.add_argument("--repository", required=True)
    parser.add_argument("--tag", required=True)
    parser.add_argument("--receipt", required=True, type=Path)
    parser.add_argument("--gh", default="gh")
    args = parser.parse_args()
    if not is_beta_tag(args.tag) or not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", args.repository):
        raise ConfigError("a versioned vX.Y.Z-beta.N tag and owner/repository are required")
    commit = resolve_tag(args.repo, args.repository, args.tag)
    assets = read_receipt(args.receipt, commit)
    index = {item["name"]: Path(item["path"]) for item in assets}
    if "latest.json" not in index:
        raise ConfigError("beta pointer needs the verified tag's latest.json")
    manifest = json.loads(index["latest.json"].read_text())
    if manifest.get("channel") != "beta" or manifest.get("version") != args.tag[1:] or manifest.get("commit") != commit:
        raise ConfigError("latest.json is not this beta version's beta-channel manifest and immutable commit")
    # The pointer may copy only the indexes, never binaries from another tag.
    for name in ("latest.json", "android.json"):
        if name not in index:
            continue
        remote = json.loads(subprocess.check_output(
            [args.gh, "api", f"repos/{args.repository}/releases/tags/{args.tag}"], text=True, timeout=60))
        if remote.get("tag_name") != args.tag or remote.get("draft") is not False or remote.get("prerelease") is not True:
            raise ConfigError("immutable beta tag must be a published prerelease")
        verify_remote_assets(args.repository, args.tag, [item for item in assets if item["name"] == name],
                             remote.get("assets", []), args.gh, missing=True)
    pointer = None
    try:
        pointer = get_json(args.gh, f"repos/{args.repository}/releases/tags/{POINTER}")
        if pointer.get("draft") or not pointer.get("prerelease") or pointer.get("tag_name") != POINTER:
            raise ConfigError("beta-channel must be a prerelease, never a stable Release")
    except subprocess.CalledProcessError:
        subprocess.run([args.gh, "release", "create", POINTER, "--repo", args.repository,
                        "--prerelease", "--title", "Persona beta updates",
                        "--notes", "The beta-channel pointer only. Binaries remain on immutable versioned prereleases."], check=True)
    if pointer is not None:
        previous, tag = previous_source(args.repo, args.repository, pointer, args.gh)
        if previous is not None and previous != commit:
            # An ordinary new beta has a new source. Authenticate the old pointer first, then permit only forward
            # ancestry; a different branch, an older release or a substituted old index must never move it.
            if subprocess.run(["git", "-C", str(args.repo), "merge-base", "--is-ancestor", previous, commit]).returncode != 0:
                raise ConfigError(f"beta pointer cannot move from {tag} to an unrelated or older immutable source")
    remote = get_json(args.gh, f"repos/{args.repository}/releases/tags/{POINTER}")
    existing = {asset["name"]: asset for asset in remote.get("assets", [])}
    pointers = []
    same = True
    for name in ("latest.json", "android.json"):
        if name not in index:
            continue
        item = next(item for item in assets if item["name"] == name)
        if name in existing:
            try:
                verify_remote_assets(args.repository, POINTER, [item], remote.get("assets", []), args.gh)
                print(f"Beta pointer {name} already has the verified digest; keeping it")
            except ConfigError as error:
                # A normal advance intentionally replaces the previous beta's index with this tag's verified bytes.
                # The old pointer was authenticated above; this expected digest change is not artifact substitution.
                if not str(error).startswith("another artifact already owns Release asset name:"):
                    raise
                same = False
                pointers.append(item)
        else:
            same = False
            pointers.append(item)
    if same:
        print(f"Beta pointer {POINTER} already describes {args.tag} ({commit})")
        return
    subprocess.run([args.gh, "release", "edit", POINTER, "--repo", args.repository,
                    "--target", commit, "--prerelease"], check=True, timeout=60)
    # One small request, then independent remote SHA-256 verification.
    subprocess.run([args.gh, "release", "upload", POINTER, "--repo", args.repository, "--clobber",
                    *(item["path"] for item in pointers)], check=True, timeout=120)
    remote = get_json(args.gh, f"repos/{args.repository}/releases/tags/{POINTER}")
    verify_remote_assets(args.repository, POINTER, pointers, remote.get("assets", []), args.gh, missing=True)
    print(f"Verified beta pointer {POINTER} -> {args.tag} ({commit})")


if __name__ == "__main__":
    try:
        main()
    except (ConfigError, OSError, ValueError, subprocess.CalledProcessError) as error:
        print("Beta pointer publication failed: " + str(error), file=sys.stderr)
        raise SystemExit(1)
