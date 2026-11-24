"""Exact-head local-runner attestations, independent of semantic/hosted checks."""
import re

from pr_watch_common import WatchError, gh_json, SHA

CONTEXT = "persona/local-gate"


class LocalGateError(WatchError):
    pass


def require_local_gate(repo, head, base, read=None):
    """Trust only the latest status and a writer's explicit local-runner result.

    Statuses are bound to head by GitHub. A baseline exception is additionally
    bound to the current base, so a result from another comparison cannot pass.
    Never fall back to an older success after a newer failure or pending run.
    """
    read = read or gh_json
    if not SHA.fullmatch(head or "") or not SHA.fullmatch(base or ""):
        raise LocalGateError("Local gate: full head and base commits are required; not merged")
    try:
        statuses = read(["api", "repos/%s/commits/%s/statuses?per_page=100" % (repo, head), "--paginate"])
        if not isinstance(statuses, list) or any(not isinstance(s, dict) for s in statuses):
            raise ValueError("invalid status list")
        matching = [s for s in statuses if s.get("context") == CONTEXT]
        if not matching:
            raise LocalGateError("Local gate: no dedicated-runner attestation for head %s; not merged" % head)
        latest = max(matching, key=lambda s: int(s["id"]))
        if latest.get("state") != "success":
            raise LocalGateError("Local gate: latest dedicated-runner status is %s; not merged" % latest.get("state"))
        description = latest.get("description")
        if description not in ("EXIT_CODE=0", "BASE=%s NEW_FAILURES=0" % base):
            raise LocalGateError("Local gate: missing pass or exact-baseline no-new-failures record; not merged")
        login = latest["creator"]["login"]
        if not isinstance(login, str) or not re.fullmatch(r"[A-Za-z0-9_-]+(?:\[bot\])?", login):
            raise ValueError("invalid status author")
        permission = read(["api", "repos/%s/collaborators/%s/permission" % (repo, login)])
        if not isinstance(permission, dict) or permission.get("permission") not in ("admin", "maintain", "write"):
            raise LocalGateError("Local gate: attestation author is not a repository writer; not merged")
        return latest
    except LocalGateError:
        raise
    except (WatchError, KeyError, TypeError, ValueError) as error:
        raise LocalGateError("Local gate: unable to read trustworthy dedicated-runner evidence; not merged") from error
