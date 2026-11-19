"""Portable configuration examples hold to the shared JSON Schema without a new dependency."""

import copy
import json
import unittest
from pathlib import Path

from test_extension_schema import Validator

ROOT = Path(__file__).resolve().parents[2]
DIRECTORY = ROOT / "schema" / "configuration"


class ConfigurationSchemaTests(unittest.TestCase):
    def setUp(self):
        self.schema = json.loads((DIRECTORY / "configuration.schema.json").read_text())
        self.validator = Validator({"configuration.schema.json": self.schema})
        self.document = json.loads((DIRECTORY / "all-types.json").read_text())

    def test_shared_document_and_successful_updates(self):
        self.assertEqual(self.validator.validate("Document", self.document), [])
        cases = json.loads((DIRECTORY / "validation-cases.json").read_text())["cases"]
        self.assertTrue(cases)
        for case in cases:
            if not case["code"]:
                with self.subTest(case=case["name"]):
                    self.assertEqual(self.validator.validate("Update", case["update"]), [])

    def test_discriminator_requires_only_its_own_options(self):
        for field in self.document["groups"][0]["fields"]:
            with self.subTest(field=field["id"]):
                self.assertEqual(self.validator.validate("Field", field), [])
                bad = copy.deepcopy(field)
                if field["kind"] != "toggle":
                    del bad[field["kind"]]
                    self.assertTrue(self.validator.validate("Field", bad))
                bad = {**field, "text" if field["kind"] != "text" else "number": {}}
                self.assertTrue(self.validator.validate("Field", bad))
                self.assertTrue(self.validator.validate("Field", {**field, "default": None}))
                self.assertTrue(self.validator.validate("Field", {**field, "kind": "future"}))

    def test_write_shape_and_disabled_reason(self):
        patch = {"schema_version": 1, "id": "example", "revision": "1", "changes": {"compact": False}}
        self.assertEqual(self.validator.validate("Update", patch), [])
        for bad in ({**patch, "changes": {}}, {**patch, "changes": None},
                    {**patch, "changes": {"compact": None}}, {**patch, "extra": True},
                    {**patch, "schema_version": 2}, {**patch, "reset": ["label", "label"]}):
            self.assertTrue(self.validator.validate("Update", bad), bad)
        field = self.document["groups"][0]["fields"][0]
        self.assertTrue(self.validator.validate("Field", {**field, "disabled_reason": "Unavailable"}))
        self.assertEqual(self.validator.validate("Field", {**field, "read_only": True, "disabled_reason": "Unavailable"}), [])


if __name__ == "__main__":
    unittest.main()
