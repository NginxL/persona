"""check-fast.sh on the inputs of the server and web checks: the repository files server tests read select go test on
the packages that read them, go and vitest run in a machine-wide slot (#10714), and the web check runs the vitest files
that the paths web/src and its tests name and read select (#10619), down to scripts/ci/paths.py's scan of the real web/.
The plan tests (#11026) cover --dry-run, --changed-from and --each, the CI lanes paths.py selects that check-fast runs
no part of, and the runtime packages that read a changed fixture or schema.

Split from test_check_fast.py (#11017), which tests how check-fast picks the scripts/ suites and stops on a signal, so
each file stays well under the per-suite limit of scripts/ci/python-tests.py on a loaded machine. Each test builds a
throwaway repository with check-fast.sh and stubs, as there, and runs it with fake go, node, npm and npx that record
their calls instead of building or testing anything.
"""
import fnmatch
import importlib.util
import os
from pathlib import Path
import re
import shutil
import signal
import subprocess
import sys
import tempfile
import time
import unittest

try:
    import fcntl
except ImportError:  # Windows
    fcntl = None

# The stubbed suites, the passing test and the copy of check-fast the throwaway repositories share with
# test_check_fast.py.
from test_check_fast import GUARD_SUITES, PASSING, copy_check_fast

ROOT = Path(__file__).resolve().parents[2]


FAKE_GO = '#!/bin/sh\nprintf "%s|%s%s\\n" "${PWD##*/}" "${GOOS:+GOOS=$GOOS }" "$*" >> "$FAKE_GO_LOG"\n'
# A gofmt that records each call ("<slot held>|<args>") and lists, as gofmt -l does, the named files that hold the
# marker: a planted unformatted file.
UNFORMATTED = "not gofmt-formatted"
FAKE_GOFMT = ('#!/bin/sh\nprintf "%s|%s\\n" "${PERSONA_HEAVY_SLOT_HELD:-}" "$*" >> "$FAKE_GOFMT_LOG"\n'
              '[ "$1" = -l ] && shift\nfor f in "$@"; do grep -l "' + UNFORMATTED + '" "$f"; done\nexit 0\n')
# The package lists of check-fast's two repo-wide guard calls: whole packages, and the named guard and contract tests
# (-run) of packages whose other tests are slow or need Postgres (#12842).
GUARD_PACKAGES = "./internal/lazyre ./internal/archguard ./internal/tzguard ./plugins ./internal/i18n ./internal/prompts"
GUARD_RUN_PACKAGES = ("./internal/hub ./internal/assistant ./internal/update ./cmd/persona-hub ./cmd/persona-manager "
                      "./cmd/persona-node")


@unittest.skipUnless(shutil.which("timeout") or shutil.which("gtimeout"), "check-fast.sh needs GNU timeout")
class CheckFastServerInputsTests(unittest.TestCase):
    """Repository inputs outside server/ that server tests read run go test on the packages that read them."""

    def setUp(self):
        self.repo = Path(tempfile.mkdtemp(prefix="check-fast-test-"))
        self.addCleanup(shutil.rmtree, self.repo)
        (self.repo / "scripts/test").mkdir(parents=True)
        copy_check_fast(self.repo)
        self.write("scripts/privacy_check.py", "print('clean')\n")
        self.write("scripts/test/test_extension_schema.py", "")
        self.write("scripts/test/pr_needs_issue_test.mjs", "")
        self.write("scripts/test/server_test_env.py", "")
        self.write("scripts/test/test_sdk_bundle.py", "")  # a schema/ change also runs the sdk bundle and sdk/go jobs
        self.write("sdk/go/go.mod", "module example.com/sdk\n")
        self.write("spec/fixtures/x.json", "{}\n")
        self.write("spec/tokens/colors.json", "{}\n")
        self.write("docs/example-guide.md", "guide\n")
        self.write("docs/other.md", "other\n")
        self.write("schema/extension/examples/Manifest.x.json", "{}\n")
        self.write("web/package.json", "{}\n")  # a schema/extension change also runs the web check (stubbed npm)
        self.write("server/go.mod", "module example.com/server\n")
        self.write("server/internal/foo/foo_test.go", 'package foo\n\nconst dir = "../../../spec/fixtures/x"\n')
        self.write("server/internal/joined/joined_test.go",
                   'package joined\n\nvar dir = filepath.Join("..", "..", "..", "spec", "fixtures", "wire")\n')
        self.write("server/internal/noted/noted_test.go", 'package noted\n\n// Same as spec/fixtures/x.json.\n')
        self.write("server/internal/bar/bar_test.go", "package bar\n")
        self.write("server/internal/guide/conformance/guide_test.go",
                   'package conformance\n\nconst guide = "docs/example-guide.md"\n')
        self.write("server/internal/ext/ext_test.go",
                   'package ext\n\nvar d = filepath.Join("..", "schema", "extension", "examples")\n')
        self.write("server/internal/db/dbtest/env_test.go",
                   'package dbtest\n\nconst launcher = "../../../../scripts/test/server_test_env.py"\n')
        self.git("init", "-q")
        self.git("add", "-A")
        self.git("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "base")
        self.git("tag", "base")
        # A fake go outside the repository records each call ("<cwd basename>|<args>") instead of building anything.
        bin_dir = Path(tempfile.mkdtemp(prefix="check-fast-go-"))
        self.addCleanup(shutil.rmtree, bin_dir)
        (bin_dir / "go").write_text(FAKE_GO)
        (bin_dir / "go").chmod(0o755)
        (bin_dir / "gofmt").write_text(FAKE_GOFMT)
        (bin_dir / "gofmt").chmod(0o755)
        (bin_dir / "npm").write_text("#!/bin/sh\n")  # passes npm ci, typecheck and vitest without running them
        (bin_dir / "npm").chmod(0o755)
        self.go_log = bin_dir / "calls.log"
        self.gofmt_log = bin_dir / "gofmt.log"
        # The fake go records GOOS only as check-fast's Windows vet sets it.
        self.env = {**{key: value for key, value in os.environ.items() if key not in ("GOOS", "GOARCH")},
                    "PATH": f"{bin_dir}{os.pathsep}{os.environ['PATH']}", "FAKE_GO_LOG": str(self.go_log),
                    "FAKE_GOFMT_LOG": str(self.gofmt_log)}

    def write(self, path, text):
        file = self.repo / path
        file.parent.mkdir(parents=True, exist_ok=True)
        file.write_text(text)

    def git(self, *args):
        subprocess.run(["git", "-C", str(self.repo), *args], check=True, capture_output=True)

    def check_fast(self, code=0):
        run = subprocess.run(["bash", "scripts/test/check-fast.sh", "--base", "base"], cwd=self.repo, env=self.env,
                             capture_output=True, text=True, timeout=300)
        out = run.stdout + run.stderr
        self.assertEqual(code, run.returncode, out)
        calls = self.go_log.read_text().splitlines() if self.go_log.exists() else []
        # The package pattern is the last argument of each recorded "go test ... ./internal/<pkg>/..." call.
        return out, sorted(call.split()[-1] for call in calls if call.startswith("server|test "))

    def go_calls(self):
        """Every recorded go call ("<cwd basename>|<args>") without the -timeout value check-fast derives from its budget."""
        calls = self.go_log.read_text().splitlines() if self.go_log.exists() else []
        return [re.sub(r" -timeout \S+", "", call) for call in calls]

    def guard_calls(self):
        return [call for call in self.go_calls() if call.endswith((" " + GUARD_PACKAGES, " " + GUARD_RUN_PACKAGES))]

    def vet_calls(self):
        return [call for call in self.go_calls() if call.startswith(("server|vet ", "server|GOOS=windows vet "))]

    def gofmt_calls(self):
        return self.gofmt_log.read_text().splitlines() if self.gofmt_log.exists() else []

    def test_a_spec_fixture_runs_the_packages_that_read_it(self):
        self.write("spec/fixtures/x.json", '{"changed": true}\n')
        out, packages = self.check_fast()
        self.assertIn("queued: server go test ./internal/foo/...", out)
        # filepath.Join elements name the fixtures too; a // comment that mentions them is not a reader.
        self.assertEqual(["./internal/foo/...", "./internal/joined/..."], packages, out)
        self.assertNotIn("no fast check maps to", out)

    def test_other_spec_files_stay_unmapped(self):
        self.write("spec/other/x.json", '{"changed": true}\n')
        out, packages = self.check_fast()
        self.assertEqual([], packages, out)
        self.assertIn("no fast check maps to: spec/other/x.json", out)

    def test_a_token_source_runs_no_server_package(self):
        self.write("spec/tokens/colors.json", '{"changed": true}\n')
        out, packages = self.check_fast()
        self.assertEqual([], packages, out)
        self.assertNotIn("no fast check maps to", out)

    def test_a_doc_runs_only_the_package_that_reads_it(self):
        self.write("docs/example-guide.md", "guide changed\n")
        self.write("docs/other.md", "other changed\n")
        out, packages = self.check_fast()
        self.assertEqual(["./internal/guide/conformance/..."], packages, out)

    def test_a_schema_example_runs_its_readers(self):
        self.write("schema/extension/examples/Manifest.x.json", '{"changed": true}\n')
        out, packages = self.check_fast()
        self.assertEqual(["./internal/ext/..."], packages, out)

    def test_a_wire_schema_runs_its_readers(self):
        self.write("server/internal/wirereader/wire_test.go",
                   'package wirereader\n\nvar s = filepath.Join("..", "..", "..", "schema", "wire", "x.schema.json")\n')
        self.git("add", "-A")
        self.git("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "reader")
        self.git("tag", "-f", "base")
        self.write("schema/wire/x.schema.json", '{"changed": true}\n')
        out, packages = self.check_fast()
        self.assertEqual(["./internal/wirereader/..."], packages, out)

    def test_any_schema_family_runs_its_readers(self):
        # internal/configuration reads schema/configuration, a family the reader rules once left out (#11026).
        self.write("server/internal/configuration/configuration_test.go",
                   'package configuration\n\nvar raw, _ = os.ReadFile("../../../schema/configuration/all-types.json")\n')
        self.write("schema/configuration/all-types.json", "{}\n")
        self.git("add", "-A")
        self.git("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "reader")
        self.git("tag", "-f", "base")
        self.write("schema/configuration/all-types.json", '{"changed": true}\n')
        out, packages = self.check_fast()
        self.assertEqual(["./internal/configuration/..."], packages, out)

    def test_the_server_test_launcher_runs_its_reader(self):
        self.write("scripts/test/server_test_env.py", "# changed\n")
        out, packages = self.check_fast()
        self.assertEqual(["./internal/db/dbtest/..."], packages, out)

    def test_a_client_source_runs_the_packages_that_name_it_whole(self):
        # The Go contract tests read Swift and Kotlin sources by their whole path (internal/proto, #11026); a longer
        # string that only mentions the path, such as a wire sample's "apple/Shared/Models.swift: Fold", reads nothing.
        self.write("server/internal/contract/contract_test.go",
                   'package contract\n\nvar models = read("apple/Shared/Models.swift")\n'
                   'var calls = read("android/core/contracts/src/Calls.kt")\n')
        self.write("server/internal/samples/samples_test.go",
                   'package samples\n\nconst source = "apple/Shared/Models.swift: Fold"\n')
        self.write("apple/Shared/Models.swift", "struct Fold {}\n")
        self.write("apple/Shared/Other.swift", "struct Other {}\n")
        self.write("android/core/contracts/src/Calls.kt", "class Calls\n")
        self.git("add", "-A")
        self.git("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "readers")
        self.git("tag", "-f", "base")
        self.write("apple/Shared/Other.swift", "struct Other { let id = 1 }\n")
        out, packages = self.check_fast()
        self.assertEqual([], packages, out)
        self.write("apple/Shared/Models.swift", "struct Fold { let id = 1 }\n")
        out, packages = self.check_fast()
        self.assertEqual(["./internal/contract/..."], packages, out)
        self.git("checkout", "-q", "--", "apple")
        self.write("android/core/contracts/src/Calls.kt", "class Calls(val id: Int)\n")
        # A plan only: the android checks it also queues need the real checkers.
        run = subprocess.run(["bash", "scripts/test/check-fast.sh", "--dry-run", "--base", "base"], cwd=self.repo,
                             env=self.env, capture_output=True, text=True, timeout=300)
        self.assertEqual(0, run.returncode, run.stdout + run.stderr)
        self.assertIn("queued: server go test ./internal/contract/...", run.stdout)
        self.assertIn("queued: android i18n", run.stdout)

    def plan(self, listed, env=None):
        """check-fast's plan for the listed paths: (exit code, output)."""
        run = subprocess.run(["bash", "scripts/test/check-fast.sh", "--dry-run", "--changed-from", "-"], cwd=self.repo,
                             env=env or self.env, input="".join(p + "\n" for p in listed), capture_output=True,
                             text=True, timeout=300)
        return run.returncode, run.stdout + run.stderr

    def test_a_large_client_diff_still_runs_the_packages_that_name_its_files(self):
        # One alternation of every changed client path once grew past what git grep compiles, and every Go reader of a
        # large apple/ or android/ diff was dropped without a word (#11250).
        self.write("server/internal/contract/contract_test.go",
                   'package contract\n\nvar models = read("apple/Shared/Models.swift")\n')
        self.write("server/internal/kotlin/kotlin_test.go",
                   'package kotlin\n\nvar sources = []string{"x", "android/core/contracts/src/Calls.kt"}\n')
        self.write("server/internal/samples/samples_test.go",
                   'package samples\n\nconst source = "apple/Shared/Models.swift: Fold"\n'
                   '// Not "apple/Persona/Commented.swift" either: a comment line.\n')
        self.git("add", "-A")
        self.git("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "readers")
        listed = [f"apple/Generated/Group{i // 100}/File{i}.swift" for i in range(4000)]
        listed += [f"android/generated/src/main/kotlin/File{i}.kt" for i in range(1000)]
        listed += ["apple/Shared/Models.swift", "android/core/contracts/src/Calls.kt", "apple/Persona/Commented.swift"]
        code, out = self.plan(listed)
        self.assertEqual(0, code, out[-2000:])
        queued = sorted(line.strip() for line in out.splitlines() if line.startswith("  queued: server go test"))
        self.assertEqual(["queued: server go test ./internal/contract/...",
                          "queued: server go test ./internal/kotlin/..."], queued, out[-2000:])

    def test_a_search_that_fails_stops_the_plan(self):
        # A reader a failed search would have found is not run otherwise: check-fast stops and says so (#11250).
        bin_dir = Path(tempfile.mkdtemp(prefix="check-fast-git-"))
        self.addCleanup(shutil.rmtree, bin_dir)
        git = shutil.which("git")
        # A git whose grep fails as one that cannot compile its pattern does; everything else is the real git.
        (bin_dir / "git").write_text('#!/bin/sh\n'
                                     'if [ "$1" = grep ]; then echo "fatal: no grep today" >&2; exit 128; fi\n'
                                     f'exec "{git}" "$@"\n')
        (bin_dir / "git").chmod(0o755)
        env = dict(self.env, PATH=f"{bin_dir}{os.pathsep}{self.env['PATH']}")
        self.write("scripts/tools/helper.py", "")
        self.git("add", "-A")
        self.git("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "helper")
        for path in ("apple/Shared/Models.swift", "docs/example-guide.md", "spec/fixtures/x.json",
                     "scripts/tools/helper.py"):
            with self.subTest(path=path):
                code, out = self.plan([path], env=env)
                self.assertEqual(2, code, out)
                self.assertRegex(out, r"check-fast: searching .* failed \(git grep exit 128\): fatal: no grep today")
                self.assertNotIn("dry run,", out)

    def test_an_sdk_ts_source_runs_the_trusted_adapter_check(self):
        # The trusted Deno adapter bundles sdk/ts/src (#4221): a change there runs denohost's tests, where
        # TestTrustedAdapterMatchesOfficialSDK catches a stale adapter. The rest of sdk/ts is not bundled.
        bun = self.go_log.parent / "bun"  # a fake bun next to the fake go: bun test passes
        bun.write_text("#!/bin/sh\n")
        bun.chmod(0o755)
        self.write("sdk/ts/README.md", "changed\n")
        out, packages = self.check_fast()
        self.assertEqual([], packages, out)
        self.write("sdk/ts/src/wire.ts", "export {}\n")
        out, packages = self.check_fast()
        self.assertEqual(["./internal/extension/denohost/..."], packages, out)

    def test_without_bun_the_trusted_adapter_check_says_it_skipped(self):
        self.env = {**self.env, "PATH": os.pathsep.join(
            d for d in self.env["PATH"].split(os.pathsep) if not (Path(d) / "bun").exists())}
        self.write("sdk/ts/src/wire.ts", "export {}\n")
        out, packages = self.check_fast()
        self.assertEqual([], packages, out)
        self.assertIn("SKIP  trusted Deno adapter", out)

    def test_a_server_go_change_also_runs_the_repo_wide_guards(self):
        # The guards fail on code outside their own package (#1936 broke the lazyre guard on main that way), so a change
        # in any package runs them next to that package's own tests.
        self.write("server/internal/foo/x.go", "package foo\n")
        out, packages = self.check_fast()
        self.assertIn("queued: server repo-wide guards", out)
        self.assertIn("./internal/foo/...", packages)
        calls = self.guard_calls()
        self.assertEqual(2, len(calls), out)
        self.assertEqual("server|test -count=1 " + GUARD_PACKAGES, calls[0])
        match = re.fullmatch(r"server\|test -count=1 -run \^\(([\w|.*]+)\)\$ " + re.escape(GUARD_RUN_PACKAGES), calls[1])
        self.assertIsNotNone(match, calls[1])
        # go test -run passes when a name matches nothing: test_check_fast_guards.py's CheckFastGoGuardListTests holds
        # every entry to a func of these packages in this repository, and every package directory to its tests.

    def test_a_plugin_change_runs_its_own_tests_and_the_guards_not_a_build(self):
        self.write("server/plugins/wechat/x.go", "package wechat\n")
        out, packages = self.check_fast()
        self.assertIn("./plugins/wechat/...", packages)
        self.assertEqual(2, len(self.guard_calls()), out)
        self.assertNotIn("server|build ./...", self.go_calls(), out)

    def test_an_sdk_go_change_runs_the_guards(self):
        # The plugins guard reads sdk/go: a plugin uses only the SDK, and the SDK stands alone.
        self.write("sdk/go/x.go", "package sdk\n")
        out, _ = self.check_fast()
        self.assertEqual(2, len(self.guard_calls()), out)

    def test_a_server_go_change_runs_go_vet_and_gofmt_in_jobs_of_their_own(self):
        # go vet checks the whole module, then the changed groups for Windows; gofmt reads just the changed .go files
        # (#12894). Each is its own job, beside the package tests and the guards.
        self.write("server/internal/foo/x.go", "package foo\n")
        self.write("server/cmd/bar/main.go", "package main\n")
        self.write("server/internal/foo/testdata/case.json", "{}\n")
        out, packages = self.check_fast()
        for line in ("PASS     server repo-wide guards", "PASS     server go vet", "PASS     gofmt (changed files)"):
            self.assertIn(line, out)
        self.assertLessEqual({"./cmd/bar/...", "./internal/foo/..."}, set(packages))
        self.assertEqual(["server|vet -ldflags= ./...",
                          "server|GOOS=windows vet -ldflags= ./cmd/bar/... ./internal/foo/..."], self.vet_calls())
        self.assertEqual(["|-l server/cmd/bar/main.go server/internal/foo/x.go"], self.gofmt_calls())

    def test_a_change_outside_every_group_vets_the_whole_module_for_windows(self):
        for path, text in (("server/go.mod", "module example.com/server\n\ngo 1.27\n"), ("sdk/go/x.go", "package sdk\n"),
                           ("server/plugins/plugins.go", "package plugins\n")):
            with self.subTest(path=path):
                self.go_log.unlink(missing_ok=True)
                self.write(path, text)
                out, _ = self.check_fast()
                self.git("checkout", "-q", "base", "--", ".")
                self.git("clean", "-fdq")
                self.assertEqual(["server|vet -ldflags= ./...", "server|GOOS=windows vet -ldflags= ./..."],
                                 self.vet_calls(), out)

    def test_a_group_with_no_windows_package_is_left_out_of_the_windows_vet(self):
        # Every file of server/scripts/fleetsoak is //go:build unix and server/cmd/password-wasm is js && wasm: given
        # only such groups, GOOS=windows go vet says "no packages to vet" and exits 1, where CI's ./... passes over them.
        # This go says so too, and its go list answers as the real one does for such a group. A group is left out only
        # on that answer: a go list that fails, or lists nothing without the warning, leaves the group in.
        (self.go_log.parent / "go").write_text(
            FAKE_GO + 'case "$GOOS $*" in\n'
            '  "windows list ./internal/unixonly/...") echo \'go: warning: "./internal/unixonly/..." matched no packages\' >&2'
            ' ;;\n'
            '  "windows list ./internal/broken/...") echo "internal/broken/x.go:1:1: expected package" >&2; exit 1 ;;\n'
            '  "windows vet "*./internal/unixonly/...*) echo "no packages to vet" >&2; exit 1 ;;\n'
            'esac\n')
        left_out = "windows vet left out ./internal/unixonly/...: no package there builds for windows"
        for path in ("server/internal/unixonly/soak.go", "server/internal/foo/x.go", "server/internal/broken/x.go"):
            self.write(path, "//go:build unix\n\npackage x\n")
        out, _ = self.check_fast()
        self.assertIn("PASS     server go vet (", out)
        self.assertIn("; " + left_out + ")", out)
        self.assertEqual(["server|vet -ldflags= ./...",
                          "server|GOOS=windows vet -ldflags= ./internal/broken/... ./internal/foo/..."], self.vet_calls())
        # With no group left, the Windows step does not run, and the job still passes.
        self.git("checkout", "-q", "base", "--", ".")
        self.git("clean", "-fdq")
        self.go_log.unlink()
        self.write("server/internal/unixonly/soak.go", "//go:build unix\n\npackage soak\n")
        out, _ = self.check_fast()
        self.assertIn("PASS     server go vet (", out)
        self.assertIn(left_out, out)
        self.assertEqual(["server|vet -ldflags= ./..."], self.vet_calls())

    def test_a_moved_tool_runs_tests_and_windows_vet_at_its_new_path(self):
        (self.repo / "server/scripts").mkdir()
        self.git("mv", "server/internal/bar", "server/scripts/bar")
        out, packages = self.check_fast()
        self.assertIn("./scripts/bar/...", packages, out)
        self.assertNotIn("./internal/bar/...", packages, out)
        self.assertNotIn("server|build ./...", self.go_calls(), out)
        self.assertIn("PASS     server go vet (", out)
        self.assertIn("; windows vet left out ./internal/bar/...: no package there builds for windows)", out)
        self.assertEqual(["server|vet -ldflags= ./...", "server|GOOS=windows vet -ldflags= ./scripts/bar/..."],
                         self.vet_calls())
        self.assertNotIn("server|GOOS=windows list ./internal/bar/...", self.go_calls())

    def test_an_unformatted_file_fails_the_gofmt_job_alone(self):
        self.write("server/internal/foo/x.go", "package foo\n\n// " + UNFORMATTED + "\n")
        self.write("server/internal/foo/y.go", "package foo\n")
        out, _ = self.check_fast(code=1)
        self.assertIn("FAIL     gofmt (changed files)", out)
        self.assertIn("gofmt: needs formatting (run gofmt -w): server/internal/foo/x.go", out)
        self.assertIn("rerun: python3 -B scripts/ci/gofmt.py server/internal/foo/x.go server/internal/foo/y.go", out)
        for line in ("PASS     server go test ./internal/foo/...", "PASS     server repo-wide guards",
                     "PASS     server go vet"):
            self.assertIn(line, out)

    def test_a_vet_error_fails_the_vet_job_alone_and_both_steps_run(self):
        # Each vet step fails here: the second still runs, so one run names both, each with its rerun line.
        (self.go_log.parent / "go").write_text(
            FAKE_GO + 'case "$*" in vet*) echo "vet: x.go:3:2: undefined: syscall.Kill"; exit 1 ;; esac\n')
        self.write("server/internal/foo/x.go", "package foo\n")
        out, _ = self.check_fast(code=1)
        self.assertIn("FAIL     server go vet", out)
        self.assertEqual(2, len(self.vet_calls()), out)
        self.assertIn("rerun: (cd server && go vet -ldflags= ./...)", out)
        self.assertIn("rerun: (cd server && env GOOS=windows GOARCH=amd64 go vet -ldflags= ./internal/foo/...)", out)
        for line in ("PASS     server go test ./internal/foo/...", "PASS     server repo-wide guards",
                     "PASS     gofmt (changed files)"):
            self.assertIn(line, out)

    def test_a_vet_past_its_timeout_cannot_hide_a_guard_failure(self):
        # The reason vet is a job of its own: a vet on a cold build cache can run past the go timeout, and a step that
        # times out makes its whole job a TIMEOUT. In the guard job that would stand in for a contract test's FAIL.
        (self.go_log.parent / "go").write_text(
            FAKE_GO + 'case "$*" in\n  vet*) exec sleep 30 ;;\n'
            '  "test -count=1 -timeout "*" -run "*) echo "--- FAIL: TestWireSamples (0.00s)"; echo FAIL; exit 1 ;;\n'
            'esac\n')
        self.env["CHECK_FAST_TIMEOUT_GO_TEST"] = "3"
        self.write("server/internal/foo/x.go", "package foo\n")
        out, _ = self.check_fast(code=1)  # a FAIL wins over a TIMEOUT in the exit code too
        self.assertIn("TIMEOUT  server go vet", out)
        self.assertIn("FAIL     server repo-wide guards", out)

    def test_all_vets_and_formats_the_whole_trees_as_ci_does(self):
        # --all vets the whole module on both platforms and runs gofmt over server/ and sdk/go/, as scripts/ci/server.sh
        # does. Other checks of --all have nothing to run on in this tree; only these calls matter here.
        subprocess.run(["bash", "scripts/test/check-fast.sh", "--all"], cwd=self.repo, env=self.env,
                       capture_output=True, text=True, timeout=300)
        self.assertEqual(["server|vet -ldflags= ./...", "server|GOOS=windows vet -ldflags= ./..."], self.vet_calls())
        calls = self.gofmt_calls()
        self.assertEqual(1, len(calls), calls)
        self.assertIn(" server/internal/foo/foo_test.go", calls[0])

    def test_a_deleted_go_file_is_not_handed_to_gofmt(self):
        self.write("server/internal/foo/old.go", "package foo\n")
        self.git("add", "-A")
        self.git("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "old")
        self.git("tag", "-f", "base")
        (self.repo / "server/internal/foo/old.go").unlink()
        out, _ = self.check_fast()
        self.assertIn("SKIP     gofmt (changed files)", out)
        self.assertIn("every changed .go file is deleted", out)
        self.assertEqual([], self.gofmt_calls())
        self.write("server/internal/foo/x.go", "package foo\n")
        out, _ = self.check_fast()
        self.assertEqual(["|-l server/internal/foo/x.go"], self.gofmt_calls())

    def test_a_docs_or_web_change_runs_no_guard(self):
        self.write("docs/example-guide.md", "guide changed\n")  # a server test reads it: that package still runs
        self.write("web/src/x.ts", "export {}\n")
        out, packages = self.check_fast()
        self.assertEqual(["./internal/guide/conformance/..."], packages, out)
        self.assertEqual([], self.guard_calls(), out)
        self.assertNotIn("repo-wide guards", out)
        self.assertEqual(([], []), (self.vet_calls(), self.gofmt_calls()), out)
        self.assertNotIn("go vet", out)

    def test_the_assistant_group_runs_in_its_partitions_and_node_runs_as_given(self):
        # Every Go group goes through scripts/test/go_test_partitioned.py (#12765). ./internal/assistant/... covers a
        # partitioned package: its tests run in go_partitions.py's partitions beside its subpackages, each with
        # the group's flags and timeout. internal/node splits itself in its TestMain, so its group runs as given.
        (self.go_log.parent / "go").write_text(
            FAKE_GO + 'case "$*" in\n'
            '  "list ./internal/assistant/...") printf "example.com/server/internal/assistant\\n'
            'example.com/server/internal/assistant/inbox\\n" ;;\n'
            '  "test -list .* ./internal/assistant") printf "TestA\\nTestB\\nTestC\\nTestD\\n'
            'ok  \\texample.com/server/internal/assistant\\t0.1s\\n" ;;\n'
            'esac\n')
        self.env.pop("GOFLAGS", None)
        self.env["CHECK_FAST_TIMEOUT_GO_TEST"] = "300"
        self.write("server/internal/assistant/x.go", "package assistant\n")
        self.write("server/internal/node/x.go", "package node\n")
        out, _ = self.check_fast()
        self.assertIn("PASS     server go test ./internal/assistant/...", out)
        self.assertIn("PASS     server go test ./internal/node/...", out)
        # The vet job's go list of each group for Windows is its own; only the go test calls are counted here.
        calls = [call for call in self.go_calls() if call not in self.guard_calls() + self.vet_calls()
                 and "|build " not in call and "|GOOS=windows list " not in call]
        spec = importlib.util.spec_from_file_location("go_partitions", ROOT / "scripts/test/go_partitions.py")
        go_partitions = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(go_partitions)
        runs = ["server|test -count=1 -run %s ./internal/assistant" % pattern for pattern in
                go_partitions.patterns(["TestA", "TestB", "TestC", "TestD"], go_partitions.PACKAGES["./internal/assistant"])]
        self.assertEqual(sorted([
            "server|list ./internal/assistant/...",
            "server|test -list .* ./internal/assistant",
            "server|test -count=1 example.com/server/internal/assistant/inbox",
            *runs,
            "server|test -count=1 ./internal/node/...",
        ]), sorted(calls), out)
        timeouts = {re.search(r" -timeout (\S+)", call).group(1)
                    for call in self.go_log.read_text().splitlines() if call.startswith("server|test -count=1 ")}
        self.assertEqual({"290s"}, timeouts)  # inside CHECK_FAST_TIMEOUT_GO_TEST's 300 s, as before

    def test_go_runs_with_trimpath_and_the_callers_flags(self):
        # Worktrees share compiled packages only under -trimpath (#10614): check-fast adds it with
        # scripts/test/go_trimpath.sh, which keeps a user-set GOFLAGS.
        shutil.copy(ROOT / "scripts/test/go_trimpath.sh", self.repo / "scripts/test/go_trimpath.sh")
        self.git("add", "-A")
        self.git("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "helper")
        self.git("tag", "-f", "base")
        # This go answers go env GOFLAGS as the real one does and records the GOFLAGS of every other call.
        (self.go_log.parent / "go").write_text(
            '#!/bin/sh\n[ "$*" = "env GOFLAGS" ] && { printf "%s\\n" "$GOFLAGS"; exit 0; }\n'
            'printf "%s|%s|%s\\n" "${PWD##*/}" "$GOFLAGS" "$*" >> "$FAKE_GO_LOG"\n')
        self.env["GOFLAGS"] = "-mod=readonly"
        self.write("server/internal/foo/x.go", "package foo\n")
        self.check_fast()
        calls = [line.split("|", 2) for line in self.go_log.read_text().splitlines()]
        tests = [call for call in calls if call[0] == "server" and call[2].startswith("test ")]
        self.assertEqual(3, len(tests), calls)  # the package and the two guard calls
        vets = [call for call in calls if call[0] == "server" and call[2].startswith("vet ")]
        self.assertEqual(2, len(vets), calls)  # the module and the changed group for Windows
        for _, goflags, _ in tests + vets:
            self.assertEqual("-mod=readonly -trimpath", goflags)

    @unittest.skipIf(fcntl is None, "heavy build slots need fcntl (not on Windows)")
    def test_go_and_vitest_run_in_a_heavy_slot_outside_their_timeout(self):
        # Parallel worktrees queue for go and vitest instead of thrashing (#10714): check-fast runs them through
        # scripts/heavy_slot.py, around the GNU timeout so a wait in the queue is not counted as a hung step. Each
        # call records the slot marker (kind:pid of the helper holding the slot) and its parent's parent: the
        # timeout's parent, which must be that helper.
        shutil.copy(ROOT / "scripts/heavy_slot.py", self.repo / "scripts/heavy_slot.py")
        self.git("add", "-A")
        self.git("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "helper")
        self.git("tag", "-f", "base")
        record = ('#!/bin/sh\nprintf "%s|%s|%s|%s\\n" "${PWD##*/}" "${PERSONA_HEAVY_SLOT_HELD:-}" '
                  '"$(ps -o ppid= -p "$PPID" | tr -d " ")" "$*" >> "$FAKE_GO_LOG"\n')
        for tool in ("go", "npm"):
            (self.go_log.parent / tool).write_text(record)
        slots = Path(tempfile.mkdtemp(prefix="check-fast-slots-"))
        self.addCleanup(shutil.rmtree, slots)
        self.env = {key: value for key, value in self.env.items() if not key.startswith("PERSONA_HEAVY_SLOT")}
        self.env["PERSONA_HEAVY_SLOT_DIR"] = str(slots)
        self.write("server/internal/foo/x.go", "package foo\n")
        self.write("web/src/x.ts", "export {}\n")
        self.check_fast()
        # go vet compiles, so both of its steps take a go slot; gofmt only parses the changed files and takes none.
        vets = [line.split("|", 3) for line in self.go_log.read_text().splitlines() if line.split("|", 3)[3][:4] == "vet "]
        self.assertEqual(2, len(vets), vets)
        for _, marker, grandparent, args in vets:
            self.assertRegex(marker, r"^go:\d+$", args)
            self.assertEqual(marker.split(":")[1], grandparent, args)
        self.assertEqual(["|-l server/internal/foo/x.go"], self.gofmt_calls())
        calls = [line.split("|", 3) for line in self.go_log.read_text().splitlines()]
        tests = [call for call in calls if call[3].split()[0] == "test"]
        # The touched package's go test, the two guard calls and vitest, each checked on its own: the package's call
        # goes through scripts/test/go_test_partitioned.py, which must exec go for it to sit right under the slot
        # (the guards call go directly, so a set of (dir, subcommand) pairs would pass without that exec).
        self.assertEqual(["server", "server", "server", "web"], sorted(call[0] for call in tests), calls)
        self.assertEqual(1, sum(1 for where, _, _, args in tests if args.endswith(" ./internal/foo/...")), calls)
        for where, marker, grandparent, args in tests:
            self.assertRegex(marker, r"^(go|web):\d+$", (where, args))
            self.assertEqual(marker.split(":")[1], grandparent, (where, args))
        # npm ci, the node_modules check and the typecheck take no slot.
        self.assertEqual({"ci", "run check:deps", "run typecheck"},
                         {args for where, marker, _, args in calls if where == "web" and not marker}, calls)
        self.assertTrue((slots / "go.0.lock").exists() and (slots / "web.0.lock").exists())

    def test_with_the_slots_off_go_and_vitest_run_directly(self):
        # PERSONA_HEAVY_SLOTS=0: the helper would only exec the command, so check-fast does not start it at all.
        shutil.copy(ROOT / "scripts/heavy_slot.py", self.repo / "scripts/heavy_slot.py")
        self.git("add", "-A")
        self.git("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "helper")
        self.git("tag", "-f", "base")
        record = '#!/bin/sh\nprintf "%s|%s|%s\\n" "${PWD##*/}" "${PERSONA_HEAVY_SLOT_HELD:-}" "$*" >> "$FAKE_GO_LOG"\n'
        for tool in ("go", "npm"):
            (self.go_log.parent / tool).write_text(record)
        # A python3 that logs its arguments before it runs: the helper's own exec leaves no other trace.
        python_log = self.go_log.parent / "python.log"
        (self.go_log.parent / "python3").write_text(
            f'#!/bin/sh\nprintf "%s\\n" "$*" >> "{python_log}"\nexec "{sys.executable}" "$@"\n')
        (self.go_log.parent / "python3").chmod(0o755)
        self.env = {key: value for key, value in self.env.items() if not key.startswith("PERSONA_HEAVY_SLOT")}
        self.env.update(PERSONA_HEAVY_SLOTS="0")
        self.write("server/internal/foo/x.go", "package foo\n")
        self.write("web/src/x.ts", "export {}\n")
        self.check_fast()
        calls = [line.split("|", 2) for line in self.go_log.read_text().splitlines()]
        self.assertEqual({("server", "test"), ("web", "test")},
                         {(where, args.split()[0]) for where, _, args in calls if args.split()[0] == "test"}, calls)
        self.assertEqual([], [call for call in calls if call[1]], calls)  # no slot marker anywhere
        python = python_log.read_text().splitlines()
        self.assertTrue(python)  # the privacy scan, at least
        self.assertEqual([], [call for call in python if "heavy_slot" in call])

    @unittest.skipIf(fcntl is None, "heavy build slots need fcntl (not on Windows)")
    def test_a_wait_for_a_slot_past_its_timeout_is_a_timeout_that_says_so(self):
        # The only go slot is taken (by this test) and no wait is allowed: each go check is a TIMEOUT whose detail says
        # it never ran, so nobody reads it as a hung test.
        shutil.copy(ROOT / "scripts/heavy_slot.py", self.repo / "scripts/heavy_slot.py")
        self.git("add", "-A")
        self.git("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "helper")
        self.git("tag", "-f", "base")
        slots = Path(tempfile.mkdtemp(prefix="check-fast-slots-"))
        self.addCleanup(shutil.rmtree, slots)
        held = os.open(slots / "go.0.lock", os.O_RDWR | os.O_CREAT, 0o600)
        self.addCleanup(os.close, held)
        fcntl.flock(held, fcntl.LOCK_EX | fcntl.LOCK_NB)
        env = {key: value for key, value in self.env.items() if not key.startswith("PERSONA_HEAVY_SLOT")}
        env.update(PERSONA_HEAVY_SLOT_DIR=str(slots), PERSONA_HEAVY_SLOTS_GO="1", PERSONA_HEAVY_SLOT_TIMEOUT="0")
        self.write("server/internal/foo/x.go", "package foo\n")
        run = subprocess.run(["bash", "scripts/test/check-fast.sh", "--base", "base"], cwd=self.repo, env=env,
                             capture_output=True, text=True, timeout=300)
        out = run.stdout + run.stderr
        self.assertEqual(124, run.returncode, out)
        self.assertIn("go test ./internal/foo/... (no go slot came free; it did not run)", out)
        self.assertFalse(any(call.startswith("server|test") for call in self.go_calls()), self.go_calls())

    @unittest.skipIf(fcntl is None, "heavy build slots need fcntl (not on Windows)")
    def test_a_wait_for_a_slot_shows_live_and_counts_apart(self):
        # A step can wait up to 45 minutes for a slot: the helper's line saying so must reach stderr while the run
        # waits, not when the step ends, and the summary must not count the queue as the check's own time.
        shutil.copy(ROOT / "scripts/heavy_slot.py", self.repo / "scripts/heavy_slot.py")
        self.git("add", "-A")
        self.git("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "helper")
        self.git("tag", "-f", "base")
        slots = Path(tempfile.mkdtemp(prefix="check-fast-slots-"))
        self.addCleanup(shutil.rmtree, slots)
        held = os.open(slots / "go.0.lock", os.O_RDWR | os.O_CREAT, 0o600)
        self.addCleanup(os.close, held)
        fcntl.flock(held, fcntl.LOCK_EX | fcntl.LOCK_NB)
        env = {key: value for key, value in self.env.items() if not key.startswith("PERSONA_HEAVY_SLOT")}
        env.update(PERSONA_HEAVY_SLOT_DIR=str(slots), PERSONA_HEAVY_SLOTS_GO="1", PERSONA_HEAVY_SLOT_TIMEOUT="120")
        self.write("server/internal/foo/x.go", "package foo\n")
        # A go whose output starts with a line like the helper's: only the helper's own lines are shown live, whether
        # the step waited (then the command's line comes after the helper's) or not (the guards job's second go call
        # finds the slot its first one just gave back).
        (self.go_log.parent / "go").write_text(FAKE_GO + 'echo "heavy_slot: got a go slot after 9m"\n')
        output = Path(tempfile.mkdtemp(prefix="check-fast-out-"))
        self.addCleanup(shutil.rmtree, output)
        with (output / "out").open("w") as out, (output / "err").open("w") as err:
            proc = subprocess.Popen(["bash", "scripts/test/check-fast.sh", "--base", "base"], cwd=self.repo, env=env,
                                    stdout=out, stderr=err, start_new_session=True)
        self.addCleanup(self.stop_group, proc)
        waiting = "check-fast: go test ./internal/foo/...: waiting for a go slot (1 busy, up to 2m)"
        deadline = time.monotonic() + 60
        while waiting not in (output / "err").read_text():
            self.assertIsNone(proc.poll(), "check-fast ended before it said it waits")
            self.assertLess(time.monotonic(), deadline, (output / "err").read_text())
            time.sleep(0.05)
        self.assertIsNone(proc.poll())  # said while the run still waits
        time.sleep(1.5)  # a wait the helper reports as at least 1s
        fcntl.flock(held, fcntl.LOCK_UN)
        self.assertEqual(0, proc.wait(timeout=120))
        err, out = (output / "err").read_text(), (output / "out").read_text()
        self.assertRegex(err, r"check-fast: go test \./internal/foo/\.\.\.: got a go slot after [1-9]\d*s\n")
        self.assertNotIn("9m", err)
        self.assertRegex(out, r"\nPASS     server go test \./internal/foo/\.\.\. \(\d+s, plus [1-9]\d*s waiting for a go "
                              r"slot\)\n")

    def stop_group(self, proc):
        if proc.poll() is None:
            os.killpg(proc.pid, signal.SIGKILL)
            proc.wait()



def load_paths(root=ROOT):
    """scripts/ci/paths.py under root, loaded fresh: its ROOT, and so its scan, is that tree's."""
    spec = importlib.util.spec_from_file_location(f"ci_paths_{abs(hash(str(root)))}", Path(root) / "scripts/ci/paths.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


# A fake node that reports major version 22 (check-fast.sh's gate) and records the supervised Vitest entry, and fake
# npm/npx commands that record each call ("<cwd basename>|<args>", npx's as "npx <args>") instead of installing/testing.
FAKE_NODE = '#!/bin/sh\nif [ "$1" = -e ]; then echo 22; elif [ "$1" = scripts/process-group.mjs ]; then printf "%s|node %s\\n" "${PWD##*/}" "$*" >> "$FAKE_NPM_LOG"; fi\n'
FAKE_NPM = '#!/bin/sh\nprintf "%s|%s\\n" "${PWD##*/}" "$*" >> "$FAKE_NPM_LOG"\n'
FAKE_NPX = '#!/bin/sh\nprintf "%s|npx %s\\n" "${PWD##*/}" "$*" >> "$FAKE_NPM_LOG"\n'
VITEST_RELATED = "web|node scripts/process-group.mjs --timeout-ms 300000 -- npx --no -- vitest related --run --passWithNoTests "
WEB_FULL = ["web|run check:deps", "web|run typecheck", "web|test"]

# A web tree that reads files outside web/ in each form the scan knows (#10619). reads.test.ts reads through fs and a
# Vite glob, so it always runs; async-reads.test.ts reads only through node:fs/promises, so it always runs too;
# build.test.mjs builds through a Vite config, whose reads no import of the test shows, so it always runs too;
# tiers.ts is a module that reads through fs, so its importers always run; stream.ts and frame-host.ts read through
# Vite query imports, so they join when a file they name changes. The config names a setup file (setupFiles, a list)
# and a global setup (globalSetup, one path): what they reach runs the whole suite (#10715).
WEB_TREE = {
    "web/src/lib/reads.test.ts": (
        'import { readFileSync } from "node:fs";\n'
        'import { join } from "node:path";\n'
        'import cases from "../../../spec/fixtures/forward/cases.json";\n'
        'const now = readFileSync(join(__dirname, "../../../spec/fixtures/inbox", "now.json"), "utf8");\n'
        'const events = readFileSync(new URL("../../../scripts/mock/inbox_events.json", import.meta.url), "utf8");\n'
        'const views = import.meta.glob("../../../schema/extension/examples/assistant/*.json", { eager: true });\n'
        'const view = (name: string) => readFileSync(new URL(`../../../schema/extension/examples/View.v8-${name}.json`, import.meta.url));\n'
        'const joined = join(__dirname, "..", "..", "..", "spec", "fixtures", "joined", "x.json");\n'
        '// Comments name spec/fixtures/other without reading it.\n'),
    "web/src/lib/async-reads.test.ts": ('import { readFile } from "node:fs/promises";\n'
                                        'const css = await readFile(new URL("../styles.css", import.meta.url), "utf8");\n'),
    "web/scripts/build.test.mjs": ('import { build } from "vite";\n'
                                   'await build({ configFile: new URL("../vite.config.ts", import.meta.url).pathname });\n'),
    "web/src/lib/tiers.ts": ('import { readdirSync } from "node:fs";\n'
                             'export const root = new URL("../../../spec/fixtures/tiers/", import.meta.url);\n'),
    "web/src/lib/stream.ts": 'import schema from "../../../schema/extension/stream.schema.json?raw";\nexport default schema;\n',
    "web/src/lib/frame-host.ts": 'import FRAME from "./frame.js?raw";\nexport default FRAME;\n',
    "web/src/lib/frame.js": "parent.postMessage('ready');\n",
    "web/src/views/View.tsx": "export const View = () => null;\n",
    "web/src/views/View.test.tsx": 'import { View } from "./View";\n',
    "web/src/lib/store.ts": "export const store = {};\n",
    "web/src/i18n/index.ts": "export const t = (key: string) => key;\n",
    "web/src/startup.ts": "export const startup = 1;\n",
    "web/vite.config.ts": ('import { startup } from "./src/startup";\n'
                           'export default { startup, test: { globalSetup: "scripts/global.mjs",'
                           ' setupFiles: ["src/setup/test-setup.ts"] } };\n'),
    "web/scripts/global.mjs": 'import { env } from "../src/setup/global-env.ts";\nexport default () => env;\n',
    "web/src/setup/global-env.ts": "export const env = {};\n",
    "web/src/setup/test-setup.ts": 'import { catalog } from "./catalog";\nimport { t } from "../i18n";\n',
    "web/src/setup/catalog.ts": ('import { names } from "./names.js";\n'
                                 'export const catalog = import.meta.glob("../locales/*.json");\n'),
    "web/src/setup/names.ts": "export const names = [];\n",
    "web/src/setup/other.ts": "export const other = 1;\n",
    "web/src/locales/en.json": "{}\n",
    "spec/fixtures/forward/cases.json": "{}\n",
    "spec/fixtures/inbox/now.json": "{}\n",
    "spec/fixtures/tiers/a.json": "{}\n",
    "spec/fixtures/other/x.json": "{}\n",
    "scripts/mock/inbox_events.json": "{}\n",
    "schema/extension/examples/assistant/a.json": "{}\n",
    "schema/extension/stream.schema.json": '{"$ref": "view.schema.json#/$defs/View"}\n',
    "schema/extension/view.schema.json": '{"$ref": "#/$defs/View"}\n',
    "schema/extension/connector.schema.json": "{}\n",
}
# What paths.py derives from WEB_TREE: every path named relative to the file that leads out of web/; a directory, a
# glob or a ${…} part as everything under its literal part; a JSON Schema with the files its $refs name.
WEB_TREE_INPUTS = [
    "schema/extension/examples/View.v8-*", "schema/extension/examples/assistant/*",
    "schema/extension/stream.schema.json", "schema/extension/view.schema.json",
    "scripts/mock/inbox_events.json",
    "spec/fixtures/forward/cases.json", "spec/fixtures/inbox/**", "spec/fixtures/joined/x.json",
    "spec/fixtures/tiers/**",
]
# The files every related run starts from on top of the changed ones, relative to web/.
WEB_TREE_READERS = [
    "scripts/build.test.mjs", "src/lib/async-reads.test.ts", "src/lib/reads.test.ts", "src/lib/tiers.ts",
]


@unittest.skipUnless(shutil.which("timeout") or shutil.which("gtimeout"), "check-fast.sh needs GNU timeout")
class CheckFastWebInputsTests(unittest.TestCase):
    """The web check runs on the files web/src and its tests read, and picks its tests by what changed (#10619)."""

    def setUp(self):
        self.repo = Path(tempfile.mkdtemp(prefix="check-fast-test-"))
        self.addCleanup(shutil.rmtree, self.repo)
        (self.repo / "scripts/test").mkdir(parents=True)
        (self.repo / "scripts/ci").mkdir(parents=True)
        copy_check_fast(self.repo)
        shutil.copy(ROOT / "scripts/ci/paths.py", self.repo / "scripts/ci/paths.py")
        self.write("scripts/privacy_check.py", "print('clean')\n")
        self.write("scripts/test/test_extension_schema.py", "")
        self.write("scripts/test/test_sdk_bundle.py", "")
        self.write("scripts/test/pr_needs_issue_test.mjs", "")
        for name in GUARD_SUITES:
            self.write(f"scripts/test/{name}", PASSING)
        self.write("sdk/go/go.mod", "module example.com/sdk\n")
        self.write("web/package.json", "{}\n")
        self.write("schema/other.json", "{}\n")
        self.write("docs/guide.md", "guide\n")
        for path, text in WEB_TREE.items():
            self.write(path, text)
        self.git("init", "-q")
        self.git("add", "-A")
        self.git("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "base")
        self.git("tag", "base")
        # An installed (untracked, ignored) node_modules, so the web check skips npm ci as on a set-up checkout.
        (self.repo / "web/node_modules").mkdir()
        self.write(".git/info/exclude", "web/node_modules/\n")
        bin_dir = Path(tempfile.mkdtemp(prefix="check-fast-bin-"))
        self.addCleanup(shutil.rmtree, bin_dir)
        for name, text in (("node", FAKE_NODE), ("npm", FAKE_NPM), ("npx", FAKE_NPX), ("go", FAKE_GO)):
            (bin_dir / name).write_text(text)
            (bin_dir / name).chmod(0o755)
        self.npm_log = bin_dir / "npm.log"
        self.env = {**os.environ, "PATH": f"{bin_dir}{os.pathsep}{os.environ['PATH']}",
                    "FAKE_NPM_LOG": str(self.npm_log), "FAKE_GO_LOG": str(bin_dir / "go.log")}

    def write(self, path, text):
        file = self.repo / path
        file.parent.mkdir(parents=True, exist_ok=True)
        file.write_text(text)

    def git(self, *args):
        subprocess.run(["git", "-C", str(self.repo), *args], check=True, capture_output=True)

    def restore(self):
        """Back to the committed tree: tracked files reset, new ones removed (the ignored node_modules stays)."""
        self.git("reset", "-q", "--hard")
        self.git("clean", "-fdq")

    def check_fast(self, *args, code=0):
        self.npm_log.unlink(missing_ok=True)
        run = subprocess.run(["bash", "scripts/test/check-fast.sh", *(args or ("--base", "base"))], cwd=self.repo,
                             env=self.env, capture_output=True, text=True, timeout=300)
        out = run.stdout + run.stderr
        if code is not None:
            self.assertEqual(code, run.returncode, out)
        return out, self.npm_log.read_text().splitlines() if self.npm_log.exists() else []

    def related(self, changed, *readers):
        """The npm and npx calls of a related run from a changed file (relative to web/), then WEB_TREE_READERS and
        the other readers given, in path order."""
        return ["web|run check:deps", "web|run typecheck",
                VITEST_RELATED + " ".join([changed, *sorted({*WEB_TREE_READERS, *readers})])]

    def test_the_inputs_are_derived_from_what_web_reads(self):
        paths = load_paths(self.repo)
        self.assertEqual(WEB_TREE_INPUTS, paths.web_inputs())
        # CI's web lane runs on the same inputs, and on nothing else outside web/ but its own runner files.
        self.assertEqual(["web/**", *WEB_TREE_INPUTS, "scripts/ci/web.sh", ".github/workflows/web-test.yml"],
                         paths.FILTERS["web"])
        run = subprocess.run([sys.executable, "scripts/ci/paths.py", "--web-inputs"], cwd=self.repo,
                             capture_output=True, text=True, timeout=60, check=True)
        self.assertEqual(WEB_TREE_INPUTS, run.stdout.splitlines())

    def test_every_derived_input_runs_the_related_web_tests(self):
        paths = load_paths(self.repo)
        for pattern in WEB_TREE_INPUTS:
            with self.subTest(pattern=pattern):
                sample = pattern.replace("**", "sample.json").replace("*", "sample")
                self.assertTrue(fnmatch.fnmatchcase(sample, pattern), sample)
                self.assertTrue(paths.select([sample])["web"], sample)
                self.write(sample, '{"changed": true}\n')
                try:
                    out, npm_calls = self.check_fast()
                finally:
                    self.restore()
                self.assertIn("queued: web\n", out)
                self.assertIn("check-fast: web tests: vitest related, 1 changed file(s)", out)
                expected = self.related(f"../{sample}")
                if pattern.endswith(".schema.json"):
                    expected = self.related(f"../{sample}", "src/lib/stream.ts")  # stream.ts reads it through ?raw
                self.assertEqual(expected, npm_calls, out)
                self.assertNotIn("no fast check maps to", out)

    def test_a_view_change_runs_its_related_tests_and_the_readers(self):
        self.write("web/src/views/View.tsx", "export const View = () => 'changed';\n")
        out, npm_calls = self.check_fast()
        self.assertIn("check-fast: web tests: vitest related, 1 changed file(s) and 4 that read files from disk", out)
        self.assertEqual(self.related("src/views/View.tsx"), npm_calls, out)
        self.assertIn("PASS     web", out)

    def test_a_file_a_module_reads_through_vite_brings_that_module(self):
        # vitest related never follows frame-host.ts's ?raw import to frame.js, so the plan names frame-host.ts itself.
        self.write("web/src/lib/frame.js", "parent.postMessage('changed');\n")
        _, npm_calls = self.check_fast()
        self.assertEqual(self.related("src/lib/frame.js", "src/lib/frame-host.ts"), npm_calls)

    def test_shared_entry_points_and_configs_run_the_whole_suite(self):
        for path, reason in (("web/src/lib/store.ts", "web/src/lib/store.ts is a shared entry point"),
                             ("web/src/i18n/index.ts", "web/src/i18n/index.ts is a shared entry point"),
                             ("web/vite.config.ts", "web/vite.config.ts is outside web/src"),
                             ("web/src/startup.ts", "web/src/startup.ts is part of the Vite config"),
                             ("web/tsconfig.json", "web/tsconfig.json is outside web/src")):
            with self.subTest(path=path):
                self.write(path, "// changed\n")
                try:
                    out, npm_calls = self.check_fast()
                finally:
                    self.restore()
                self.assertIn(f"check-fast: web tests: the whole suite, {reason}", out)
                self.assertEqual(WEB_FULL, npm_calls, out)
        # A changed lock file also reinstalls node_modules first.
        self.write("web/package-lock.json", "{}\n")
        out, npm_calls = self.check_fast()
        self.assertIn("check-fast: web tests: the whole suite, web/package-lock.json is outside web/src", out)
        self.assertEqual(["web|ci", *WEB_FULL], npm_calls, out)

    def test_what_the_vitest_setup_files_reach_runs_the_whole_suite(self):
        # Every test runs the setup files first, but vitest related follows only a test's own imports (#10715).
        for path in ("web/src/setup/test-setup.ts",  # the setup file
                     "web/src/setup/catalog.ts",     # a module it imports
                     "web/src/setup/names.ts",       # a module that one imports, as ./names.js
                     "web/src/locales/en.json",      # a file that one reads through a Vite glob
                     "web/src/setup/global-env.ts"):  # a module the global setup imports
            with self.subTest(path=path):
                self.write(path, "// changed\n")
                try:
                    out, npm_calls = self.check_fast()
                finally:
                    self.restore()
                self.assertIn(f"check-fast: web tests: the whole suite, {path} is part of the vitest setup", out)
                self.assertEqual(WEB_FULL, npm_calls, out)
        # A module beside them that no setup file reaches runs its related tests.
        self.write("web/src/setup/other.ts", "export const other = 2;\n")
        _, npm_calls = self.check_fast()
        self.assertEqual(self.related("src/setup/other.ts"), npm_calls)

    def test_all_runs_the_whole_web_suite(self):
        self.write("web/src/views/View.tsx", "export const View = () => 'changed';\n")
        # --all also runs checks this tree has no files for; only the web check's calls matter here.
        out, npm_calls = self.check_fast("--all", code=None)
        self.assertIn("queued: web\n", out)
        self.assertEqual(WEB_FULL, npm_calls, out)

    def test_without_the_plan_a_web_change_runs_the_whole_suite(self):
        self.write("scripts/ci/paths.py", "raise SystemExit('broken on purpose')\n")
        self.write("web/src/views/View.tsx", "export const View = () => 'changed';\n")
        out, npm_calls = self.check_fast()
        self.assertIn("INFO  web plan: python3 scripts/ci/paths.py --web-plan failed (broken on purpose)", out)
        self.assertEqual(WEB_FULL, npm_calls, out)

    def test_web_inputs_keep_their_other_checks(self):
        self.write("schema/extension/examples/assistant/a.json", '{"changed": true}\n')
        out, _ = self.check_fast()
        self.assertIn("queued: web\n", out)
        self.assertIn("queued: extension schema", out)  # the schema/ mapping still applies
        self.restore()
        self.write("scripts/mock/inbox_events.json", '{"changed": true}\n')
        out, _ = self.check_fast()
        self.assertIn("queued: web\n", out)
        self.assertIn("queued: pr-needs-issue workflow test", out)  # the scripts/ mapping still applies

    def test_files_web_does_not_read_do_not_run_web(self):
        # Another schema, a fixture only a comment names, a schema no named schema's $refs reach, a doc.
        for path in ("schema/other.json", "spec/fixtures/other/x.json", "schema/extension/connector.schema.json",
                     "docs/guide.md"):
            with self.subTest(path=path):
                self.write(path, '{"changed": true}\n')
                try:
                    out, npm_calls = self.check_fast()
                finally:
                    self.restore()
                self.assertNotIn("queued: web\n", out)
                self.assertEqual([], npm_calls, out)

    def test_rename_out_of_web_still_queues_web(self):
        self.write("web/src/old.ts", "export const old = 1;\n")
        self.write("web/src/consumer.ts", "import { old } from './old';\nconsole.log(old);\n")
        self.git("add", "web/src/old.ts", "web/src/consumer.ts")
        self.git("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "web source base")
        self.git("tag", "-f", "base")
        self.git("config", "diff.renames", "true")
        self.git("mv", "web/src/old.ts", "docs/old.ts")

        out, npm_calls = self.check_fast()
        self.assertIn("queued: web\n", out)
        self.assertEqual(self.related("src/old.ts"), npm_calls, out)  # vitest related starts from the old path


@unittest.skipUnless(shutil.which("timeout") or shutil.which("gtimeout"), "check-fast.sh needs GNU timeout")
class CheckFastPlanTests(unittest.TestCase):
    """--dry-run prints the plan and runs nothing; --changed-from plans listed paths, --each one at a time (#11026).
    The plan also names the CI lanes it runs no part of (scripts/ci/paths.py --lanes-for), and runs the runtime
    packages that read a changed fixture or schema."""

    def setUp(self):
        self.repo = Path(tempfile.mkdtemp(prefix="check-fast-test-"))
        self.addCleanup(shutil.rmtree, self.repo)
        (self.repo / "scripts/test").mkdir(parents=True)
        (self.repo / "scripts/ci").mkdir(parents=True)
        copy_check_fast(self.repo)
        shutil.copy(ROOT / "scripts/ci/paths.py", self.repo / "scripts/ci/paths.py")
        self.write("scripts/privacy_check.py", "print('clean')\n")
        self.write("server/go.mod", "module example.com/server\n")
        self.write("server/internal/foo/foo.go", "package foo\n")
        self.write("android/app/build.gradle.kts", "\n")
        self.write("spec/fixtures/input-clip/cases.json", "[]\n")
        self.write("spec/fixtures/other/cases.json", "[]\n")
        self.write("schema/extension/examples/Manifest.x.json", "{}\n")
        self.write("integrations/clip/package.json", "{}\n")
        self.write("integrations/clip/test/clip.test.mjs",
                   "// spec/fixtures/other/cases.json is named in a comment only\n"
                   "const cases = new URL('../../../spec/fixtures/input-clip/cases.json', import.meta.url)\n")
        self.write("sdk/ts/src/fixtures.ts", 'const dir = new URL("../../../schema/extension/examples/", import.meta.url);\n')
        self.git("init", "-q")
        self.git("add", "-A")
        self.git("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "base")
        self.git("tag", "base")
        bin_dir = Path(tempfile.mkdtemp(prefix="check-fast-bin-"))
        self.addCleanup(shutil.rmtree, bin_dir)
        for name, text in (("node", FAKE_NODE), ("go", FAKE_GO), ("bun", "#!/bin/sh\n")):
            (bin_dir / name).write_text(text)
            (bin_dir / name).chmod(0o755)
        self.go_log = bin_dir / "go.log"
        self.env = {**os.environ, "PATH": f"{bin_dir}{os.pathsep}{os.environ['PATH']}", "FAKE_GO_LOG": str(self.go_log)}

    def write(self, path, text):
        file = self.repo / path
        file.parent.mkdir(parents=True, exist_ok=True)
        file.write_text(text)

    def git(self, *args):
        subprocess.run(["git", "-C", str(self.repo), *args], check=True, capture_output=True)

    def check_fast(self, *args, stdin=None, code=0, cwd=None):
        run = subprocess.run(["bash", str(self.repo / "scripts/test/check-fast.sh"), *args], cwd=cwd or self.repo,
                             env=self.env, input=stdin, capture_output=True, text=True, timeout=300)
        out = run.stdout + run.stderr
        self.assertEqual(code, run.returncode, out)
        return out

    def test_a_dry_run_prints_the_plan_and_runs_nothing(self):
        self.write("server/internal/foo/foo.go", "package foo // changed\n")
        out = self.check_fast("--dry-run", "--base", "base")
        self.assertIn("queued: server go test ./internal/foo/...", out)
        # The privacy scan, the package's tests, the guards, go vet and gofmt (#12894).
        self.assertIn("check-fast: dry run, 5 check(s) planned; nothing ran.", out)
        self.assertNotIn("logs in", out)
        self.assertFalse(self.go_log.exists(), out)

    def test_listed_paths_are_planned_instead_of_the_diff(self):
        self.write("list.txt", "server/internal/foo/foo.go\n\n")
        (self.repo / "sub").mkdir()
        out = self.check_fast("--dry-run", "--changed-from", "../list.txt", cwd=self.repo / "sub")  # from the caller
        self.assertIn("1 listed file(s) from", out)
        self.assertIn("queued: server go test ./internal/foo/...", out)
        out = self.check_fast("--dry-run", "--changed-from", "-", stdin="android/app/build.gradle.kts\n")
        self.assertIn("queued: android i18n", out)
        self.assertNotIn("server go test", out)

    def test_each_listed_path_is_planned_on_its_own(self):
        out = self.check_fast("--dry-run", "--changed-from", "-", "--each",
                              stdin="android/app/build.gradle.kts\nserver/internal/foo/foo.go\n")
        android, server = out.split("== server/internal/foo/foo.go\n")
        self.assertTrue(android.startswith("== android/app/build.gradle.kts\n"), out)
        self.assertIn("queued: android i18n", android)
        self.assertNotIn("server go test", android)
        self.assertIn("queued: server go test ./internal/foo/...", server)
        self.assertNotIn("android i18n", server)

    def test_each_retains_unittest_classification(self):
        # Both paths select the same table suite. Its file is classified once,
        # while each path still gets its own queue or SKIP result.
        self.write("scripts/ci/alpha.py", "VALUE = 1\n")
        self.write("scripts/ci/other.py", "VALUE = 2\n")
        self.write("listed.txt", "scripts/ci/alpha.py\nscripts/ci/other.py\n")
        for text, expected in (("import unittest\n", "queued: scripts/test/test_doc_sections.py"),
                               ("print('not a suite')\n", "SKIP  scripts/test/test_doc_sections.py (not a unittest suite)")):
            with self.subTest(expected=expected):
                self.write("scripts/test/test_doc_sections.py", text)
                counter = self.repo / "grep-count.txt"
                counter.write_text("")
                bin_dir = self.repo / "counting-bin"
                bin_dir.mkdir(exist_ok=True)
                grep = bin_dir / "grep"
                real_grep = shutil.which("grep")
                grep.write_text(f'#!/bin/sh\nprintf "%s\\n" "$*" >> "{counter}"\nexec "{real_grep}" "$@"\n')
                grep.chmod(0o755)
                env = dict(self.env, PATH=f"{bin_dir}{os.pathsep}{self.env['PATH']}")
                run = subprocess.run(["bash", str(self.repo / "scripts/test/check-fast.sh"), "--dry-run",
                                      "--changed-from", "listed.txt", "--each"], cwd=self.repo, env=env,
                                     capture_output=True, text=True, timeout=300)
                self.assertEqual(0, run.returncode, run.stdout + run.stderr)
                out = run.stdout + run.stderr
                alpha, other = out.split("== scripts/ci/other.py\n")
                self.assertIn(expected, alpha)
                self.assertIn(expected, other)
                calls = counter.read_text().splitlines()
                self.assertEqual(1, sum(call == "-q unittest scripts/test/test_doc_sections.py" for call in calls), calls)

    def test_listed_paths_only_plan(self):
        for args in (("--changed-from", "-"), ("--dry-run", "--all", "--changed-from", "-"),
                     ("--dry-run", "--base", "base", "--changed-from", "-"), ("--dry-run", "--each")):
            with self.subTest(args=args):
                self.check_fast(*args, stdin="server/go.mod\n", code=2)
        self.check_fast("--dry-run", "--changed-from", "missing.txt", code=2)
        (self.repo / "scripts/ci/affected_drift.py").unlink()
        self.check_fast("--dry-run", "--changed-from", "-", "--each", stdin="server/go.mod\n", code=2)

    def test_the_ci_lanes_it_runs_no_part_of_are_named(self):
        out = self.check_fast("--dry-run", "--changed-from", "-", stdin="android/app/build.gradle.kts\n")
        self.assertIn("INFO  CI lanes this change selects that check-fast does not run: android (", out)
        out = self.check_fast("--dry-run", "--changed-from", "-", stdin="server/internal/foo/foo.go\n")
        self.assertNotIn("CI lanes this change selects", out)
        # A shared fixture: CI's server, android, apple and runtimes lanes; check-fast runs the runtime package
        # that reads it, so only the lanes it cannot run are named.
        out = self.check_fast("--dry-run", "--changed-from", "-", stdin="spec/fixtures/input-clip/cases.json\n")
        self.assertIn("does not run: server, android, apple (", out)

    def test_each_path_names_the_ci_lanes_it_runs_no_part_of(self):
        out = self.check_fast("--dry-run", "--changed-from", "-", "--each",
                              stdin="android/app/build.gradle.kts\nserver/internal/foo/foo.go\n")
        android, server = out.split("== server/internal/foo/foo.go\n")
        self.assertIn("INFO  CI lanes this change selects that check-fast does not run: android (", android)
        self.assertNotIn("CI lanes this change selects", server)

    def test_a_runtime_package_that_reads_a_fixture_or_schema_runs(self):
        out = self.check_fast("--dry-run", "--changed-from", "-", stdin="spec/fixtures/input-clip/cases.json\n")
        self.assertIn("queued: integrations/clip", out)
        self.assertNotIn("sdk/ts (bun)", out)
        # Named in a comment only: not a reader.
        out = self.check_fast("--dry-run", "--changed-from", "-", stdin="spec/fixtures/other/cases.json\n")
        self.assertNotIn("integrations/clip", out)
        out = self.check_fast("--dry-run", "--changed-from", "-",
                              stdin="schema/extension/examples/Manifest.x.json\n")
        self.assertIn("queued: sdk/ts (bun)", out)
        self.assertNotIn("integrations/clip", out)


class WebFileTests(unittest.TestCase):
    """Which files the scan counts as reading through node's fs (WebFile.fs): they run on every related run."""

    @classmethod
    def setUpClass(cls):
        cls.paths = load_paths()

    def test_each_fs_module_counts(self):
        # fs/promises once never matched: the scan looked for a quoted "fs" before trying its pattern (#10715).
        for text in ('import { readFile } from "node:fs/promises";\n',
                     "import { readdir } from 'fs/promises';\n",
                     'const { readFile } = await import("node:fs/promises");\n',
                     'import { readdirSync } from "node:fs";\n',
                     "import * as fs from 'fs';\n",
                     "const text = readFileSync(path, 'utf8');\n"):
            with self.subTest(text=text):
                self.assertTrue(self.paths.WebFile("web/src/lib/a.test.ts", text).fs)

    def test_other_names_ending_in_fs_do_not(self):
        for text in ('const routes = { "/agents/prefs": {} };\n', 'import { refs } from "./refs";\n',
                     'import { vol } from "memfs";\n', "const label = 'fs/promises is a node module';\n"):
            with self.subTest(text=text):
                self.assertFalse(self.paths.WebFile("web/src/lib/a.test.ts", text).fs)


class WebPlanTests(unittest.TestCase):
    """The scan of the real web/ (scripts/ci/paths.py): the reads it knows, and the plan for a few changes (#10619)."""

    @classmethod
    def setUpClass(cls):
        cls.paths = load_paths()

    def test_the_web_lane_covers_what_web_reads_in_each_form(self):
        # One real read per form; the web lane in CI and the web check in check-fast both run on each.
        for sample in ("spec/fixtures/forward/cases.json",          # a JSON import
                       "spec/fixtures/inbox/now.json",              # readFileSync of a directory joined with a name
                       "spec/fixtures/sync/x.json",                 # a Vite glob
                       "spec/fixtures/screen-tiers/x.json",         # a directory a test helper reads through fs
                       "schema/extension/examples/View.v8-x.json",  # a path with a ${…} part
                       "schema/extension/stream.schema.json",       # a ?raw import in a module
                       "schema/extension/manifest.schema.json",     # reached through the $refs of the one above
                       "scripts/mock/inbox_events.json",            # outside spec/ and schema/
                       "server/internal/sessionorigin/testdata/pane_sources.json"):
            with self.subTest(sample=sample):
                self.assertTrue(self.paths.select([sample])["web"], sample)
                self.assertEqual("related", self.paths.web_plan([sample])[0], sample)
        for sample in ("spec/fixtures/continuity/x.json", "spec/tokens/colors.json", "schema/wire/x.schema.json"):
            with self.subTest(sample=sample):
                self.assertFalse(self.paths.select([sample])["web"], sample)
                self.assertEqual("none", self.paths.web_plan([sample])[0], sample)

    def test_each_input_is_outside_web(self):
        inputs = self.paths.web_inputs()
        self.assertTrue(inputs)
        for pattern in inputs:
            self.assertFalse(pattern.startswith("web/"), pattern)
            self.assertNotIn(pattern, ("*", "**"))

    def test_a_view_change_runs_its_related_tests_and_the_tests_that_read_files(self):
        mode, _, files = self.paths.web_plan(["web/src/views/chat/ChatView.tsx"])
        self.assertEqual("related", mode)
        self.assertEqual("src/views/chat/ChatView.tsx", files[0])
        # Tests that read sources and fixtures from disk run on every related run, found by the scan.
        for reader in ("src/lib/inbox-bar.test.ts", "src/views/chat/sync/sync-scenarios.test.tsx",
                       "src/lib/extension-ui-stream.test.ts"):
            self.assertIn(reader, files)
        self.assertNotIn("src/lib/forward.test.ts", files)  # imports its fixture: the import graph has it

    def test_a_stylesheet_change_runs_the_build_that_reads_it(self):
        # vite.public.config.ts reads src/styles.css with readFileSync during the public viewer's build, which
        # scripts/public-share-build.test.mjs runs through Vite's build(): no import of the test reaches the file.
        mode, _, files = self.paths.web_plan(["web/src/styles.css"])
        self.assertEqual("related", mode)
        self.assertIn("scripts/public-share-build.test.mjs", files)

    def test_shared_entry_points_run_the_whole_suite(self):
        for path in ("web/src/lib/store.ts", "web/src/lib/api.ts", "web/src/ui/atoms.tsx", "web/src/i18n/index.ts",
                     "web/src/i18n/test-setup.ts", "web/src/admin/catalog.ts", "web/vite.config.ts",
                     "web/tsconfig.json", "web/package.json", "web/package-lock.json",
                     "web/scripts/installed-deps.mjs"):
            with self.subTest(path=path):
                self.assertEqual("full", self.paths.web_plan([path, "web/src/views/chat/ChatView.tsx"])[0])

    def test_what_the_vitest_setup_file_imports_runs_the_whole_suite(self):
        # web/src/i18n/test-setup.ts loads the admin catalog before every test, though no test needs to import it.
        self.assertEqual(("full", "web/src/admin/catalog.ts is part of the vitest setup", []),
                         self.paths.web_plan(["web/src/admin/catalog.ts"]))



if __name__ == "__main__":
    unittest.main()
