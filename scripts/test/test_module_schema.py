"""schema/module.schema.json (#13408): every module manifest holds to it, and the server's modapi.Info has its fields.

    python3 scripts/test/test_module_schema.py

Each module.json under server/internal/modules/<id>/ must validate and carry the directory's name as its id; the
invalid cases below must not. The validator is the one of test_extension_schema.py (the subset the generator knows).
"""

import json
import re
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from test_extension_schema import ANNOTATIONS, VALIDATED, Validator, unknown_keywords  # noqa: E402

ROOT = Path(__file__).resolve().parents[2]
SCHEMA = ROOT / "schema" / "module.schema.json"
MODULES = ROOT / "server" / "internal" / "modules"
INFO = ROOT / "server" / "internal" / "modapi" / "modapi.go"

INVALID = {
    "no id": {"api": 1, "origin": "builtin"},
    "upper-case id": {"id": "Briefing", "api": 1, "origin": "builtin"},
    "unknown origin": {"id": "briefing", "api": 1, "origin": "plugin"},
    "api zero": {"id": "briefing", "api": 0, "origin": "builtin"},
    "unknown field": {"id": "briefing", "api": 1, "origin": "builtin", "routes": []},
    "repeated setting": {"id": "briefing", "api": 1, "origin": "builtin", "settings": ["a", "a"]},
    "empty name": {"id": "briefing", "api": 1, "origin": "builtin", "mcp_tools": [""]},
    "bad need": {"id": "briefing", "api": 1, "origin": "builtin", "needs": ["Voice"]},
    "feature without module prefix": {"id": "briefing", "api": 1, "origin": "builtin", "feature": "briefing"},
}


class ModuleSchemaTest(unittest.TestCase):
    def setUp(self):
        self.schema = json.loads(SCHEMA.read_text())
        self.v = Validator({SCHEMA.name: self.schema})

    def errors(self, value):
        return self.v.errors(self.schema, value, SCHEMA.name)

    def test_schema_keywords_are_known(self):
        self.assertEqual(unknown_keywords(self.schema, SCHEMA.name), set(), f"known: {sorted(VALIDATED | ANNOTATIONS)}")

    def test_module_manifests_hold_to_the_schema(self):
        dirs = [d for d in sorted(MODULES.iterdir()) if d.is_dir() and d.name != "all"]
        self.assertTrue(dirs, "no module under server/internal/modules")
        for d in dirs:
            with self.subTest(module=d.name):
                manifest = json.loads((d / "module.json").read_text())
                self.assertEqual(self.errors(manifest), [])
                self.assertEqual(manifest["id"], d.name)

    def test_invalid_manifests_are_refused(self):
        for name, value in INVALID.items():
            with self.subTest(case=name):
                self.assertNotEqual(self.errors(value), [], f"{name} was accepted")

    def test_go_info_has_the_schema_fields(self):
        info = INFO.read_text()
        body = info[info.index("type Info struct {"):]
        body = body[:body.index("\n}\n")]
        tags = set(re.findall(r'json:"([a-z0-9_]+)', body))
        fields = set(self.schema["$defs"]["Module"]["properties"]) - {"$schema"}
        self.assertEqual(tags, fields, "modapi.Info and schema/module.schema.json name different fields")


if __name__ == "__main__":
    unittest.main()
