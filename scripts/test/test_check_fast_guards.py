"""check-fast.sh on changes outside scripts/: docs, markdown, apple/, deploy/docker/, the wire schemas and shared
fixtures, design tokens and icons select the scripts/test suites that guard them, and migrations the frozen
migrations check.

Split from test_check_fast.py (#11017), whose throwaway repository and helpers it uses, so each file stays well under
the per-suite limit of scripts/ci/python-tests.py on a loaded machine.
"""
import importlib.util
import re
import sys
import unittest

from test_check_fast import FAILING, PASSING, ROOT, ScriptsRepoTestCase


def section_check_dirs():
    """The directories test_doc_sections.py scans besides the Markdown files."""
    saved = list(sys.path)  # the module puts scripts/test on sys.path to import test_doc_paths
    try:
        spec = importlib.util.spec_from_file_location("doc_sections", ROOT / "scripts/test/test_doc_sections.py")
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
    finally:
        sys.path[:] = saved
    return [name for name in module.SOURCE_DIRS if not name.endswith(".md")]


class CheckFastGuardSuitesTests(ScriptsRepoTestCase):
    """The scripts/ suites a change outside scripts/ runs, and the frozen migrations check."""

    def test_a_doc_change_runs_the_doc_path_suites(self):
        self.break_suite("test_doc_paths.py")
        self.write("docs/x.md", "see `scripts/missing.py`\n")
        code, out = self.check_fast()
        self.assertEqual(1, code, out)
        self.assertIn("FAIL     scripts/test/test_doc_paths.py", out)
        self.assertIn("PASS     scripts/test/test_doc_go_paths.py", out)
        self.assertIn("PASS     scripts/test/test_doc_sections.py", out)
        self.assertIn("PASS     scripts/test/test_doc_wire.py", out)
        self.assertNotIn("test_apple_l10n.py", out)
        self.assertNotIn("test_docker_entrypoint.py", out)

    def test_a_top_level_markdown_change_runs_the_doc_path_suites(self):
        self.write("CLAUDE.md", "index\n")
        code, out = self.check_fast()
        self.assertEqual(0, code, out)
        self.assertIn("PASS     scripts/test/test_doc_paths.py", out)
        self.assertIn("PASS     scripts/test/test_doc_go_paths.py", out)
        self.assertIn("PASS     scripts/test/test_doc_sections.py", out)
        self.assertIn("PASS     scripts/test/test_doc_wire.py", out)
        self.assertNotIn("no fast check maps to", out)

    def test_a_change_to_the_wire_lists_runs_the_doc_wire_suite(self):
        # test_doc_wire.py reads these sources: a new message type, flag, point or field breaks it from the code side.
        self.break_suite("test_doc_wire.py")
        for path in ("server/internal/proto/proto.go", "server/internal/hub/extensions_link.go",
                     "server/internal/extension/points.go", "schema/extension/manifest.schema.json"):
            with self.subTest(path=path):
                self.write(path, "changed\n")
                code, out = self.check_fast(path=self.toolchain_without_node_and_go())
                self.git("clean", "-fdq", "--", path.split("/", 1)[0])
                self.assertEqual(1, code, out)
                self.assertIn("FAIL     scripts/test/test_doc_wire.py", out)
                self.assertNotIn("test_doc_paths.py", out)

    def test_wire_schema_inputs_run_the_wire_sample_suite(self):
        # test_wire_schema.py holds the samples the shared fixtures keep for each wire schema to it, runs the generator's
        # --check and imports the extension test's validator.
        self.break_suite("test_wire_schema.py")
        self.commit_on_top("scripts/test/test_sdk_bundle.py", PASSING, "base")  # a schema/ change runs it too
        self.commit_on_top("scripts/gen/extension_schema.py", "", "base")
        for path in ("schema/wire/x.schema.json", "spec/fixtures/todo-view/x.json", "scripts/gen/extension_schema.py",
                     "scripts/test/test_extension_schema.py"):
            with self.subTest(path=path):
                self.write(path, "# changed\n")
                code, out = self.check_fast(path=self.toolchain_without_node_and_go())
                self.git("checkout", "-q", "base", "--", "scripts")
                self.git("clean", "-fdq", "--", path.split("/", 1)[0])
                self.assertEqual(1, code, out)
                self.assertIn("FAIL     scripts/test/test_wire_schema.py", out)

    def test_todo_fixture_inputs_run_the_todo_fixture_suite(self):
        # test_todo_fixtures.py reads the to-do page and window fixtures, the view schema, the todo-view line keys, the
        # agent names and the hub's row fields, and imports the wire and extension schema tests.
        self.break_suite("test_todo_fixtures.py")
        self.commit_on_top("scripts/test/test_sdk_bundle.py", PASSING, "base")  # a schema/ change runs it too
        for path in ("spec/fixtures/todo-detail/x.json", "spec/fixtures/todo-layout/README.md",
                     "spec/fixtures/todo-view/README.md", "spec/fixtures/agent-names/names.json",
                     "schema/wire/todo-view.schema.json", "server/internal/assistant/todos.go",
                     "server/internal/assistant/todo_ask.go", "scripts/test/test_wire_schema.py",
                     "scripts/test/test_extension_schema.py"):
            with self.subTest(path=path):
                self.write(path, "# changed\n")
                code, out = self.check_fast(path=self.toolchain_without_node_and_go())
                self.git("checkout", "-q", "base", "--", "scripts")
                self.git("clean", "-fdq", "--", path.split("/", 1)[0])
                self.assertEqual(1, code, out)
                self.assertIn("FAIL     scripts/test/test_todo_fixtures.py", out)

    def test_other_todo_files_do_not_run_the_todo_fixture_suite(self):
        self.break_suite("test_todo_fixtures.py")
        for path in ("spec/fixtures/todo-view/todo_view.json", "spec/fixtures/todo-counts/x.json",
                     "server/internal/assistant/todo_plan.go"):
            with self.subTest(path=path):
                self.write(path, "{}\n")
                code, out = self.check_fast(path=self.toolchain_without_node_and_go())
                self.git("clean", "-fdq", "--", path.split("/", 1)[0])
                self.assertNotIn("test_todo_fixtures.py", out)

    def test_other_schema_and_spec_files_do_not_run_the_wire_sample_suite(self):
        self.break_suite("test_wire_schema.py")
        self.commit_on_top("scripts/test/test_sdk_bundle.py", PASSING, "base")
        for path in ("schema/extension/x.schema.json", "spec/tokens/x.json"):
            with self.subTest(path=path):
                self.write(path, "{}\n")
                code, out = self.check_fast(path=self.toolchain_without_node_and_go())
                self.git("clean", "-fdq", "--", path.split("/", 1)[0])
                self.assertNotIn("test_wire_schema.py", out)

    def test_a_change_where_the_section_check_reads_runs_it(self):
        # Most names test_doc_sections.py checks are in code comments, so a change under any of its SOURCE_DIRS runs it.
        self.break_suite("test_doc_sections.py")
        for directory in section_check_dirs():
            path = f"{directory}/sample.txt"
            with self.subTest(path=path):
                self.write(path, "changed\n")
                code, out = self.check_fast(path=self.toolchain_without_node_and_go())
                (self.repo / path).unlink()
                self.assertEqual(1, code, out)
                self.assertIn("FAIL     scripts/test/test_doc_sections.py", out)
                self.assertNotIn("test_doc_paths.py", out)

    def test_an_apple_change_runs_the_l10n_suite(self):
        self.break_suite("test_apple_l10n.py")
        self.commit_on_top("scripts/test/test_apple_no_callkit.py", PASSING, "base")
        self.write("apple/Persona/Views/Settings/Options.swift", 'let s = "中文"\n')
        code, out = self.check_fast()
        self.assertEqual(1, code, out)
        self.assertIn("FAIL     scripts/test/test_apple_l10n.py", out)
        self.assertIn("PASS     scripts/test/test_apple_no_callkit.py", out)
        self.assertIn("SKIP  apple/** builds and tests", out)  # the Mac-only checks are still reported
        self.assertNotIn("test_doc_paths.py", out)

    def test_an_apple_or_xcconfig_change_runs_the_callkit_guard(self):
        # It reads every Apple source and build file, and the xcconfig scripts/persona_config.py writes (#9290).
        self.break_suite("test_apple_no_callkit.py")
        for path in ("apple/Persona/Calls/Call.swift", "scripts/persona_config.py"):
            with self.subTest(path=path):
                self.write(path, "changed\n")
                code, out = self.check_fast(path=self.toolchain_without_node_and_go())
                self.git("clean", "-fdq", "--", path.split("/", 1)[0])
                self.assertEqual(1, code, out)
                self.assertIn("FAIL     scripts/test/test_apple_no_callkit.py", out)

    def plans(self, paths):
        """check-fast's plan of each path on its own ({path: [lines]}), from --dry-run --changed-from --each."""
        self.write("listed.txt", "".join(p + "\n" for p in paths))
        code, out = self.check_fast("--dry-run", "--changed-from", "listed.txt", "--each")
        self.assertEqual(0, code, out)
        plans, current = {}, None
        for line in out.splitlines():
            if line.startswith("== "):
                current = plans.setdefault(line[3:], [])
            elif current is not None:
                current.append(line.strip())
        self.assertEqual(sorted(paths), sorted(plans), out)
        return plans

    def test_what_the_development_build_suite_reads_runs_it(self):
        # test_apple_dev_build.py reads the project, the includes its xcodegen dumps turn on, the scripts it names (each
        # that generates a project or builds the Rust core, and the lean build's note), and runs some of them (#11250).
        # The scripts are taken from the suite itself, so one it starts to name is held to the map too.
        self.commit_on_top("scripts/test/test_apple_dev_build.py", PASSING, "base")
        suite = (ROOT / "scripts/test/test_apple_dev_build.py").read_text()
        scripts = sorted(set(re.findall(r"'((?:scripts|native)/[\w./-]+\.sh)'", suite)))
        self.assertIn("scripts/app/lean-build-note.sh", scripts)
        self.assertIn("scripts/diag/stream-scroll/run-ios.sh", scripts)
        project = ["apple/project.yml", "apple/app-entitlements.yml", "apple/watch-and-share.yml",
                   "apple/lean-dev-build.yml", "apple/siri-calls.yml"]
        others = ["apple/homekit-entitlements.yml", "scripts/app/android.sh", "native/app-core/scripts/build-apple.sh"]
        plans = self.plans(scripts + project + others)
        for path, lines in plans.items():
            with self.subTest(path=path):
                self.assertEqual(path not in others, "queued: scripts/test/test_apple_dev_build.py" in lines, lines)
                self.assertFalse([line for line in lines if line.startswith("INFO  no fast check maps to")], lines)

    def test_token_inputs_run_the_token_suite(self):
        self.commit_on_top("scripts/assets/test_gen_tokens.py", FAILING, "base")
        for path in ("spec/tokens/dimension.json", "apple/Shared/Design/Theme.swift",
                     "android/core/designsystem/src/main/kotlin/persona/core/designsystem/Tokens.kt"):
            with self.subTest(path=path):
                self.write(path, "changed\n")
                code, out = self.check_fast(path=self.toolchain_without_node_and_go())
                self.git("clean", "-fdq", "--", path.split("/", 1)[0])
                self.assertEqual(1, code, out)
                self.assertIn("FAIL     scripts/assets/test_gen_tokens.py", out)

    def test_icon_inputs_run_the_icon_suite(self):
        self.commit_on_top("scripts/assets/test_gen_icon_meanings.py", FAILING, "base")
        for path in ("spec/icons/meanings.json", "apple/Shared/Design/Icons.swift",
                     "android/core/designsystem/src/main/kotlin/persona/core/designsystem/icons/PersonaIcons.kt"):
            with self.subTest(path=path):
                self.write(path, "changed\n")
                code, out = self.check_fast(path=self.toolchain_without_node_and_go())
                self.git("clean", "-fdq", "--", path.split("/", 1)[0])
                self.assertEqual(1, code, out)
                self.assertIn("FAIL     scripts/assets/test_gen_icon_meanings.py", out)

    def test_a_docker_change_runs_the_entrypoint_suite(self):
        self.break_suite("test_docker_entrypoint.py")
        self.write("deploy/docker/entrypoint.sh", "echo changed\n")
        code, out = self.check_fast()
        self.assertEqual(1, code, out)
        self.assertIn("FAIL     scripts/test/test_docker_entrypoint.py", out)
        self.assertNotIn("no fast check maps to", out)
        self.assertNotIn("test_doc_paths.py", out)

    def test_a_root_dockerfile_change_runs_the_entrypoint_suite(self):
        # test_docker_entrypoint.py asserts the EXPOSE line of the repo-root Dockerfile.
        self.break_suite("test_docker_entrypoint.py")
        self.write("Dockerfile", "EXPOSE 18660\n")
        code, out = self.check_fast()
        self.assertEqual(1, code, out)
        self.assertIn("FAIL     scripts/test/test_docker_entrypoint.py", out)
        self.assertNotIn("no fast check maps to", out)

    def test_the_docker_doc_runs_both_the_doc_and_the_entrypoint_suites(self):
        self.write("docs/operations/docker.md", "changed\n")
        code, out = self.check_fast()
        self.assertEqual(0, code, out)
        self.assertIn("PASS     scripts/test/test_docker_entrypoint.py", out)
        self.assertIn("PASS     scripts/test/test_doc_paths.py", out)

    def test_a_migration_edit_runs_the_freeze_check(self):
        self.write("scripts/ci/migration_freeze.py", "print('freeze-ran')\n")
        self.write("server/internal/db/migrations/hub/0001_init.sql", "CREATE TABLE t (id int);\n")
        self.git("add", "-A")
        self.git("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "migrations")
        self.git("tag", "-f", "base")
        self.write("server/internal/db/migrations/hub/0001_init.sql", "CREATE TABLE t (id text);\n")
        code, out = self.check_fast(path=self.toolchain_without_node_and_go())
        self.assertEqual(0, code, out)
        self.assertIn("PASS     frozen migrations", out)
        self.assertIn("server go tests (internal/db)", out)  # no Go module here; the freeze check still ran

    def test_an_unrelated_change_does_not_run_the_freeze_check(self):
        self.write("scripts/ci/migration_freeze.py", "print('freeze-ran')\n")
        self.git("add", "-A")
        self.git("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "checker")
        self.git("tag", "-f", "base")
        self.write("docs/guide.md", "changed\n")
        code, out = self.check_fast(path=self.toolchain_without_node_and_go())
        self.assertEqual(0, code, out)
        self.assertNotIn("frozen migrations", out)
        self.assertIn("PASS     scripts/test/test_doc_paths.py", out)

    def test_renaming_a_migration_out_of_its_directory_runs_the_freeze_check(self):
        self.write("scripts/ci/migration_freeze.py", "print('freeze-ran')\n")
        self.write("server/internal/db/migrations/hub/0001_init.sql", "CREATE TABLE t (id int);\n")
        self.git("add", "-A")
        self.git("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "migrations")
        self.git("tag", "-f", "base")
        (self.repo / "notes").mkdir()
        self.git("mv", "server/internal/db/migrations/hub/0001_init.sql", "notes/moved.sql")
        code, out = self.check_fast(path=self.toolchain_without_node_and_go())
        self.assertEqual(0, code, out)
        self.assertIn("PASS     frozen migrations", out)
        self.assertIn("SKIP  server go tests (internal/db): go not found", out)  # --no-renames retains the source path

    def test_all_runs_the_freeze_check_when_the_script_exists(self):
        self.write("scripts/ci/migration_freeze.py", "print('freeze-ran')\n")
        code, out = self.check_fast("--all", path=self.toolchain_without_node_and_go())
        self.assertEqual(1, code, out)  # the stubbed scripts/tools/test_other.py fails, and node/go are missing
        self.assertIn("PASS     frozen migrations", out)
        self.assertIn("queued: scripts unittest suites (8)", out)


if __name__ == "__main__":
    unittest.main()
