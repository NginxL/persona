import contextlib
import io
import subprocess
import unittest
import tempfile
from pathlib import Path
from unittest import mock

import refract_about_build as build


class AboutBuildConsumerTest(unittest.TestCase):
    def test_lock_and_production_exclusion(self):
        lock = build.validate_consumer_lock()
        self.assertEqual(build.EXPECTED["commit"], lock["commit"])
        self.assertEqual(build.EXPECTED["mappingSchemaVersion"], 1)
        self.assertEqual(build.SOURCE.parent, build.MAPPING.parent)
        self.assertNotEqual(build.SOURCE, build.source_lock.SOURCE)

    def test_plain_string_fallback_is_rejected(self):
        text = build.GENERATED.read_text(encoding="utf-8")
        with mock.patch.object(build, "GENERATED") as generated:
            generated.read_text.return_value = text.replace(
                "stringResource(R.string.settings_not_provided)", '"settings.not.provided"')
            with self.assertRaisesRegex(ValueError, "optional/native/token/localization"):
                build.validate_consumer_lock()

    def test_production_generated_reference_is_rejected(self):
        with tempfile.TemporaryDirectory(prefix="persona-refract-build-production-") as temporary:
            android = Path(temporary) / "android"
            production = android / "feature/settings/src/main/kotlin/Host.kt"
            production.parent.mkdir(parents=True)
            production.write_text("import persona.feature.settings.generated.AboutBuildRow\n")
            with self.assertRaisesRegex(
                    ValueError, r"feature/settings/src/main/kotlin/Host\.kt$") as error:
                build.validate_production_exclusion(android)
            self.assertIn("AboutBuildRow", production.read_text())
            self.assertEqual(str(production), str(android / error.exception.args[0].split(": ")[-1]))

    def test_production_comments_and_strings_do_not_create_references(self):
        with tempfile.TemporaryDirectory(prefix="persona-refract-build-production-") as temporary:
            android = Path(temporary) / "android"
            production = android / "feature/settings/src/main/kotlin/Host.kt"
            production.parent.mkdir(parents=True)
            production.write_text('''// AboutBuildRow in a line comment
/* AboutBuildSnapshot in a block comment
   /* nested */ still a comment */
/** Documentation mentions AboutBuildRow. */
val ordinary = "AboutBuildRow and AboutBuildSnapshot"
val raw = """AboutBuildRow /* AboutBuildSnapshot */"""
val character = '"'
val escaped = "\\\" AboutBuildSnapshot"
val kept = ordinary.length + raw.length + character.code + escaped.length
''')
            build.validate_production_exclusion(android)

    def test_production_interpolated_generated_reference_is_rejected(self):
        for expression in ("$AboutBuildRow", "${AboutBuildRow}", "${mapOf(1 to AboutBuildSnapshot)[1]}"):
            with self.subTest(expression=expression), \
                    tempfile.TemporaryDirectory(prefix="persona-refract-build-production-") as temporary:
                android = Path(temporary) / "android"
                production = android / "feature/settings/src/main/kotlin/Host.kt"
                production.parent.mkdir(parents=True)
                production.write_text(f'val rendered = "{expression}"\n')
                with self.assertRaisesRegex(ValueError, "test-only consumer"):
                    build.validate_production_exclusion(android)

    def test_debug_and_release_generated_references_are_rejected(self):
        for source_set in ("debug", "release"):
            with self.subTest(source_set=source_set), \
                    tempfile.TemporaryDirectory(prefix="persona-refract-build-production-") as temporary:
                android = Path(temporary) / "android"
                production = android / f"feature/settings/src/{source_set}/kotlin/Host.kt"
                production.parent.mkdir(parents=True)
                production.write_text("AboutBuildRow(AboutBuildSnapshot(\"42\").buildNumber)\n")
                with self.assertRaisesRegex(ValueError, f"src/{source_set}/kotlin/Host.kt$"):
                    build.validate_production_exclusion(android)

    def test_test_source_generated_reference_is_allowed(self):
        for source_set in ("test", "androidTest"):
            with self.subTest(source_set=source_set), \
                    tempfile.TemporaryDirectory(prefix="persona-refract-build-production-") as temporary:
                android = Path(temporary) / "android"
                generated = android / f"feature/settings/src/{source_set}/kotlin/Host.kt"
                generated.parent.mkdir(parents=True)
                generated.write_text("AboutBuildRow(AboutBuildSnapshot(\"42\").buildNumber)\n")
                build.validate_production_exclusion(android)

    def test_host_default_and_optional_slot_are_pinned(self):
        host = build.HOST.read_text(encoding="utf-8")
        build.validate_host(host)
        with self.assertRaisesRegex(ValueError, "handwritten default"):
            build.validate_host(host.replace("buildRow: (@Composable () -> Unit)? = null", "buildRow: @Composable () -> Unit"))
        with self.assertRaisesRegex(ValueError, "handwritten PackageInfo default"):
            build.validate_host(host.replace('Text("${info.longVersionCode}"', 'Text("42"'))
        with self.assertRaisesRegex(ValueError, "only the supplied Build component"):
            build.validate_host(host.replace("buildRow()", "OtherPage()"))

    def test_check_generates_twice_without_patching_the_reviewed_file(self):
        original = build.GENERATED.read_bytes()
        with tempfile.TemporaryDirectory() as temporary:
            cli = Path(temporary) / "refract"
            cli.touch()
            calls = []

            def generate(command, **kwargs):
                calls.append(command)
                if "--version" in command:
                    return mock.Mock(returncode=0, stdout=f"refract 0.1.0 ({build.EXPECTED['commit']})\n")
                output = Path(command[command.index("--android-out") + 1]) / "AboutBuildRow.kt"
                output.parent.mkdir(parents=True, exist_ok=True)
                output.write_bytes(original)
                return mock.Mock(returncode=0)

            with mock.patch.object(build, "validate_consumer_lock", return_value=dict(build.EXPECTED)), \
                    mock.patch.object(build.subprocess, "run", side_effect=generate), \
                    mock.patch.object(build.sys, "argv", ["refract_about_build.py", "check", "--cli", str(cli)]):
                self.assertEqual(build.main(), 0)
        self.assertEqual(2, sum("--version" not in command for command in calls))
        self.assertEqual(build.GENERATED.read_bytes(), original)

    def test_check_rejects_different_second_fresh_output(self):
        original = build.GENERATED.read_bytes()
        with tempfile.TemporaryDirectory() as temporary:
            cli = Path(temporary) / "refract"
            cli.touch()
            generated_outputs = []

            def generate(command, **kwargs):
                if "--version" in command:
                    return mock.Mock(returncode=0, stdout=f"refract 0.1.0 ({build.EXPECTED['commit']})\n")
                output = Path(command[command.index("--android-out") + 1]) / "AboutBuildRow.kt"
                output.parent.mkdir(parents=True, exist_ok=True)
                fresh = original if not generated_outputs else original + b"// nondeterministic second invocation\n"
                output.write_bytes(fresh)
                generated_outputs.append(fresh)
                return mock.Mock(returncode=0)

            stderr = io.StringIO()
            with mock.patch.object(build, "validate_consumer_lock", return_value=dict(build.EXPECTED)), \
                    mock.patch.object(build.subprocess, "run", side_effect=generate), \
                    mock.patch.object(build.sys, "argv", ["refract_about_build.py", "check", "--cli", str(cli)]), \
                    contextlib.redirect_stderr(stderr):
                self.assertEqual(build.main(), 1)
        self.assertEqual(len(generated_outputs), 2)
        self.assertNotEqual(generated_outputs[0], generated_outputs[1])
        self.assertEqual(build.GENERATED.read_bytes(), original)
        self.assertIn("Pinned Build generation is not deterministic", stderr.getvalue())


class AboutBuildGenerationRollbackTest(unittest.TestCase):
    def run_generate(self, generated, generate):
        cli = generated.parent / "refract"
        cli.touch()
        stderr = io.StringIO()
        stdout = io.StringIO()
        with mock.patch.object(build, "GENERATED", generated), \
                mock.patch.object(build, "validate_consumer_lock", return_value=dict(build.EXPECTED)), \
                mock.patch.object(build, "run", side_effect=generate), \
                mock.patch.object(build.sys, "argv", ["refract_about_build.py", "generate", "--cli", str(cli)]), \
                contextlib.redirect_stderr(stderr), contextlib.redirect_stdout(stdout):
            result = build.main()
        return result, stdout.getvalue(), stderr.getvalue()

    def test_nondeterministic_generate_restores_the_entry_bytes(self):
        with tempfile.TemporaryDirectory(prefix="persona-refract-build-rollback-") as temporary:
            generated = Path(temporary) / "AboutBuildRow.kt"
            reviewed = b"reviewed Kotlin at entry\n"
            generated.write_bytes(reviewed)
            invocations = 0

            def generate(cli, mode, android_out, harmony_out):
                nonlocal invocations
                invocations += 1
                generated.write_bytes(reviewed + f"// unreviewed invocation {invocations}\n".encode())

            result, stdout, stderr = self.run_generate(generated, generate)
            self.assertEqual(result, 1)
            self.assertEqual(invocations, 2)
            self.assertEqual(generated.read_bytes(), reviewed)
            self.assertIn("Pinned Build generation is not deterministic", stderr)
            self.assertIn("original reviewed Kotlin restored", stderr)
            self.assertNotIn("PASSED", stdout)

    def test_failed_generate_removes_new_output_without_entry_bytes(self):
        with tempfile.TemporaryDirectory(prefix="persona-refract-build-rollback-") as temporary:
            generated = Path(temporary) / "AboutBuildRow.kt"

            def generate(cli, mode, android_out, harmony_out):
                generated.write_bytes(b"partial unreviewed output\n")
                raise subprocess.CalledProcessError(1, [str(cli), mode], stderr="primary generator failure\n")

            result, stdout, stderr = self.run_generate(generated, generate)
            self.assertEqual(result, 1)
            self.assertFalse(generated.exists())
            self.assertIn("primary generator failure", stderr)
            self.assertIn("new unreviewed Kotlin removed after generation failure", stderr)
            self.assertNotIn("PASSED", stdout)

    def test_new_output_removal_failure_preserves_the_primary_cli_diagnostic(self):
        with tempfile.TemporaryDirectory(prefix="persona-refract-build-rollback-") as temporary:
            generated = Path(temporary) / "AboutBuildRow.kt"

            def generate(cli, mode, android_out, harmony_out):
                generated.write_bytes(b"partial unreviewed output\n")
                raise subprocess.CalledProcessError(1, [str(cli), mode], stderr="primary generator failure\n")

            with mock.patch.object(build, "remove_new_output", side_effect=OSError("fixture removal failure")):
                result, stdout, stderr = self.run_generate(generated, generate)
            self.assertEqual(result, 1)
            self.assertLess(stderr.index("primary generator failure"), stderr.index("Refract About Build: FAILED"))
            self.assertLess(stderr.index("Refract About Build: FAILED"), stderr.index("secondary restoration failure"))
            self.assertIn("fixture removal failure", stderr)
            self.assertNotIn("new unreviewed Kotlin removed", stderr)
            self.assertNotIn("PASSED", stdout)

    def test_restoration_failure_preserves_the_primary_cli_diagnostic(self):
        with tempfile.TemporaryDirectory(prefix="persona-refract-build-rollback-") as temporary:
            generated = Path(temporary) / "AboutBuildRow.kt"
            generated.write_bytes(b"reviewed Kotlin at entry\n")

            def generate(cli, mode, android_out, harmony_out):
                generated.write_bytes(b"partial unreviewed output\n")
                raise subprocess.CalledProcessError(1, [str(cli), mode], stderr="primary generator failure\n")

            result, stdout, stderr = self.run_generate(generated, generate)
            with mock.patch.object(build.source_lock, "restore_reviewed_output",
                                   side_effect=ValueError("Could not restore: fixture restore failure")):
                result, stdout, stderr = self.run_generate(generated, generate)
            self.assertEqual(result, 1)
            self.assertEqual(generated.read_bytes(), b"partial unreviewed output\n")
            self.assertLess(stderr.index("primary generator failure"), stderr.index("Refract About Build: FAILED"))
            self.assertLess(stderr.index("Refract About Build: FAILED"), stderr.index("secondary restoration failure"))
            self.assertIn("fixture restore failure", stderr)
            self.assertNotIn("original reviewed Kotlin restored", stderr)
            self.assertNotIn("PASSED", stdout)


if __name__ == "__main__":
    unittest.main()
