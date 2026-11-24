#!/usr/bin/env python3
"""check-fast's per-path table: the local fast checks each changed path selects (#11535).

scripts/ci/paths.py picks CI's lanes (which the PR watcher reads too) and check-fast's web tests; this table, beside
it, picks the rest of the checks scripts/test/check-fast.sh runs for a diff. A new mapping is a rule in TABLE. The
table is a module of its own because importing paths.py scans web/ (about half a second), while check-fast asks this
one on every run; it imports nothing outside the standard library.

    python3 scripts/ci/check_fast_map.py < paths    # one line per path read: its checks, tab-separated, in order

check-fast.sh keeps what it finds at run time or across a rename: the server packages whose tests read a changed
fixture, schema, doc or client source (git grep), the runtime packages that name a changed fixture or schema, the
scripts suites named after a changed script or importing it, the suites a dedicated check runs instead
(script_test_add), and the frozen-migration and fixed-sleep checks, which also look at a rename's old path.
--all runs every check and does not read the table.
"""
import fnmatch
import json
from pathlib import Path
import posixpath
import re
import sys

ROOT = Path(__file__).resolve().parents[2]


def _counts_table_files(root=ROOT):
    """Every file the counts surface table names (spec/fixtures/counts/surfaces.json, #11900): each surface's code and
    tests, and a hub-fed surface's fallback. Its guard reads them all, so a rename in any of them runs it. Read on each
    run, so a new row needs no edit here; a missing or unreadable table names none, and its own rule still runs the
    guard."""
    try:
        rows = json.loads((root / "spec/fixtures/counts/surfaces.json").read_text(encoding="utf-8"))["surfaces"]
        files = {entry["file"] for row in rows for key in ("code", "tests") for entry in row.get(key, ())}
        files |= {row["view"]["fallback"]["file"] for row in rows if isinstance(row.get("view"), dict)}
    except (OSError, ValueError, KeyError, TypeError, AttributeError):
        return ()
    return tuple(sorted(path for path in files if isinstance(path, str) and path))

# What each check is; check-fast.sh's plan_changes turns each one into the jobs it schedules. A check with a / in it is
# a scripts/**/test_*.py suite, run from its own directory as CI's Scripts tests job does (script_test_add: one that
# does not exist or never mentions unittest is not run).
CHECKS = {
    "web": "the web check: npm ci if web/node_modules is missing, node_modules against package-lock.json, the "
           "typecheck, then the tests python3 scripts/ci/paths.py --web-plan picks (#10619)",
    "web-install": "npm ci before the web check, even with web/node_modules there: a changed manifest can leave it "
                   "stale",
    "site": "node --test site/worker.test.mjs",
    "integration": "node --test test/*.test.{js,mjs} in integrations/<pkg>, the path's package (none without tests)",
    "sdk-ts": "bun test in sdk/ts (a SKIP line when bun is absent)",
    "deno-adapter": "go test ./internal/extension/denohost/...: the trusted Deno adapter bundles sdk/ts/src, and "
                    "TestTrustedAdapterMatchesOfficialSDK fails while the committed one is stale (#4221; needs bun)",
    "go-package": "go test ./<top>/<name>/... in server/ for server/internal/<name>, server/cmd/<name>, "
                  "server/plugins/<name> or server/scripts/<name>, the path's package group",
    "server-build": "go build ./... in server/: a server change no package group covers",
    "go-guards": "the server's repo-wide guard and contract tests (GO_GUARD_PKGS, GO_GUARD_TEST_PKGS, GO_GUARD_TESTS "
                 "in check-fast.sh): they read other packages' source, go list output, the migrations or sdk/go "
                 "(#1936), scan the module for text and prompts (i18n, prompts), check what each program links, or pin "
                 "what other packages render, such as the prompt and MCP goldens and the wire samples (#12842)",
    "go-fmt": "python3 scripts/ci/gofmt.py on each changed .go file under server/ and sdk/go/ (the trees CI's Go "
              "formatting step covers), all in one job that takes no go slot (#12894)",
    "go-vet": "go vet -ldflags= ./... in server/, and GOOS=windows GOARCH=amd64 go vet -ldflags= of the path's package "
              "group, or of ./... for a server or sdk/go path outside every group, in one job of its own (#12894); a "
              "group with no package that builds for Windows, or that is gone, is left out of the Windows step",
    "sdk-go": "go test ./... in the sdk/go module",
    "android": "scripts/app/android_i18n_check.py, the slot order, token and motion baselines, and four of their "
               "unittests (no Gradle)",
    "pr-needs-issue": "node scripts/test/pr_needs_issue_test.mjs",
    "extension-schema": "python3 scripts/test/test_extension_schema.py (it needs gofmt)",
    "sdk-bundle": "python3 scripts/test/test_sdk_bundle.py",
    "external-refs": "python3 scripts/test/test_external_service_refs.py",
    "apple": "a SKIP line: the iOS and macOS builds and tests need a Mac",
    "unmapped": "an INFO line naming the path: no fast check maps to it",
}


def _vet_group(parts):
    """The server package group go vet checks a path's change in for Windows: <top>/<name> for a path under
    server/{internal,cmd,plugins,scripts}/<name> (go-package's groups), and . (the whole module) for other server paths,
    such as go.mod or a file directly in server/plugins/, and for sdk/go, which the server module imports."""
    if len(parts) > 3 and parts[0] == "server" and parts[1] in ("internal", "cmd", "plugins", "scripts"):
        return parts[1] + "/" + parts[2]
    return "."


# The checks that take an argument from the path: the integrations/<pkg> package, the server/<top>/<name> group, and
# the group go vet checks for Windows.
_FROM_PATH = {
    "integration": lambda parts: parts[1],
    "go-package": lambda parts: parts[1] + "/" + parts[2],
    "go-vet": _vet_group,
}

# The table. Each group is a bash case statement: its rules are tried in order, the first one with a pattern that
# matches the path gives its checks, and the rules after it are not tried. Every group is tried for every path, and
# the checks come out in the table's order. A pattern is an fnmatch pattern for the whole path in which * matches /
# too, as in a case statement; "name:<pattern>" matches the file name instead (the part after the last /).
TABLE = (
    # The checks of the path's own area.
    (
        # A changed manifest can make the installed node_modules stale, even when the diff touched package.json only.
        (("web/package.json", "web/package-lock.json"), ("web", "web-install")),
        # Files outside web/ that web/src and its tests read also run the web check: paths.py --web-plan finds them.
        (("web/*",), ("web",)),
        (("site/*",), ("site",)),
        (("integrations/*/*",), ("integration",)),
        (("sdk/ts/src/*",), ("sdk-ts", "deno-adapter")),
        (("sdk/ts/*",), ("sdk-ts",)),
        # Read by server tests: check-fast.sh finds the packages that read them.
        (("spec/fixtures/*",), ()),
        # test_gen_tokens.py and test_gen_icon_meanings.py, in the suites below.
        (("spec/tokens/*", "spec/icons/*"), ()),
        (("server/internal/*/*", "server/cmd/*/*", "server/plugins/*/*", "server/scripts/*/*"), ("go-package",)),
        # A file directly in server/plugins/ is the plugins package, which the repo-wide guards run.
        (("server/plugins/*",), ()),
        (("server/*",), ("server-build",)),
        # sdk/go is its own module: its tests run there, not server's build.
        (("sdk/go/*",), ("sdk-go",)),
        (("android/*",), ("android",)),
        ((".github/*",), ("pr-needs-issue",)),
        # CI's extension-schema job: the conformance test, the sdk bundle test and the Go wire round trip.
        (("schema/*",), ("extension-schema", "sdk-bundle", "sdk-go")),
        # The i18n and token checkers and four of their test_android_* unittests belong to the android check.
        (("scripts/app/*android*",), ("android",)),
        (("scripts/test/test_external_service_refs.py",), ("external-refs",)),
        (("scripts/gen/sdk_bundle.py", "scripts/test/test_sdk_bundle.py"), ("sdk-bundle",)),
        (("scripts/gen/*extension*", "scripts/test/test_extension_schema.py"), ("extension-schema",)),
        # Any other script, nested ones included: the seconds-long repository checks.
        (("scripts/*",), ("pr-needs-issue", "extension-schema")),
        (("apple/*",), ("apple",)),
        # Docs, markdown, deploy/docker and the root Dockerfile, and two native/ build scripts: their suites below.
        (("docs/*", "deploy/docker/*", "Dockerfile", "*.md"), ()),
        (("native/app-core/scripts/build-apple.sh", "native/iphone-bridge/build.sh"), ()),
        (("*",), ("unmapped",)),
    ),
    # On top of those: the repo-wide guards fail on code outside their own package, and go vet checks the whole module
    # and, for Windows, the changed group (#12894). Each is a job of its own: a vet that runs past its timeout on a cold
    # build cache then cannot turn a contract test's FAIL into the guard job's TIMEOUT.
    (
        (("server/*", "sdk/go/*"), ("go-guards", "go-vet")),
    ),
    # gofmt reads only the changed Go files of the trees CI's Go formatting step covers (#12894).
    (
        (("server/*.go", "sdk/go/*.go"), ("go-fmt",)),
    ),
    # The suites of CI's Scripts tests job that guard files outside scripts/, or that run or read a script by its path
    # rather than importing it. The docs: quoted paths and the CLAUDE.md index, Go paths, quoted section names, and the
    # wire lists in protocol.md and extensions.md, whose code side is in proto, hub, extension and the manifest schema.
    (
        (("docs/*", "*.md"), ("scripts/test/test_doc_paths.py", "scripts/test/test_doc_go_paths.py",
                              "scripts/test/test_doc_sections.py", "scripts/test/test_doc_wire.py")),
        (("server/internal/proto/*", "server/internal/hub/*", "server/internal/extension/*",
          "schema/extension/manifest.schema.json"), ("scripts/test/test_doc_wire.py",)),
    ),
    # AGENTS.md is CLAUDE.md through a symlink (#12723): the test reads how git records both, so either one's change
    # runs it, on top of the doc suites above.
    (
        (("AGENTS.md", "CLAUDE.md"), ("scripts/test/test_agents_md.py",)),
    ),
    # The name freezes (docs/server/architecture.md "Registering a domain's entries", #12844): test_name_freezes.py
    # runs the archguard test that reads the central lists' names and their files, and mock_hub_switches.py takes its
    # list format and the open PRs' heads from name_freezes.py.
    (
        (("server/internal/archguard/central_lists_test.go", "server/internal/archguard/testdata/central_lists/*"),
         ("scripts/test/test_name_freezes.py",)),
        (("scripts/test/name_freezes.py",), ("scripts/mock/test_mock_hub.py",)),
    ),
    # The Android modules where a dropped result is still a warning (#13024): test_android_return_values.py reads their
    # list in android/build-logic and the call sites it names in the modules' sources.
    (
        (("android/*",), ("scripts/test/test_android_return_values.py",)),
    ),
    # Most section names test_doc_sections.py checks sit in code comments under its SOURCE_DIRS (about a second).
    (
        (("server/*", "web/src/*", "web/e2e/*", "scripts/*", "sdk/*", "apple/*", "android/*", "integrations/*",
          "spec/*", "schema/*"), ("scripts/test/test_doc_sections.py",)),
    ),
    # String Catalogs and Chinese Swift literals.
    (
        (("apple/*",), ("scripts/test/test_apple_l10n.py",)),
    ),
    # Every Apple source and build file, and the xcconfig persona_config.py writes, against CallKit (#9290).
    (
        (("apple/*", "scripts/persona_config.py"), ("scripts/test/test_apple_no_callkit.py",)),
    ),
    # The development build (#10713): the switch that leaves the watch app and the share extension out, who sets it,
    # the Release-only commit stamp; and the Rust core's XCFramework cache shared between worktrees.
    # test_apple_dev_build.py reads the project and the includes its xcodegen dumps turn on, every script that
    # generates a project or builds the Rust core it names, and the note a lean build prints (#11250). Its two searches
    # of the whole tree (who sets the switch, who runs xcodegen generate) also see a new caller; that one is not mapped.
    (
        (("apple/project.yml", "apple/app-entitlements.yml", "apple/watch-and-share.yml", "apple/lean-dev-build.yml",
          "apple/siri-calls.yml", "scripts/app/sim.sh", "scripts/app/ui-test.sh", "scripts/app/perf-test.sh",
          "scripts/app/share-from-apps.sh", "scripts/app/lean-build-note.sh", "scripts/app/island_peer.sh",
          "scripts/app/stamp_debug_build_info.sh",
          "scripts/diag/stream-scroll/run-ios.sh", "scripts/release/release.sh", "scripts/ci/apple.sh",
          "native/iphone-bridge/build.sh"), ("scripts/test/test_apple_dev_build.py",)),
        (("native/app-core/scripts/build-apple.sh",), ("scripts/test/test_app_core_apple_build.py",)),
    ),
    # test_gen_tokens.py checks the token sources against the iOS values and the committed Android Tokens.kt (#4209).
    (
        (("spec/tokens/*", "apple/Shared/Design/*",
          "android/core/designsystem/src/main/kotlin/persona/core/designsystem/Tokens.kt"),
         ("scripts/assets/test_gen_tokens.py",)),
    ),
    # test_gen_icon_meanings.py checks spec/icons against Theme.Icon and the Phosphor glyphs, and PersonaIcons.kt.
    (
        (("spec/icons/*", "apple/Shared/Design/Icons.swift",
          "android/core/designsystem/src/main/kotlin/persona/core/designsystem/icons/*"),
         ("scripts/assets/test_gen_icon_meanings.py",)),
    ),
    # Suites whose names do not match the file they test: test_privacy_check.py and test_check_deno_adapter_sh.py copy
    # scripts/hooks into their temp repositories and run the hooks there (the latter also scripts/gen/deno_adapter.sh);
    # check_bundle_privacy.py imports privacy_check; test_merge_pr_sh.py copies privacy_check.py into its temp
    # repositories, where merge-pr.sh runs it, and checks that pr_watch_merge.py's merge lock excludes merge-pr.sh's.
    # It copies changed_ratchets.py and the two ratchets it imports there too (#7977), with ratchet_base.py, which
    # they and the prompt-language ratchet import (#12785); test_changed_ratchets.py imports them, and the ratchets'
    # own suites load ratchet_base.py through them (test_prompt_language_ratchet.py copies both). test_ci.py and test_check_fast_inputs.py run server.sh and check-fast.sh with go_trimpath.sh, which composes
    # their GOFLAGS; test_check_fast_inputs.py runs check-fast.sh with heavy_slot.py too, and test_android_sh.py runs
    # android.sh with it (#11250). check-fast.sh's tests are six files (#11017, #14321): its own name picks
    # test_check_fast.py, and the other five, which import its fixtures, come along. They copy check_fast_compare.py,
    # which names a failure's rerun and compares it with the merge base (#12733), into their temp repositories too,
    # and go_test_partitioned.py with the go_partitions.py it imports, which every Go group runs through (#12765).
    (
        (("scripts/hooks/*",), ("scripts/test/test_privacy_check.py", "scripts/test/test_check_deno_adapter_sh.py")),
        (("scripts/gen/deno_adapter.sh",), ("scripts/test/test_check_deno_adapter_sh.py",)),
        (("scripts/privacy_check.py",),
         ("scripts/release/test_check_bundle_privacy.py", "scripts/test/test_merge_pr_sh.py")),
        (("scripts/operations/pr_watch_merge.py", "scripts/ci/changed_ratchets.py"),
         ("scripts/test/test_merge_pr_sh.py",)),
        (("scripts/ci/sleep_ratchet.py", "scripts/ci/web_font_ratchet.py"),
         ("scripts/ci/test_changed_ratchets.py", "scripts/test/test_merge_pr_sh.py")),
        (("scripts/ci/ratchet_base.py",),
         ("scripts/ci/test_changed_ratchets.py", "scripts/test/test_merge_pr_sh.py", "scripts/ci/test_sleep_ratchet.py",
          "scripts/ci/test_web_font_ratchet.py", "scripts/test/test_prompt_language_ratchet.py",
          "scripts/ci/test_web_module_boundary_ratchet.py")),
        (("scripts/ci/prompt_language_ratchet.py",), ("scripts/test/test_prompt_language_ratchet.py",)),
        (("scripts/ci/web_module_boundary_ratchet.py",), ("scripts/ci/test_web_module_boundary_ratchet.py",)),
        (("scripts/test/go_trimpath.sh",), ("scripts/ci/test_ci.py", "scripts/test/test_check_fast_inputs.py")),
        (("scripts/heavy_slot.py",), ("scripts/test/test_check_fast_inputs.py", "scripts/test/test_android_sh.py")),
        (("scripts/check-fast.sh", "scripts/test/check-fast.sh", "scripts/test/test_check_fast.py",
          "scripts/ci/affected_drift.py"),
         ("scripts/test/test_check_fast_guards.py", "scripts/test/test_check_fast_inputs.py",
          "scripts/test/test_check_fast_base.py", "scripts/test/test_check_fast_reports.py",
          "scripts/test/test_check_fast_callers.py")),
        (("scripts/ci/check_fast_compare.py", "scripts/test/go_test_partitioned.py", "scripts/test/go_partitions.py"),
         ("scripts/test/test_check_fast.py", "scripts/test/test_check_fast_guards.py",
          "scripts/test/test_check_fast_inputs.py", "scripts/test/test_check_fast_base.py",
          "scripts/test/test_check_fast_reports.py", "scripts/test/test_check_fast_callers.py")),
    ),
    # test_heavy_slot.py reads CI's lanes, the operations scripts (the watchers among them), their example configs and
    # services, and both watcher skills: one that names a script taking a slot must set PERSONA_HEAVY_SLOTS=0
    # (docs/operations/testing.md "Heavy build slots"). A new lane script once broke it unseen (#11026). It skips
    # test_* files, as this does.
    (
        (("name:test_*",), ()),
        ((".github/workflows/*.yml", "scripts/ci/*.sh", "scripts/ci/*.py", "scripts/operations/*.py",
          "scripts/operations/*watch*.json", "scripts/operations/*watch*.service",
          ".claude/skills/pr-review-merge-loop/*", ".claude/skills/main-verify-fix-loop/*"),
         ("scripts/test/test_heavy_slot.py",)),
    ),
    # The selections of what a change affects (#11026): CI's lanes (paths.py, tested in test_ci.py and, for the web
    # plan and the lanes a plan names, test_check_fast_inputs.py), check-fast (check-fast.sh and this table, which
    # check-fast's six test files run) and the watcher's lanes; test_affected_drift.py compares them.
    (
        (("scripts/ci/paths.py",), ("scripts/ci/test_ci.py", "scripts/test/test_check_fast_inputs.py",
                                    "scripts/ci/test_affected_drift.py")),
        (("scripts/test/check-fast.sh", "scripts/ci/affected_drift.py"), ("scripts/ci/test_affected_drift.py",)),
        (("scripts/ci/check_fast_map.py",),
         ("scripts/test/test_check_fast.py", "scripts/test/test_check_fast_guards.py",
          "scripts/test/test_check_fast_inputs.py", "scripts/test/test_check_fast_base.py",
          "scripts/test/test_check_fast_reports.py", "scripts/test/test_check_fast_callers.py",
          "scripts/ci/test_affected_drift.py")),
        (("scripts/operations/pr_watch_verify.py",),
         ("scripts/operations/test_pr_watch.py", "scripts/ci/test_affected_drift.py")),
        (("scripts/operations/pr_watch_*.py",), ("scripts/operations/test_pr_watch.py",)),
    ),
    # The Release binary's DEBUG checks (#10616, #10654): a list that matches no script name, whose union merges can
    # leave it out of order, the checker test_apple_release_switch_names.py imports, and release.sh, which runs it.
    (
        (("scripts/release/debug_switches.txt", "scripts/release/debug_switches.py", "scripts/release/release.sh"),
         ("scripts/release/test_debug_switches.py", "scripts/release/test_release_debug_guard.py",
          "scripts/test/test_apple_release_switch_names.py")),
    ),
    # test_docker_entrypoint.py also reads the root Dockerfile.
    (
        (("deploy/docker/*", "docs/operations/docker.md", "Dockerfile"), ("scripts/test/test_docker_entrypoint.py",)),
    ),
    # test_wire_schema.py holds every sample the shared fixtures keep for a wire schema to that schema, at any depth;
    # it imports the extension test's validator.
    (
        (("schema/wire/*", "spec/fixtures/*", "scripts/gen/extension_schema.py",
          "scripts/test/test_extension_schema.py"), ("scripts/test/test_wire_schema.py",)),
    ),
    # test_todo_fixtures.py holds the to-do page and window fixtures to their READMEs' rules; it also reads the view
    # schema, the todo-view line keys, the agent names and the row fields of the hub's Todo and listedTodo, and imports
    # the wire and extension schema tests.
    (
        (("spec/fixtures/todo-detail/*", "spec/fixtures/todo-layout/*", "spec/fixtures/todo-view/README.md",
          "spec/fixtures/agent-names/*", "schema/wire/todo-view.schema.json", "server/internal/assistant/todos.go",
          "server/internal/assistant/todo_ask.go", "scripts/test/test_wire_schema.py",
          "scripts/test/test_extension_schema.py"), ("scripts/test/test_todo_fixtures.py",)),
    ),
    # The counts surface table (#11900): its guard reads the table, its README, the needs-you cases its replays report
    # on, the plan's decisions and every file the table names; the lane differ's tests read the table and those cases.
    (
        (("spec/fixtures/counts/*", "spec/fixtures/needs-you/*"),
         ("scripts/test/test_counts_surfaces.py", "scripts/test/test_counts_agreement.py")),
        (("docs/plans/plan-derived-counts.md", *_counts_table_files()), ("scripts/test/test_counts_surfaces.py",)),
    ),
)

_NAME = "name:"


def _matcher(pattern):
    if pattern.startswith(_NAME):
        compiled = re.compile(fnmatch.translate(pattern[len(_NAME):]))
        return lambda path: compiled.match(posixpath.basename(path)) is not None
    compiled = re.compile(fnmatch.translate(pattern))
    return lambda path: compiled.match(path) is not None


_GROUPS = [[([_matcher(pattern) for pattern in listed], named) for listed, named in group] for group in TABLE]


def patterns():
    """Every path pattern of the table, in order (the name: patterns left out)."""
    return [pattern for group in TABLE for listed, _ in group for pattern in listed if not pattern.startswith(_NAME)]


def suites():
    """Every scripts/**/test_*.py suite the table names, in order, once."""
    return list(dict.fromkeys(check for group in TABLE for _, named in group for check in named if "/" in check))


def checks(path):
    """The checks of one path, in the table's order: a CHECKS name, <check>:<argument> for a check that takes its
    argument from the path (integration:<pkg>, go-package:<top>/<name>, go-vet:<top>/<name> or go-vet:.), or
    suite:<scripts/**/test_*.py>."""
    found = []
    parts = path.split("/")
    for group in _GROUPS:
        for matchers, named in group:
            if any(match(path) for match in matchers):
                for check in named:
                    if "/" in check:
                        found.append("suite:" + check)
                    elif check in _FROM_PATH:
                        found.append(check + ":" + _FROM_PATH[check](parts))
                    else:
                        found.append(check)
                break
    return found


def main(argv=None):
    args = sys.argv[1:] if argv is None else argv
    if args:
        print(__doc__.strip(), file=sys.stderr if args[0] not in ("-h", "--help") else sys.stdout)
        return 0 if args[0] in ("-h", "--help") else 2
    # Bytes in and out: one line per line read, whatever its encoding, so check-fast can read them side by side.
    lines = sys.stdin.buffer.read().decode("utf-8", "surrogateescape").split("\n")
    if lines and lines[-1] == "":
        lines.pop()
    out = "".join("\t".join(checks(path)) + "\n" for path in lines)
    sys.stdout.buffer.write(out.encode("utf-8", "surrogateescape"))
    return 0


if __name__ == "__main__":
    sys.exit(main())
