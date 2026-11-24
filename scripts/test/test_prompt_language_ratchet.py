import importlib.util
from pathlib import Path
import tempfile
import subprocess
import json
import os
import sys
import unittest

SOURCE = Path(__file__).resolve().parents[1] / "ci" / "prompt_language_ratchet.py"
HELPER = SOURCE.parent / "ratchet_base.py"  # the ratchet imports it from its own directory


def install(script):
    """Copy the ratchet and the helper it imports to script's place in a fixture repository."""
    script.parent.mkdir(parents=True, exist_ok=True)
    script.write_text(SOURCE.read_text())
    (script.parent / HELPER.name).write_text(HELPER.read_text())


class PromptLanguageRatchetTests(unittest.TestCase):
    def setUp(self):
        spec = importlib.util.spec_from_file_location("prompt_language_ratchet", SOURCE)
        self.ratchet = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(self.ratchet)

    def scan(self, files):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for name, text in files.items():
                path = root / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text(text)
            return self.ratchet.scan(root)

    def test_language_conditions_and_includes(self):
        got = self.scan({
            "agents/old.md": "用中文写",
            "agents/localized.md": '{{if .Language}}Write in {{.Language}}{{else}}用中文写{{end}}',
            "voice/full.md": '{{if eq .Lang "en"}}Hello{{else}}你好{{end}}',
            "kb/output.md": '{{template "kb/language" .}}\n总结记录',
            "kb/language.md": '{{if .Language}}{{.Language}}{{else}}中文{{end}}',
            "agents/comment.md": '{{/* if .Language */}}\n用中文写',
            "agents/data.md": 'The field is .Language. 中文',
            "agents/quoted.md": '{{if eq .Title ".Language"}}中文{{end}}',
            "agents/else_if.md": '{{if .Handled}}Done{{else if eq .Language "English"}}Hello{{else}}你好{{end}}',
            "agents/else_other.md": '{{if .Handled}}Done{{else if .Quiet}}安静{{end}}',
            "agents/plain.md": "English instructions",
        })
        self.assertEqual(got, {"agents": ["agents/comment.md", "agents/data.md", "agents/else_other.md", "agents/old.md", "agents/quoted.md"]})

    def test_same_count_cannot_hide_new_file(self):
        old = {"agents": ["agents/old.md"]}
        new = {"agents": ["agents/new.md"]}
        self.assertTrue(self.ratchet.increases(new, old))
        with self.assertRaises(ValueError):
            self.ratchet.lowered(new, old)

    def test_lower_only_and_new_directory(self):
        old = {"agents": ["agents/a.md", "agents/b.md"]}
        new = {"agents": ["agents/a.md"]}
        self.assertEqual(self.ratchet.lowered(new, old), new)
        self.assertTrue(self.ratchet.increases({"new": ["new/a.md"]}, old))
        self.assertEqual(self.ratchet.lowered({}, old), {})

    def test_cycle_does_not_grant_localization(self):
        got = self.scan({"a/a.md": '{{template "a/b" .}}中文', "a/b.md": '{{template "a/a" .}}中文'})
        self.assertEqual(got, {"a": ["a/a.md", "a/b.md"]})

    def test_baseline_counts_and_paths_are_checked(self):
        with self.assertRaises(ValueError):
            self.ratchet.decode_baseline({"agents": {"count": 2, "files": ["agents/a.md"]}})
        with self.assertRaises(ValueError):
            self.ratchet.decode_baseline({"agents": {"count": 1, "files": ["other/a.md"]}})

    def test_cli_refuses_raised_baseline_and_update(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            script = root / "scripts/ci/prompt_language_ratchet.py"
            install(script)
            prompts = root / "server/internal/prompts/agents"
            prompts.mkdir(parents=True)
            (prompts / "old.md").write_text("中文")
            baseline = root / "scripts/ci/prompt_language_baseline.json"
            baseline.write_text(json.dumps({"agents": {"count": 1, "files": ["agents/old.md"]}}))
            def git(*args):
                subprocess.run(["git", *args], cwd=root, check=True, capture_output=True)
            git("init")
            git("add", ".")
            git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.com", "-c", "core.hooksPath=/dev/null", "commit", "-m", "baseline")
            def run(*args):
                return subprocess.run([sys.executable, str(script), "--base", "HEAD", *args], cwd=root, capture_output=True, text=True)
            self.assertEqual(run().returncode, 0)
            (prompts / "new.md").write_text("中文")
            self.assertNotEqual(run("--update").returncode, 0)
            baseline.write_text(json.dumps({"agents": {"count": 2, "files": ["agents/new.md", "agents/old.md"]}}))
            self.assertNotEqual(run().returncode, 0)
            self.assertNotEqual(run("--update").returncode, 0)

    def test_a_base_baseline_that_cannot_be_fetched_fails_instead_of_passing(self):
        # CI's guards checkout is blobless (#12753): a branch that changed the baseline has the base's copy fetched
        # on first read. A failed fetch must fail the check, not read as "no baseline at the base" and pass.
        with tempfile.TemporaryDirectory() as directory:
            work = Path(directory)
            source = work / "source"
            script = source / "scripts/ci/prompt_language_ratchet.py"
            install(script)
            prompts = source / "server/internal/prompts/agents"
            prompts.mkdir(parents=True)
            (prompts / "old.md").write_text("中文")
            baseline = source / "scripts/ci/prompt_language_baseline.json"
            baseline.write_text(json.dumps({"agents": {"count": 1, "files": ["agents/old.md"]}}))

            def git(*args, cwd=source):
                subprocess.run(["git", "-c", "user.name=Fixture", "-c", "user.email=fixture@example.com",
                                "-c", "core.hooksPath=/dev/null", *args], cwd=cwd, check=True, capture_output=True,
                               timeout=60)
            git("init", "-q", "-b", "main")
            git("add", ".")
            git("commit", "-q", "-m", "baseline")
            git("checkout", "-q", "-b", "raise")
            (prompts / "new.md").write_text("中文")
            baseline.write_text(json.dumps({"agents": {"count": 2, "files": ["agents/new.md", "agents/old.md"]}}))
            git("add", ".")
            git("commit", "-q", "-m", "raise")
            origin = work / "origin.git"
            git("clone", "-q", "--bare", str(source), str(origin), cwd=work)
            git("symbolic-ref", "HEAD", "refs/heads/main", cwd=origin)
            git("config", "uploadpack.allowFilter", "true", cwd=origin)
            clone = work / "clone"
            git("clone", "-q", "--filter=blob:none", "--branch", "raise", origin.as_uri(), str(clone), cwd=work)
            git("remote", "set-url", "origin", (work / "gone.git").as_uri(), cwd=clone)

            env = {key: value for key, value in os.environ.items() if key != "PERSONA_CI_SOURCE_ROOT"}

            def run():
                return subprocess.run([sys.executable, str(clone / "scripts/ci/prompt_language_ratchet.py"),
                                       "--base", "origin/main"], cwd=clone, env=env, capture_output=True, text=True,
                                      timeout=120)
            offline = run()
            self.assertEqual(offline.returncode, 1, offline.stdout)
            self.assertIn("cannot read origin/main:scripts/ci/prompt_language_baseline.json", offline.stdout)
            git("remote", "set-url", "origin", origin.as_uri(), cwd=clone)
            online = run()
            self.assertEqual(online.returncode, 1, online.stdout)
            self.assertIn("new unlocalized prompts: agents/new.md", online.stdout)


class IncreaseOnlyTests(unittest.TestCase):
    """A resolved prompt still in the baseline passes; with a base it may not lose its language handling again."""

    LOCALIZED = '{{if .Language}}Write in {{.Language}}{{else}}用中文写{{end}}'

    def setUp(self):
        temp = tempfile.TemporaryDirectory(prefix="prompt-language-")
        self.addCleanup(temp.cleanup)
        self.root = Path(temp.name)
        self.script = self.root / "scripts/ci/prompt_language_ratchet.py"
        install(self.script)
        self.prompts = self.root / "server/internal/prompts/agents"
        self.prompts.mkdir(parents=True)
        (self.prompts / "old.md").write_text("中文")
        (self.prompts / "fixed.md").write_text(self.LOCALIZED)  # resolved on main; the baseline still lists it
        self.baseline = self.root / "scripts/ci/prompt_language_baseline.json"
        self.baseline.write_text(json.dumps({"agents": {"count": 2, "files": ["agents/fixed.md", "agents/old.md"]}}))
        self.git("init", "-q", "-b", "main")
        self.git("add", ".")
        self.git("commit", "-q", "-m", "main")
        self.git("checkout", "-q", "-b", "change")
        self.env = {key: value for key, value in os.environ.items()
                    if key not in ("PERSONA_CI_SOURCE_ROOT", "CI_BASE_SHA")}

    def git(self, *args):
        subprocess.run(["git", "-c", "user.name=Fixture", "-c", "user.email=fixture@example.com",
                        "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgSign=false", *args], cwd=self.root,
                       check=True, capture_output=True, timeout=60)

    def run_ratchet(self, *args):
        return subprocess.run([sys.executable, str(self.script), "--base", "main", *args], cwd=self.root,
                              env=self.env, capture_output=True, text=True, timeout=120)

    def test_a_resolved_prompt_in_the_baseline_passes_with_a_notice(self):
        result = self.run_ratchet()
        self.assertEqual(result.returncode, 0, result.stdout)
        self.assertIn("1 resolved prompt(s) still in the baseline", result.stdout)
        self.assertIn("agents/fixed.md", result.stdout)
        self.assertIn("python3 scripts/ci/lower_baselines.py", result.stdout)
        self.assertEqual(self.run_ratchet("--update").returncode, 0)
        self.assertEqual(json.loads(self.baseline.read_text()), {"agents": {"count": 1, "files": ["agents/old.md"]}})
        self.assertNotIn("still in the baseline", self.run_ratchet().stdout)

    def test_a_prompt_the_merge_base_localizes_may_not_lose_it_again(self):
        (self.prompts / "fixed.md").write_text("用中文写")
        result = self.run_ratchet()
        self.assertEqual(result.returncode, 1, result.stdout)
        self.assertIn("the merge base already localizes lost it again: agents/fixed.md", result.stdout)
        self.git("commit", "-q", "-am", "drop the language branch")
        self.assertEqual(self.run_ratchet().returncode, 1)
        # Against itself (no change since the base) the baseline alone holds, and it still lists the file.
        result = self.run_ratchet("--base", "HEAD")
        self.assertEqual(result.returncode, 0, result.stdout)

    def test_an_edit_that_keeps_the_omissions_passes(self):
        (self.prompts / "old.md").write_text("中文，改了")
        result = self.run_ratchet()
        self.assertEqual(result.returncode, 0, result.stdout)

    def test_the_default_branch_alone_is_no_base(self):
        # Main verification's checkout: HEAD is the commit just merged and origin/main still names its parent. With
        # no --base and no CI_BASE_SHA the baseline alone holds the prompts, as merge-pr.sh holds them.
        self.git("update-ref", "refs/remotes/origin/main", "HEAD")
        (self.prompts / "fixed.md").write_text("用中文写")
        self.git("commit", "-q", "-am", "drop the language branch")
        result = subprocess.run([sys.executable, str(self.script)], cwd=self.root, env=self.env,
                                capture_output=True, text=True, timeout=120)
        self.assertEqual(result.returncode, 0, result.stdout)
        given = dict(self.env, CI_BASE_SHA="origin/main")
        result = subprocess.run([sys.executable, str(self.script)], cwd=self.root, env=given,
                                capture_output=True, text=True, timeout=120)
        self.assertEqual(result.returncode, 1, result.stdout)
        self.assertIn("the merge base already localizes lost it again: agents/fixed.md", result.stdout)


if __name__ == "__main__":
    unittest.main()
