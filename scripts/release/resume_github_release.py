#!/usr/bin/env python3
"""Reuse a GitHub Release's existing build identity and verified bytes during a rerun."""
import argparse
import hashlib
import json
from pathlib import Path
import re
import subprocess
import tempfile


class ResumeError(ValueError):
    pass


def release_assets(repository, tag, gh):
    data = json.loads(subprocess.check_output(
        [gh, "release", "view", tag, "--repo", repository, "--json", "assets"], text=True, timeout=60))
    assets = data.get("assets") if isinstance(data, dict) else None
    if not isinstance(assets, list):
        raise ResumeError("GitHub returned no Release asset list")
    for item in assets:
        if not isinstance(item, dict) or not isinstance(item.get("name"), str) \
                or not isinstance(item.get("digest"), str) or not re.fullmatch(r"sha256:[0-9a-f]{64}", item["digest"]):
            raise ResumeError("GitHub returned an asset without a SHA-256 digest")
    return assets


def download(repository, tag, item, gh):
    name = item["name"]
    with tempfile.TemporaryDirectory(prefix="release-resume-download-") as directory:
        subprocess.run([gh, "release", "download", tag, "--repo", repository,
                        "--pattern", name, "--dir", directory], check=True, timeout=300)
        data = (Path(directory) / name).read_bytes()
    if hashlib.sha256(data).hexdigest() != item["digest"].removeprefix("sha256:"):
        raise ResumeError("downloaded Release asset digest differs: " + name)
    return data


def prepare(repository, tag, commit, resume, gh):
    assets = release_assets(repository, tag, gh)
    if any(item["name"] in {"latest.json", "SHA256SUMS"} for item in assets):
        resume.parent.mkdir(parents=True, exist_ok=True)
        resume.write_text(json.dumps({"build": 0, "has_manifest": True, "coordinated": True}) + "\n")
        resume.chmod(0o600)
        return 0, True, True
    descriptor = next((item for item in assets if item["name"] == "ci-latest.json"), None)
    if descriptor is not None:
        manifest = json.loads(download(repository, tag, descriptor, gh))
        if manifest.get("commit") != commit:
            raise ResumeError("existing Release manifest is for another source commit")
        build = manifest.get("build")
        if type(build) is not int or not re.fullmatch(r"[0-9]{12}", str(build)):
            raise ResumeError("existing Release manifest has no valid build number")
        has_manifest = True
    else:
        data = json.loads(subprocess.check_output(
            [gh, "release", "view", tag, "--repo", repository, "--json", "createdAt"], text=True, timeout=60))
        build = release_build_timestamp(data.get("createdAt"))
        has_manifest = False
    resume.parent.mkdir(parents=True, exist_ok=True)
    resume.write_text(json.dumps({"build": build, "has_manifest": has_manifest, "coordinated": False}) + "\n")
    resume.chmod(0o600)
    return build, has_manifest, False


def release_build_timestamp(value):
    if not isinstance(value, str):
        raise ResumeError("GitHub Release has no creation timestamp")
    match = re.fullmatch(r"([0-9]{4})-([0-9]{2})-([0-9]{2})T([0-9]{2}):([0-9]{2}):[0-9]{2}Z", value)
    if match is None:
        raise ResumeError("GitHub Release creation timestamp is not UTC")
    return int("".join(match.groups()))


def restore(repository, tag, directory, gh, resume=None):
    if resume is not None and json.loads(resume.read_text()).get("coordinated") is True:
        return
    directory = Path(directory)
    for item in release_assets(repository, tag, gh):
        name = item["name"]
        if name in {"latest.json", "SHA256SUMS"}:
            continue
        if name.endswith(".exe.gz"):
            continue  # Native Windows verification stays with the hub publisher's exact verified bytes.
        if not (name.endswith(".gz") or name in {"install.sh", "SHA256SUMS", "VERSION", "latest.json", "ci-latest.json", "ci-SHA256SUMS"}):
            continue
        data = download(repository, tag, item, gh)
        target = directory / name if name == "install.sh" else directory / "bin" / name
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(data)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("prepare", "restore"))
    parser.add_argument("--repository", required=True)
    parser.add_argument("--tag", required=True)
    parser.add_argument("--commit", required=True)
    parser.add_argument("--directory", type=Path)
    parser.add_argument("--resume", type=Path, required=True)
    parser.add_argument("--output", type=Path)
    parser.add_argument("--gh", default="gh")
    args = parser.parse_args()
    if args.action == "prepare":
        build, has_manifest, coordinated = prepare(args.repository, args.tag, args.commit, args.resume, args.gh)
        if args.output:
            args.output.write_text("number=%d\nmanifest=%s\ncoordinated=%s\n" %
                                   (build, "true" if has_manifest else "false", "true" if coordinated else "false"))
    else:
        if args.directory is None:
            parser.error("restore requires --directory")
        restore(args.repository, args.tag, args.directory, args.gh, args.resume)


if __name__ == "__main__":
    main()
