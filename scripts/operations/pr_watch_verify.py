"""Repository-specific local verification, selected from the PR's changed paths."""
import importlib.util
import sys
import uuid
from pathlib import Path

from pr_watch_common import WatchError, bounded, checked, run, verification_env


def command_with_prefix(command, arguments):
    if isinstance(command, str):
        command = [command]
    if not isinstance(command, list) or not command or not all(isinstance(x, str) and x for x in command):
        raise WatchError("verification command must be an executable or argv prefix")
    return command + arguments


def result_summary(result):
    """Keep failed checks and their nearby errors, not just earlier passing lanes."""
    lines = (result.stdout + "\n" + result.stderr).strip().splitlines()
    selected = set()
    for index, line in enumerate(lines):
        if any(word in line for word in ("FAIL", "FAILED", "Error", "error", "panic", "Traceback", "TIMEOUT")):
            selected.update(range(max(0, index - 2), min(len(lines), index + 5)))
    if selected:
        lines = [line for index, line in enumerate(lines) if index in selected]
    return bounded("\n".join(lines), 4500)


def run_check(name, argv, worktree, timeout, environ=None):
    result = run(argv, cwd=worktree, timeout=timeout, environ=environ)
    return {"name": name, "command": argv, "ok": result.returncode == 0,
            "summary": "" if result.returncode == 0 else result_summary(result)}


def load_paths(worktree):
    """Load the PR's selector, not a stale module imported by another worker."""
    name = "pr_watch_paths_" + uuid.uuid4().hex
    spec = importlib.util.spec_from_file_location(name, Path(worktree) / "scripts" / "ci" / "paths.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def external_check(command, pr, worktree, name):
    """A trusted local adapter may supply otherwise unavailable platform evidence."""
    if not isinstance(command, list) or not command or not all(isinstance(x, str) and x for x in command):
        raise WatchError(name + " must be an argv array")
    replacements = {"worktree": str(worktree), "head": pr["headRefOid"],
                    "base": pr["baseRefOid"], "number": str(pr["number"])}
    argv = []
    for arg in command:
        for key, value in replacements.items():
            arg = arg.replace("{" + key + "}", value)
        argv.append(arg)
    return name, argv


def ui_change(names):
    for path in names:
        if path.startswith(("apple/", "web/src/", "android/", "harmony/")):
            if "/test" not in path and "/docs/" not in path and not path.endswith((".md", ".xcstrings", "strings.xml")):
                return True
    return False


def persona_checks(worktree, pr, timeout, config):
    """Use trusted watcher/base path mapping, never import PR-controlled Python."""
    base, head = pr["baseRefOid"], pr["headRefOid"]
    names = checked(["git", "diff", "--name-only", "--no-renames", base + "..." + head], cwd=worktree).splitlines()
    selected = load_paths(Path(__file__).resolve().parents[2]).select(names)
    commands = []
    for lane in ("server", "web", "android", "runtimes", "scripts"):
        if selected.get(lane):
            commands.append(("CI " + lane, ["bash", "scripts/ci/" + lane + ".sh"]))
    if selected.get("apple"):
        if not config.get("apple_command"):
            raise WatchError("Apple changes need the configured trusted macOS verifier; this host cannot verify them")
        commands.append(external_check(config["apple_command"], pr, worktree, "trusted Apple verification"))
    if ui_change(names):
        if not config.get("ui_evidence_command"):
            raise WatchError("UI changes need the configured visual-evidence verifier (dark/light screenshots and manual inspection); tests alone are insufficient")
        commands.append(external_check(config["ui_evidence_command"], pr, worktree, "visual evidence"))
    if not commands:
        raise WatchError("no verification lane selected")
    # These scripts create a disposable Postgres and do not use the production hub.
    return commands


def extension_modules(worktree, names):
    text = checked(["python3", "-I", "scripts/check-layout.py", "modules"], cwd=worktree)
    modules = [m for m in text.splitlines() if m]
    affected = set()
    for module in modules:
        if any(name == module or name.startswith(module + "/") for name in names):
            affected.add(module)
    if any(name.startswith(("scripts/", "schema/", "sdk/", "pluginkit/", ".github/"))
           or name in ("go.mod", "go.sum") for name in names):
        return None
    # Shared code can affect consumers in every module. Let go list compute the
    # actual dependency closure rather than maintaining another extension index.
    shared = [name for name in names if name.startswith("sdk/go/")]
    if shared:
        for module in modules:
            result = run(["go", "-C", module, "list", "-deps", "./..."], cwd=worktree, timeout=180)
            if result.returncode:
                raise WatchError("cannot determine extension dependencies: " + result_summary(result))
            for name in shared:
                pkg = Path(name).parent.as_posix()
                if pkg in result.stdout.splitlines():
                    affected.add(module)
    if not affected:
        return []
    return sorted(affected)


def extension_checks(worktree, pr, timeout):
    base, head = pr["baseRefOid"], pr["headRefOid"]
    names = checked(["git", "diff", "--name-only", "--no-renames", base + "..." + head], cwd=worktree).splitlines()
    modules = extension_modules(worktree, names)
    if modules is None:
        return [("extension repository CI", ["bash", "scripts/ci.sh"])]
    if not modules:
        # Documentation-only changes still have repository-wide doc/layout gates.
        return [("extension repository CI", ["bash", "scripts/ci.sh"])]
    commands = []
    for module in modules:
        commands.extend([
            (module + " tidy", ["go", "-C", module, "mod", "tidy", "-diff"]),
            (module + " vet", ["go", "-C", module, "vet", "./..."]),
            (module + " tests", ["go", "-C", module, "test", "-race", "./..."]),
        ])
        if module.startswith("extensions/"):
            identifier = module.split("/", 1)[1]
            commands.append((identifier + " package", ["bash", "scripts/package.sh", identifier]))
            if Path(worktree, module, "requirements.txt").is_file():
                commands.append((identifier + " Python tests",
                                 ["python3", "-I", "-B", "-m", "unittest", "discover", "-s", module + "/tests"]))
    return commands


def verify(repo, pr, worktree, config, environ=None):
    """Return actual completed check evidence. A failed or unavailable check raises."""
    profile = config.get("profile", "commands")
    timeout = int(config.get("timeout_seconds", 3600))
    if not 60 <= timeout <= 21600:
        raise WatchError("verification timeout must be between 60 and 21600 seconds")
    if profile == "persona":
        commands = persona_checks(worktree, pr, timeout, config)
    elif profile == "extensions":
        commands = extension_checks(worktree, pr, timeout)
    elif profile == "commands":
        raw = config.get("commands")
        if not isinstance(raw, list) or not raw:
            raise WatchError("commands profile needs a non-empty commands array")
        commands = []
        for item in raw:
            if isinstance(item, list):
                name, argv = Path(item[0]).name if item and isinstance(item[0], str) else "check", item
            elif isinstance(item, dict):
                name, argv = item.get("name"), item.get("argv")
            else:
                raise WatchError("each verification command needs argv")
            commands.append((name, command_with_prefix(argv, [])))
            if not isinstance(name, str) or not name or len(name) > 100:
                raise WatchError("invalid verification command name")
    else:
        raise WatchError("unknown verification profile")
    results = []
    for name, argv in commands:
        result = run_check(name, argv, worktree, timeout, environ)
        results.append(result)
        if not result["ok"]:
            raise WatchError("%s failed:\n%s" % (name, result["summary"]))
    return results
