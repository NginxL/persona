"""Trusted, explicit verification lanes for an exact main commit.

These are maintainer-side checks, not PR semantic review. Configuration is local
and trusted; repository files can never add commands or relax a failed lane.
"""
from pathlib import Path

from pr_watch_common import WatchError, checked, verification_env
from pr_watch_verify import run_check


PERSONA_LANES = (
    ("Go build", ["go", "-C", "server", "build", "./..."], 3600),
    ("Go vet", ["go", "-C", "server", "vet", "./..."], 3600),
    ("Repository guards", ["bash", "{trusted}/scripts/ci/guards.sh"], 900),
    ("Script and documentation guards", ["bash", "{trusted}/scripts/ci/scripts.sh"], 1800),
)
EXTRA_LANES = {
    "server": ["bash", "{trusted}/scripts/ci/server.sh"],
    "web": ["bash", "{trusted}/scripts/ci/web.sh"],
    "android": ["bash", "{trusted}/scripts/ci/android.sh"],
    "apple": ["bash", "{trusted}/scripts/ci/apple.sh", "ios"],
    "macos": ["bash", "{trusted}/scripts/ci/apple.sh", "macos"],
    "core": ["bash", "{trusted}/scripts/ci/core.sh"],
    "runtimes": ["bash", "{trusted}/scripts/ci/runtimes.sh"],
}


def commands(config, worktree):
    """Resolve only maintainer-selected commands before running the checkout."""
    profile = config.get("profile", "persona")
    selected = []
    if profile == "persona":
        if Path(worktree, "server/go.mod").is_file():
            selected.extend(PERSONA_LANES[:2])
        selected.extend(PERSONA_LANES[2:])
    elif profile == "commands":
        for index, item in enumerate(config.get("commands", [])):
            if (not isinstance(item, dict) or not isinstance(item.get("name"), str)
                    or not item["name"].strip() or not isinstance(item.get("argv"), list)
                    or not item["argv"] or not all(isinstance(arg, str) and arg for arg in item["argv"])):
                raise WatchError("every verification command needs a name and an argv array")
            timeout = item.get("timeout_seconds", config.get("timeout_seconds", 3600))
            if not isinstance(timeout, int) or isinstance(timeout, bool) or not 60 <= timeout <= 21600:
                raise WatchError("verification timeout must be between 60 and 21600 seconds")
            selected.append((item["name"], list(item["argv"]), timeout))
        if not selected:
            raise WatchError("the commands profile needs at least one command")
    else:
        raise WatchError("verification profile must be persona or commands")
    for lane in config.get("extra_lanes", []):
        if lane not in EXTRA_LANES:
            raise WatchError("unsupported extra verification lane: " + str(lane))
        selected.append((lane, EXTRA_LANES[lane], config.get("timeout_seconds", 7200)))
    timeout = config.get("timeout_seconds", 3600)
    if not isinstance(timeout, int) or isinstance(timeout, bool) or not 60 <= timeout <= 21600:
        raise WatchError("verification timeout must be between 60 and 21600 seconds")
    # Ordinary main verification keeps the checker and tested source at one
    # commit, even after the resident watcher's own checkout becomes stale.
    # Repair verification sets trusted_checkout to the frozen original-main
    # snapshot outside the worker's writable tree; never inherit that boundary
    # from the watcher's long-lived implementation path.
    trusted = str(config.get("trusted_checkout") or Path(worktree).resolve())
    return [(name, [arg.replace("{worktree}", str(worktree)).replace("{trusted}", trusted) for arg in argv],
             item_timeout if profile == "commands" and index < len(config.get("commands", [])) else timeout)
            for index, (name, argv, item_timeout) in enumerate(selected)]


def verify(worktree, config, environ=None):
    """Run every selected lane; retain all failures for one bounded repair."""
    env = verification_env() if environ is None else environ
    # Main is trusted local source. Keep Go's normal module behavior and never
    # inject linker trimming from the PR-only optimized verification policy.
    env.pop("GOFLAGS", None)
    trusted = config.get("trusted_checkout")
    if trusted:
        # CI scripts are trusted implementation, never a different test target.
        env["PERSONA_CI_SOURCE_ROOT"] = str(worktree)
    checks = []
    for name, argv, timeout in commands(config, worktree):
        try:
            checks.append(run_check(name, argv, worktree, timeout, env))
        except WatchError as error:
            checks.append({"name": name, "command": argv, "ok": False,
                           "summary": str(error), "stdout": "", "stderr": str(error)})
    return checks


def failures(checks):
    return [check for check in checks if not check.get("ok")]


def diagnostic(check):
    """A compact, deterministic diagnosis; full output remains private state."""
    return "%s\ncommand: %s\n%s" % (
        check["name"], " ".join(check["command"]), check.get("summary", ""))
