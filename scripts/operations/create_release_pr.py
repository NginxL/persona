#!/usr/bin/env python3
"""Create a release PR once, with bounded backoff for GitHub's transient content-creation limits."""
from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import time
from pathlib import Path


class ReleasePRError(ValueError):
    pass


def gh(arguments, token, repository):
    environment = dict(os.environ)
    environment["GH_TOKEN"] = token
    environment.pop("GITHUB_TOKEN", None)
    return subprocess.run(["gh", *arguments, "--repo", repository], env=environment,
                          capture_output=True, text=True)


def create(repository, title, branch, body, token, attempts=6, wait=30, sleeper=None):
    if not token:
        raise ReleasePRError("persona-release is missing RELEASE_PR_TOKEN; no release PR or branch was created")
    if sleeper is None:
        sleeper = time.sleep
    if attempts < 1:
        raise ReleasePRError("release PR creation needs at least one attempt")
    existing = gh(["pr", "list", "--state", "open", "--head", branch, "--json", "number,title,url"], token, repository)
    if existing.returncode:
        raise ReleasePRError("could not check the existing release PR: " + existing.stderr.strip())
    pulls = json.loads(existing.stdout)
    if len(pulls) > 1:
        raise ReleasePRError("more than one open release PR owns " + branch)
    if pulls:
        if pulls[0]["title"] != title:
            raise ReleasePRError("the existing release PR has a different version: " + pulls[0]["title"])
        print(pulls[0]["url"])
        return
    for attempt in range(attempts):
        created = gh(["pr", "create", "--base", "main", "--head", branch, "--title", title, "--body", body], token, repository)
        if created.returncode == 0:
            print(created.stdout.strip())
            return
        transient = re.search(r"was submitted too quickly|secondary rate limit|temporarily blocked|HTTP 50[234]", created.stderr, re.I)
        if not transient or attempt + 1 == attempts:
            raise ReleasePRError("release PR creation failed after the bounded retry budget: " + created.stderr.strip())
        delay = wait * (2 ** attempt)
        print(f"release PR creation was throttled; retrying in {delay} seconds", flush=True)
        sleeper(delay)
    raise ReleasePRError("release PR creation exhausted its bounded retry budget")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repository", required=True)
    parser.add_argument("--title", required=True)
    parser.add_argument("--branch", required=True)
    parser.add_argument("--body-file", type=Path, required=True)
    parser.add_argument("--attempts", type=int, default=int(os.environ.get("RELEASE_PR_CREATE_ATTEMPTS", "6")))
    parser.add_argument("--wait", type=int, default=int(os.environ.get("RELEASE_PR_CREATE_WAIT", "30")))
    args = parser.parse_args()
    if args.wait < 0:
        parser.error("retry wait cannot be negative")
    body = args.body_file.read_text()
    create(args.repository, args.title, args.branch, body, os.environ.get("RELEASE_PR_TOKEN", ""), args.attempts, args.wait)


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError) as error:
        raise SystemExit(str(error))
