"""Repository-specific local verification, selected from the PR's changed paths."""
import importlib.util
import re
import sys
import uuid
from pathlib import Path

from pr_watch_common import VerificationError, WatchError, bounded, checked, run, test_or_fix, verification_env


def command_with_prefix(command, arguments):
    if isinstance(command, str):
        command = [command]
    if not isinstance(command, list) or not command or not all(isinstance(x, str) and x for x in command):
        raise WatchError("verification command must be an executable or argv prefix")
    return command + arguments


def result_summary(result):
    """Keep terminal diagnostics and every explicit failed test, not passing noise."""
    output = (result.stdout + "\n" + result.stderr).strip()
    lines = output.splitlines()
    selected = set()
    marker = re.compile(r"(?:^--- FAIL:|^FAIL(?:\s|\t)|^FAILED(?:\s|:)|^\s*(?:AssertionError|ERROR):|"
                        r"^panic:|^Traceback|^FAIL\s+\S|^(?:fatal|time out|command not found)\b|"
                        r"\b(?:error|failed|panic|timeout)\b|\d+ tests? failed)", re.I)
    generic = re.compile(r"\b(?:error|failed|panic|timeout)\b|\d+ tests? failed", re.I)
    precise = re.compile(r"(?:^--- FAIL:|^FAIL(?:\s|\t)|^FAILED(?:\s|:)|^\s*(?:AssertionError|ERROR):|"
                         r"^panic:|^Traceback|^FAIL\s+\S|^(?:fatal|time out|command not found)\b)", re.I)
    for index, line in enumerate(lines):
        if marker.search(line) and not (line.startswith(("PASS", "ok ")) and generic.search(line) and not precise.search(line)):
            selected.update(range(max(0, index - 4), min(len(lines), index + 6)))
    if selected:
        evidence = "\n".join(line for index, line in enumerate(lines) if index in selected)
    else:
        # A nonzero command can explain itself without using a familiar keyword.
        evidence = output
    # Preserve the beginning for context and, especially, the shard's final failed-test summary.
    return bounded(evidence, 12000)


def run_check(name, argv, worktree, timeout, environ=None):
    result = run(argv, cwd=worktree, timeout=timeout, environ=environ)
    return {"name": name, "command": argv, "ok": result.returncode == 0,
            "summary": "" if result.returncode == 0 else result_summary(result),
            "stdout": result.stdout if result.returncode else "",
            "stderr": result.stderr if result.returncode else ""}


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


# Tests do not change the interface they exercise. Production source outside
# the known UI paths must still be read; SwiftUI in any file stays gated.
APPLE_NON_UI_PATHS = ("apple/",)
APPLE_VISUAL_PATHS = (
    "apple/Packages/BeautifulUI/",
    "apple/Shared/Design/",
    "apple/Shared/Localization/",
    "apple/Persona/DesignSystem/",
    "apple/Persona/Views/",
    "apple/Persona/Resources/",
    "apple/PersonaWidgets/",
    "apple/PersonaWatch/",
    "apple/LiveActivity/",
    "apple/Vision/",
)
APPLE_VISUAL_MARKERS = re.compile(
    r"\b(?:SwiftUI|UIKit|AppKit|UIView|NSView|View|ViewModifier|ViewBuilder|"
    r"NavigationStack|NavigationSplitView|TabView|GeometryReader|"
    r"Image|Color|Font|Theme|StyleSpec|ComponentSpec|ColorSpec|AppStyle|"
    r"localized|String\(localized|Text\s*\(|Button\s*\(|Label\s*\(|"
    r"XCUIApplication|XCUITest)\b|@(?:MainActor|State|Binding|ObservedObject)\b|"
    r"\.(?:frame|padding|font|foregroundStyle|background|sheet|fullScreenCover|"
    r"foregroundColor|opacity|offset|position|lineLimit|minimumScaleFactor)\s*\(")


APPLE_NON_UI_SHARED = (
    "schema/",
    "spec/",
    "scripts/ci/",
    "scripts/app/",
    "docs/",
)
APPLE_PORTABLE_CORE = (
    "native/app-core/crates/persona-core-runtime/",
    "native/app-core/crates/persona-core-ohos/",
)
APPLE_ANDROID_SHARED = (
    "android/core/rustcore/",
)


def non_ui_apple_change(worktree, base, head, names):
    """Shared backend/schema changes can use the Linux non-visual policy too."""
    if not names or not all(name.startswith(APPLE_NON_UI_PATHS + APPLE_NON_UI_SHARED +
                                          APPLE_PORTABLE_CORE + APPLE_ANDROID_SHARED) for name in names):
        return False
    for path in names:
        if path == "native/app-core/crates/persona-core-runtime/src/wire.rs":
            return False
        if not path.startswith("apple/"):
            continue
        parts = path.split("/")
        test_path = (len(parts) >= 2 and parts[1] in ("PersonaTests", "PersonaUITests")) or (
            len(parts) >= 5 and parts[1] == "Packages" and parts[3] == "Tests") or (
            len(parts) >= 4 and parts[1] == "PersonaLink" and parts[2] == "Tests")
        if path.startswith(APPLE_VISUAL_PATHS) and not test_path:
            return False
        if "/Assets" in path or "/Design" in path or "/Localization" in path:
            return False
        if path.endswith((".xcstrings", ".strings", ".stringsdict", ".png", ".jpg", ".jpeg", ".gif", ".pdf", ".svg")):
            return False
        # A UI test's own imports and assertions do not change the interface.
        if not test_path and not path.endswith(".swift"):
            return False
        if path.endswith(".swift") and not test_path:
            diff = checked(["git", "diff", "--unified=0", "--no-renames", base + "..." + head, "--", path], cwd=worktree)
            changed = [line[1:] for line in diff.splitlines()
                       if line.startswith(("+", "-")) and not line.startswith(("+++", "---"))]
            if any(APPLE_VISUAL_MARKERS.search(line) for line in changed):
                return False
    return True


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
    for lane in ("server", "web", "android", "runtimes", "scripts", "core", "desktop"):
        if selected.get(lane):
            commands.append(("CI " + lane, ["bash", "scripts/ci/" + lane + ".sh"]))
    if selected.get("apple"):
        non_ui = non_ui_apple_change(worktree, base, head, names)
        if non_ui:
            # Explicit maintainer policy for backend-only Apple PRs on a Linux
            # host. This does not claim an Apple build or UI acceptance.
            commands.append(("Apple non-UI review policy", ["true"]))
        elif not config.get("apple_command"):
            raise WatchError("Apple changes need the configured trusted macOS verifier; this host cannot verify them")
        else:
            commands.append(external_check(config["apple_command"], pr, worktree, "trusted Apple verification"))
    else:
        non_ui = False
    if ui_change(names) and not non_ui and not test_or_fix(pr, names):
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
    failures = []
    for name, argv in commands:
        result = run_check(name, argv, worktree, timeout, environ)
        results.append(result)
        if not result["ok"]:
            failures.append(VerificationError("%s failed:\n%s" % (name, result["summary"]), command=argv,
                                              output=result["stdout"] + "\n" + result["stderr"]))
    if failures:
        raise VerificationError("\n\n".join(str(failure) for failure in failures),
                                output="\n\n".join(failure.output for failure in failures),
                                command=failures[0].command,
                                failures=[{"command": failure.command, "output": failure.output} for failure in failures],
                                checks=results)
    return results
