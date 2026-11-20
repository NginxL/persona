#!/usr/bin/env python3
"""Bind the required release verification check to an exact proposed merge commit."""
from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import sys

SHA = re.compile(r"[0-9a-f]{40}\Z")
REPOSITORY = re.compile(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+\Z")
CHECK_NAME = "All available release-PR checks passed"


class CheckError(ValueError):
    """A safe-to-report release-check failure."""


def gh_api(arguments, token, payload=None, runner=subprocess.run):
    if not token:
        raise CheckError("GitHub token is required for the release verification check")
    environment = dict(os.environ)
    environment["GH_TOKEN"] = token
    environment.pop("GITHUB_TOKEN", None)
    command = ["gh", "api", *arguments]
    if payload is not None:
        command += ["--input", "-"]
    result = runner(command, input=None if payload is None else json.dumps(payload),
                    env=environment, capture_output=True, text=True, timeout=60)
    if result.returncode:
        raise CheckError("GitHub check-run API failed: " + (result.stderr or result.stdout).strip())
    return json.loads(result.stdout) if result.stdout.strip() else {}


def identity(repository, commit, pr):
    if not REPOSITORY.fullmatch(repository):
        raise CheckError("release repository must be owner/name")
    if not SHA.fullmatch(commit):
        raise CheckError("release check requires a full merge-result commit")
    if type(pr) is not int or pr < 1:
        raise CheckError("release PR number must be positive")


def external_id(pr, commit):
    return f"release-pr/{pr}/{commit}"


def pending(repository, commit, pr, token, runner=subprocess.run):
    identity(repository, commit, pr)
    external = external_id(pr, commit)
    endpoint = f"repos/{repository}/commits/{commit}/check-runs?check_name={CHECK_NAME.replace(' ', '%20')}&per_page=100"
    checks = gh_api([endpoint], token, runner=runner).get("check_runs", [])
    existing = [check for check in checks if check.get("external_id") == external and check.get("head_sha") == commit]
    if len(existing) > 1:
        raise CheckError("multiple release verification checks own this exact merge result")
    payload = {
        "name": CHECK_NAME,
        "head_sha": commit,
        "external_id": external,
        "status": "in_progress",
        "output": {"title": "Release verification is running",
                   "summary": "Every required platform must pass at this exact proposed merge result."},
    }
    if existing:
        check_id = existing[0]["id"]
        gh_api(["-X", "PATCH", f"repos/{repository}/check-runs/{check_id}"], token, payload, runner)
        return check_id
    created = gh_api(["-X", "POST", f"repos/{repository}/check-runs"], token, payload, runner)
    check_id = created.get("id")
    if type(check_id) is not int or check_id < 1:
        raise CheckError("GitHub did not return a release check-run ID")
    return check_id


def conclude(repository, check_run_id, conclusion, token, runner=subprocess.run):
    if not REPOSITORY.fullmatch(repository):
        raise CheckError("release repository must be owner/name")
    if type(check_run_id) is not int or check_run_id < 1:
        raise CheckError("release check-run ID must be positive")
    if conclusion not in ("success", "failure"):
        raise CheckError("release check conclusion must be success or failure")
    title = "All required platforms passed" if conclusion == "success" else "Release verification failed"
    gh_api(["-X", "PATCH", f"repos/{repository}/check-runs/{check_run_id}"], token, {
        "status": "completed", "conclusion": conclusion,
        "output": {"title": title, "summary": "This result belongs to the exact proposed merge result."},
    }, runner)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repository", required=True)
    sub = parser.add_subparsers(dest="command", required=True)
    start = sub.add_parser("pending")
    start.add_argument("--commit", required=True)
    start.add_argument("--pr", type=int, required=True)
    finish = sub.add_parser("conclude")
    finish.add_argument("--check-run-id", type=int, required=True)
    finish.add_argument("--conclusion", choices=("success", "failure"), required=True)
    args = parser.parse_args(argv)
    token = os.environ.get("GH_TOKEN", "")
    if args.command == "pending":
        print(pending(args.repository, args.commit, args.pr, token))
    else:
        conclude(args.repository, args.check_run_id, args.conclusion, token)


if __name__ == "__main__":
    try:
        main()
    except (CheckError, OSError, ValueError, subprocess.SubprocessError) as error:
        print("Release check failed: " + str(error), file=sys.stderr)
        sys.exit(1)
