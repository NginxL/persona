#!/usr/bin/env python3
"""Tests of core_size.py's line counts (the builds are left to the script's own runs)."""
import importlib.util
import unittest
from pathlib import Path

spec = importlib.util.spec_from_file_location("core_size", Path(__file__).with_name("core_size.py"))
core_size = importlib.util.module_from_spec(spec)
spec.loader.exec_module(core_size)


class CoreSizeTests(unittest.TestCase):
    def test_counts_core_and_each_module_apart(self):
        r = core_size.count("HEAD")
        self.assertGreater(r["core_lines"], 100000)
        self.assertGreater(r["core_test_lines"], 0)
        modules = {p.parent.name for p in (core_size.ROOT / "server/internal/modules").glob("*/module.json")}
        self.assertTrue(modules)
        self.assertLessEqual(modules, set(r["modules"]))
        for mid in modules:
            self.assertGreater(r["modules"][mid]["lines"], 0, mid)


if __name__ == "__main__":
    unittest.main()
