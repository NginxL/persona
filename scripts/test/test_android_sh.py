"""scripts/app/android.sh: how it starts Gradle (#10714, #10712, #11017).

Each test copies android.sh and scripts/heavy_slot.py into a throwaway tree whose android/gradlew records its arguments
and the slot marker it sees, and runs `android.sh test` with a PATH of only the tools the script needs (and a java that
reports JDK 21), so no real Gradle, JDK or Android SDK runs.
"""
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

try:
    import fcntl
except ImportError:  # Windows
    fcntl = None

ROOT = Path(__file__).resolve().parents[2]
# A gradlew that logs "<arguments>|<PERSONA_HEAVY_SLOT_HELD>" and exits with FAKE_GRADLE_EXIT (0 by default).
FAKE_GRADLEW = ('#!/bin/sh\nprintf "%s|%s\\n" "$*" "${PERSONA_HEAVY_SLOT_HELD:-}" >> "$FAKE_GRADLE_LOG"\n'
                'exit "${FAKE_GRADLE_EXIT:-0}"\n')
FAKE_JAVA = '#!/bin/sh\necho \'openjdk version "21.0.5" 2024-10-15\' >&2\n'
# A python3 that logs its arguments and fails, as one that cannot run the helper would.
FAKE_PYTHON = '#!/bin/sh\nprintf "%s\\n" "$*" >> "$FAKE_PYTHON_LOG"\nexit 1\n'
GRADLE_ARGS = "--max-workers=4 -q -PpersonaApplicationId=com.example.persona -PpersonaGettingStartedUrl= test"


class AndroidShGradleTests(unittest.TestCase):
    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp(prefix="android-sh-test-"))
        self.addCleanup(shutil.rmtree, self.tmp, True)
        repo = self.tmp / "repo"
        (repo / "scripts/app").mkdir(parents=True)
        (repo / "android").mkdir()
        shutil.copy(ROOT / "scripts/app/android.sh", repo / "scripts/app/android.sh")
        shutil.copy(ROOT / "scripts/heavy_slot.py", repo / "scripts/heavy_slot.py")
        self.script = repo / "scripts/app/android.sh"
        self.tool(repo / "android/gradlew", FAKE_GRADLEW)
        # Only what android.sh runs on its way to Gradle; no python3 unless a test adds one.
        self.bin = self.tmp / "bin"
        self.bin.mkdir()
        for name in ("dirname", "head", "sed", "grep"):
            found = shutil.which(name)
            self.assertIsNotNone(found, name)
            (self.bin / name).symlink_to(found)
        self.tool(self.bin / "java", FAKE_JAVA)
        self.gradle_log = self.tmp / "gradle.log"
        self.python_log = self.tmp / "python.log"
        home = self.tmp / "home"
        home.mkdir()
        self.env = {key: value for key, value in os.environ.items()
                    if not key.startswith(("PERSONA_HEAVY_SLOT", "ANDROID_", "GRADLE_", "JAVA_"))}
        self.env.update(PATH=str(self.bin), HOME=str(home), ANDROID_HOME=str(self.tmp / "sdk"),
                        FAKE_GRADLE_LOG=str(self.gradle_log), FAKE_PYTHON_LOG=str(self.python_log),
                        PERSONA_HEAVY_SLOT_DIR=str(self.tmp / "slots"))

    def tool(self, path, text):
        path.write_text(text)
        path.chmod(0o755)

    def run_script(self, **env):
        bash = shutil.which("bash")
        self.assertIsNotNone(bash)
        return subprocess.run([bash, str(self.script), "test"], env={**self.env, **env}, capture_output=True,
                              text=True, timeout=60)

    def gradle_calls(self):
        return self.gradle_log.read_text().splitlines() if self.gradle_log.exists() else []

    def test_without_python3_gradle_runs_directly(self):
        done = self.run_script()
        self.assertEqual(0, done.returncode, done.stderr)
        self.assertEqual([GRADLE_ARGS + "|"], self.gradle_calls())
        # Gradle's own exit status is the script's.
        done = self.run_script(FAKE_GRADLE_EXIT="3")
        self.assertEqual(3, done.returncode, done.stderr)

    def test_with_the_slots_off_python3_is_never_asked_to_run_gradle(self):
        self.tool(self.bin / "python3", FAKE_PYTHON)
        done = self.run_script(PERSONA_HEAVY_SLOTS="0")
        self.assertEqual(0, done.returncode, done.stderr)
        self.assertEqual([GRADLE_ARGS + "|"], self.gradle_calls())
        calls = self.python_log.read_text().splitlines() if self.python_log.exists() else []
        self.assertTrue(calls)  # it still reads the config through python3, and copes with a failure
        self.assertEqual([], [call for call in calls if "heavy_slot" in call])

    @unittest.skipIf(fcntl is None, "heavy build slots need fcntl (not on Windows)")
    def test_with_python3_gradle_runs_in_a_gradle_slot(self):
        (self.bin / "python3").symlink_to(sys.executable)
        done = self.run_script()
        self.assertEqual(0, done.returncode, done.stderr)
        calls = self.gradle_calls()
        self.assertEqual(1, len(calls), calls)
        arguments, held = calls[0].split("|")
        self.assertEqual(GRADLE_ARGS, arguments)
        self.assertRegex(held, r"^gradle:\d+$")
        self.assertTrue((self.tmp / "slots/gradle.0.lock").exists())


if __name__ == "__main__":
    unittest.main()
