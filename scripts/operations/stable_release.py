#!/usr/bin/env python3
"""Advance a stable GitHub Release only after its immutable manifest and newest-stable status verify."""
import argparse
import json
from pathlib import Path
import subprocess

from tag_release import check_tag_feed, stable_version


class StableReleaseError(ValueError):
    pass


def promote_stable(repository, tag, manifest_path, gh="gh"):
    manifest = json.loads(manifest_path.read_text())
    version, build = manifest.get("version"), manifest.get("build")
    candidate = stable_version(tag)
    if candidate is None or version != tag[1:] or type(build) is not int:
        raise StableReleaseError("stable Release requires a vX.Y.Z tag and a numeric build")
    releases = json.loads(subprocess.check_output(
        [gh, "release", "list", "--repo", repository, "--exclude-drafts", "--exclude-pre-releases",
         "--limit", "1000", "--json", "tagName"], text=True, timeout=60))
    versions = [stable_version(item.get("tagName", "")) for item in releases if isinstance(item, dict)] \
        if isinstance(releases, list) else []
    versions = [item for item in versions if item is not None]
    if candidate not in versions:
        raise StableReleaseError("GitHub's stable Release list does not contain the exact tag")
    if max(versions) > candidate:
        print("Not moving Latest back to older stable Release " + tag, flush=True)
        return False
    check_tag_feed("https://github.com/%s/releases/download/%s/latest.json" % (repository, tag), version, build)
    subprocess.run([gh, "release", "edit", tag, "--repo", repository, "--latest"], check=True, timeout=60)
    details = json.loads(subprocess.check_output(
        [gh, "release", "view", tag, "--repo", repository, "--json", "isLatest,isPrerelease"], text=True, timeout=60))
    if details.get("isPrerelease") is not False or details.get("isLatest") is not True:
        raise StableReleaseError("stable Release did not become GitHub Latest")
    return True


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repository", required=True)
    parser.add_argument("--tag", required=True)
    parser.add_argument("--manifest", required=True)
    parser.add_argument("--gh", default="gh")
    args = parser.parse_args()
    promote_stable(args.repository, args.tag, Path(args.manifest), args.gh)


if __name__ == "__main__":
    main()
