#!/usr/bin/env python3
"""Build, sign and check one version tag's Android APK for the GitHub Release.

scripts/operations/tag_release.py runs this as the android platform's command, in its clean worktree detached at the
tag's commit (docs/operations/release.md "Private version-tag consumers"). It reads the main-branch worker's private
configuration (android_release_worker.py): the same application id, signing certificate, version-code base, build
environment and signer, so an APK from a Release and one a hub got from the main branch update each other. The
receipt it writes, only after aapt2 and apksigner confirm package, version and certificate, lists
Persona-<version>-android.apk and android.json, the index hubs read beside latest.json.
"""
import argparse
import fcntl
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import time

import android_release_worker as worker
import android_signing_worker
import publish_android_apk

INDEX = "android.json"
VERSION = re.compile(r"[0-9A-Za-z][0-9A-Za-z._-]*")
HEX_SHA256 = re.compile(r"[0-9a-f]{64}")


def git(worktree, *args):
    return subprocess.run(["git", "-C", str(worktree), *args], check=True, capture_output=True, text=True).stdout.strip()


def check_source(worktree, commit):
    """The coordinator's worktree, at the tag's commit, clean, with the whole history the version code counts."""
    if git(worktree, "rev-parse", "HEAD") != commit:
        raise ValueError("worktree is not at the tag's commit")
    if git(worktree, "status", "--porcelain=v1", "--untracked-files=all"):
        raise ValueError("worktree is not clean")
    if git(worktree, "rev-parse", "--is-shallow-repository") != "false":
        raise ValueError("a shallow clone would count another version code")


def check_config(config):
    for key in ("repository", "application_id", "state_directory"):
        if not isinstance(config.get(key), str) or not config[key]:
            raise ValueError("missing configuration field: " + key)
    if not HEX_SHA256.fullmatch(config.get("certificate_sha256", "")):
        raise ValueError("certificate_sha256 must be the existing signing certificate digest")
    if "signing_queue" not in config and not isinstance(config.get("local_signing"), dict):
        raise ValueError("no signer: configure signing_queue (the signing host) or local_signing (a keystore here)")


def wait_for_lock(path, timeout):
    """The main-branch worker's lock: the signing queue holds one request at a time. A worker cycle builds, signs,
    publishes and verifies, so this waits for it, up to timeout seconds."""
    stream = path.open("a")
    deadline = time.monotonic() + timeout
    while True:
        try:
            fcntl.flock(stream.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
            return stream
        except BlockingIOError:
            if time.monotonic() >= deadline:
                stream.close()
                raise TimeoutError("the Android release worker kept its lock for %d s" % timeout)
            print("Waiting for the Android release worker", flush=True)
            time.sleep(10)


def sign_here(config, request, directory):
    """Sign with a keystore on this machine, through the signing host's own steps (zipalign, apksigner, checks)."""
    signer = dict(config["local_signing"], expected_cert_sha256=config["certificate_sha256"],
                  expected_package=config["application_id"])
    for key in ("keystore", "alias"):
        if not isinstance(signer.get(key), str) or not signer[key]:
            raise ValueError("missing local_signing field: " + key)
    command = signer.get("password_command")
    if not isinstance(command, list) or not command or any(not isinstance(arg, str) for arg in command):
        raise ValueError("local_signing.password_command must be a nonempty argv array")
    signature = android_signing_worker.sign(directory / "unsigned.apk", directory / "signed.apk", signer, request)
    worker.atomic_json(directory / "signature.json", signature)


def inspect(config, apk, request, version):
    """aapt2 and apksigner read the signed APK itself: whatever signed it, nothing goes up that is not this tag's."""
    args = argparse.Namespace(version_code=request["version_code"], version_name=version, min_sdk=None, notes="",
                              expected_package=config["application_id"],
                              expected_cert_sha256=config["certificate_sha256"], source_sha=request["source_sha"])
    return publish_android_apk.inspect_apk(apk, args)


def release(config, worktree, commit, version, receipt, release_only=False):
    check_source(worktree, commit)
    receipt.unlink(missing_ok=True)
    state = Path(config["state_directory"]).expanduser().resolve()
    state.mkdir(parents=True, exist_ok=True, mode=0o700)
    # Apart from the main branch's runs/<commit>: the same commit there has another version name.
    directory = state / "tags" / version
    directory.mkdir(parents=True, exist_ok=True)
    with wait_for_lock(state / "worker.lock", config.get("lock_timeout_seconds", 7200)):
        request = worker.build(config, worktree, commit, directory, version, release_only=release_only)
        (directory / "signed.apk").unlink(missing_ok=True)
        if "signing_queue" in config:
            worker.signing(config, request, directory)
        else:
            sign_here(config, request, directory)
    signed = directory / "signed.apk"
    index = inspect(config, signed, request, version)
    signature = json.loads((directory / "signature.json").read_text())
    if signature.get("signed_sha256") != index["sha256"]:
        raise ValueError("signed APK differs from its signing receipt")
    out = directory / "release"
    shutil.rmtree(out, ignore_errors=True)
    out.mkdir()
    name = f"Persona-{version}-android.apk"
    shutil.copyfile(signed, out / name)
    index = dict(version=version, name=name, **index)
    worker.atomic_json(out / INDEX, index)
    if worker.digest(out / name) != index["sha256"]:
        raise ValueError("copied APK differs from the checked one")
    assets = [dict(path=str(out / item), name=item, sha256=worker.digest(out / item)) for item in (name, INDEX)]
    receipt.parent.mkdir(parents=True, exist_ok=True)
    worker.atomic_json(receipt, dict(commit=commit, assets=assets))
    return index


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", required=True, type=Path, help="the Android release worker's private configuration")
    parser.add_argument("--worktree", required=True, type=Path, help="{worktree}: the tag's clean, detached checkout")
    parser.add_argument("--commit", required=True, help="{commit}: the tag's full commit")
    parser.add_argument("--version", required=True, help="{version}: the tag without its v")
    parser.add_argument("--receipt", required=True, type=Path, help="the receipt path of the tag-release configuration")
    parser.add_argument("--release-only", action="store_true",
                        help="after the standard sharded CI passed this commit, build only the release APK and signing evidence")
    args = parser.parse_args()
    os.umask(0o077)
    try:
        if not re.fullmatch(r"[0-9a-f]{40}|[0-9a-f]{64}", args.commit):
            raise ValueError("commit must be a full lowercase SHA")
        if not VERSION.fullmatch(args.version):
            raise ValueError("invalid version")
        if not args.receipt.is_absolute():
            raise ValueError("receipt must be an absolute path")
        config = json.loads(args.config.expanduser().read_text())
        check_config(config)
        # aapt2 and apksigner come from the build environment's SDK, as the build's tools do.
        os.environ.update(config.get("build_environment", {}))
        index = release(config, args.worktree.resolve(), args.commit, args.version, args.receipt, args.release_only)
    except (OSError, ValueError, KeyError, TimeoutError, subprocess.CalledProcessError) as error:
        print(f"ANDROID_TAG_RELEASE_FAILED {args.version}: {error}", file=sys.stderr, flush=True)
        return 1
    print(f"ANDROID_TAG_RELEASE_SUCCESS {args.version} version_code {index['version_code']} sha256 {index['sha256']}",
          flush=True)
    return 0


if __name__ == "__main__":
    sys.exit(main())
