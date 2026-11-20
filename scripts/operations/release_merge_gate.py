#!/usr/bin/env python3
"""Require the release PR's exact verified merge result before a manual merge."""
from __future__ import annotations

import json
import os
import re
import subprocess
import sys

SHA = re.compile(r"[0-9a-f]{40}\Z")


class ReleaseMergeError(ValueError):
    pass


def require_verified(repository, number, head, title, runner=None):
    runner = runner or subprocess.check_output
    if not re.fullmatch(r"[0-9a-f]{40}", head or ""):
        raise ReleaseMergeError("release PR head is not a full commit")
    if not title.startswith("release:"):
        return
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repository):
        raise ReleaseMergeError("release repository must be owner/name")
    try:
        data = json.loads(runner(["gh", "api", f"repos/{repository}/pulls/{number}"], text=True))
        if data.get("state") != "open" or data.get("draft") is not False:
            raise ReleaseMergeError("release PR is not open and non-draft")
        if (data.get("base") or {}).get("ref") != "main" or (data.get("head") or {}).get("sha") != head:
            raise ReleaseMergeError("release PR source moved or is not main")
        if (data.get("head") or {}).get("repo", {}).get("full_name") != repository:
            raise ReleaseMergeError("release PR must be in the protected repository")
        if (data.get("user") or {}).get("login") != "chyroc":
            raise ReleaseMergeError("release PR author is not the administrator")
        merge_commit = data.get("merge_commit_sha", "")
        if not SHA.fullmatch(merge_commit):
            raise ReleaseMergeError("release PR has no proposed merge result yet; resolve conflicts first")
        checks = json.loads(runner(["gh", "api", f"repos/{repository}/commits/{merge_commit}/check-runs"], text=True))
        matching = [check for check in checks.get("check_runs", [])
                    if check.get("name") == "All available release-PR checks passed"
                    and check.get("head_sha") == merge_commit]
        if not matching or any(check.get("status") != "completed" or check.get("conclusion") != "success" for check in matching):
            raise ReleaseMergeError("all required release-PR checks must pass at the exact merge result")
        artifact_name = f"release-pr-verified-{number}-{merge_commit}"
        artifacts = json.loads(runner(["gh", "api", f"repos/{repository}/actions/artifacts?per_page=100&page=1", "--paginate"], text=True))
        found = [item for item in artifacts.get("artifacts", [])
                 if item.get("name") == artifact_name and item.get("expired") is False]
        if not found:
            raise ReleaseMergeError("the exact verified release plan artifact is missing or expired")
    except (KeyError, TypeError, json.JSONDecodeError, subprocess.SubprocessError) as error:
        raise ReleaseMergeError("could not verify the release PR's exact merge result") from error


if __name__ == "__main__":
    try:
        require_verified(os.environ["GITHUB_REPOSITORY"], sys.argv[1], sys.argv[2], sys.argv[3])
    except (ReleaseMergeError, KeyError, IndexError) as error:
        print("release merge gate: " + str(error), file=sys.stderr)
        sys.exit(1)
