#!/usr/bin/env python3
"""Recognize the single open, administrator-owned release candidate for one channel."""
from __future__ import annotations

import argparse
import json

import release_pr


def active_candidate(pulls, repository, channel):
    """Return the one eligible PR; do not silently choose among competing sources."""
    candidates = []
    for pull in pulls:
        if pull.get("state") != "OPEN" or pull.get("draft") is not False:
            continue
        if (pull.get("base") or {}).get("ref") != "main":
            continue
        if (pull.get("head") or {}).get("repo", {}).get("full_name") != repository:
            continue
        if (pull.get("user") or {}).get("login") != "chyroc":
            continue
        try:
            release_pr.release_title(pull.get("title", ""), channel == "beta")
        except release_pr.ReleaseError:
            continue
        number = pull.get("number")
        if type(number) is not int or number <= 0:
            raise release_pr.ReleaseError("an open release candidate has no valid PR number")
        candidates.append(pull)
    if len(candidates) > 1:
        numbers = ", ".join("#" + str(pull["number"]) for pull in candidates)
        raise release_pr.ReleaseError(
            "multiple open " + channel + " release candidates exist (" + numbers
            + "); explicitly supersede all but one without moving their immutable sources"
        )
    return candidates[0] if candidates else None


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repository", required=True)
    parser.add_argument("--channel", choices=("release", "beta"), required=True)
    parser.add_argument("--pulls", required=True, help="JSON list from the repository's open-PR API")
    args = parser.parse_args()
    if not release_pr.REPOSITORY.fullmatch(args.repository):
        raise SystemExit("a public owner/repository is required")
    with open(args.pulls, encoding="utf-8") as stream:
        pulls = json.load(stream)
    if not isinstance(pulls, list):
        raise SystemExit("open release PR response is not a list")
    candidate = active_candidate(pulls, args.repository, args.channel)
    if candidate:
        print(candidate["number"])
        print(candidate["html_url"])


if __name__ == "__main__":
    main()
