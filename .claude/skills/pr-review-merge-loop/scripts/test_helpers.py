"""Tests for configurable PR review helpers; no real Herdr or GitHub writes."""
import json
import os
from pathlib import Path
import runpy
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

HERE = Path(__file__).resolve().parent
OPERATIONS = HERE.parents[3] / "scripts" / "operations"
sys.path.insert(0, str(OPERATIONS))
import pr_watch_common
import pr_watch_repair


class HelperTests(unittest.TestCase):
    def test_reviewer_forwards_prompt_json_and_read_boundary(self):
        module = runpy.run_path(str(HERE / "herdr_review.py"))
        flags = module["forwarded_flags"](
            ["-p", "--output-format", "json", "--json-schema", '{"type":"object"}',
             "--tools", "Read", "--permission-mode", "plan"], {"--model"})
        self.assertEqual(flags[:3], ["-p", "--output-format", "json"])
        self.assertEqual(flags[flags.index("--tools") + 1], "Read")
        with self.assertRaises(RuntimeError):
            module["forwarded_flags"](["--model", "other"], {"--model"})

    def test_resource_lock_survives_exec_and_limits_process(self):
        with tempfile.TemporaryDirectory(prefix="pr-review-resource-test-") as root:
            config = Path(root) / "resources.json"
            config.write_text(json.dumps({"slots": 2, "cpus_per_slot": 1, "first_cpu": 0,
                                          "lock_directory": str(Path(root) / "locks"),
                                          "address_space_bytes": 1024**3, "cpu_seconds": 60}))
            children = [subprocess.Popen(
                [sys.executable, "-I", str(HERE / "pr_watch_resources.py"), str(config),
                 "/usr/bin/python3", "-I", "-c",
                 "import os,resource,time; print(os.sched_getaffinity(0),resource.getrlimit(resource.RLIMIT_AS)); time.sleep(2)"],
                stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True) for _ in range(2)]
            try:
                outputs = [child.communicate(timeout=10)[0].split() for child in children]
                self.assertTrue(all(child.returncode == 0 for child in children))
                self.assertEqual(sorted(output[0] for output in outputs), ["{0}", "{1}"])
                self.assertIn("(1073741824,", outputs[0])
            finally:
                for child in children:
                    if child.poll() is None:
                        child.kill()
                        child.wait(timeout=5)

    def test_repair_mounts_rewrite_only_configured_arguments(self):
        with tempfile.TemporaryDirectory(prefix="pr-review-mount-test-") as root:
            source = Path(root) / "adapter.py"
            source.write_text("pass\n")
            with patch.dict(os.environ, {"PR_WATCH_REPAIR_MOUNTS": str(source) + ":/tools/adapter.py"}), \
                    patch.object(pr_watch_repair.shutil, "which", return_value="/usr/bin/bwrap"):
                argv = pr_watch_repair.repair_worker_argv(
                    ["/usr/bin/taskset", "--cpu-list", "0-3", "/usr/bin/python3", "-I", str(source)],
                    root, home=Path(root) / "home")
            index = argv.index("/tools/adapter.py", argv.index("--") + 1)
            self.assertEqual(argv[index - 5:index + 1],
                             ["/tools/repair-bwrap", "--cpu-list", "0-3", "/usr/bin/python3", "-I", "/tools/adapter.py"])
            mount = argv.index("/tools/adapter.py", 0, argv.index("--"))
            self.assertEqual(argv[mount - 2:mount + 1], ["--ro-bind", str(source), "/tools/adapter.py"])

    def test_process_wrapper_is_opt_in_and_never_a_host_default(self):
        with patch.dict(os.environ, {}, clear=True):
            self.assertEqual(pr_watch_common._bounded_command(["git", "status"]), ["git", "status"])
        with patch.dict(os.environ, {"PR_WATCH_PROCESS_WRAPPER": "/usr/bin/python3 -I /private/wrapper.py"}):
            self.assertEqual(pr_watch_common._bounded_command(["git", "status"]),
                             ["/usr/bin/python3", "-I", "/private/wrapper.py", "git", "status"])


if __name__ == "__main__":
    unittest.main()
