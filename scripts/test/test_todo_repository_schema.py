"""Exercise the repository input schemas advertised to MCP clients, without a model or hub."""

import json
from pathlib import Path
import unittest

from test_extension_schema import Validator

ROOT = Path(__file__).resolve().parents[2]


class TodoRepositorySchemaTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.schemas = {}
        for name in ("todo_create", "todo_repositories_set", "todo_repository_links_set"):
            text = (ROOT / "server/internal/prompts/mcp/todo" / (name + ".md")).read_text()
            cls.schemas[name] = json.loads(text.split("```json\n", 1)[1].rsplit("\n```", 1)[0])
        cls.validator = Validator(cls.schemas)

    def check_row(self, row, valid):
        calls = {
            "todo_create": {"title": "Example work", "repository_bindings": {
                "catalog_revision": 1, "repositories": [row]}},
            "todo_repositories_set": {"id": "todo_example", "expected_revision": 0,
                "expected_catalog_revision": 1, "repositories": [row]},
        }
        for name, args in calls.items():
            with self.subTest(tool=name, row=row):
                errors = self.validator.errors(self.schemas[name], args, name)
                self.assertEqual(not errors, valid, errors)

    def test_ref_may_be_omitted_for_older_clients(self):
        self.check_row({"repo_id": "repo_example"}, True)
        for args in ({"title": "Ordinary work"}, {"title": "Empty bindings", "repository_bindings": {
                "catalog_revision": 0, "repositories": []}}):
            self.assertEqual(self.validator.errors(self.schemas["todo_create"], args, "todo_create"), [])

    def test_default_ref_requires_kind_and_omits_value(self):
        for ref, valid in [({"kind": "default"}, True), ({}, False), ({"value": "main"}, False),
                           ({"kind": "default", "value": "main"}, False),
                           ({"kind": "default", "value": ""}, False),
                           ({"kind": "unknown"}, False), ({"kind": "default", "extra": True}, False)]:
            self.check_row({"repo_id": "repo_example", "requested_ref": ref}, valid)

    def test_nondefault_refs_require_nonempty_string_value(self):
        for kind, value in [("branch", "feature/example"), ("tag", "v1.0"),
                            ("commit", "a" * 40), ("ref", "refs/pull/7/head")]:
            for ref, valid in [({"kind": kind, "value": value}, True), ({"kind": kind}, False),
                               ({"kind": kind, "value": ""}, False),
                               ({"kind": kind, "value": 7}, False)]:
                self.check_row({"repo_id": "repo_example", "requested_ref": ref}, valid)

    def check_links(self, links, valid):
        self.check_row({"repo_id": "repo_example", "links": links}, valid)
        name = "todo_repository_links_set"
        args = {"id": "todo_example", "repo_id": "repo_example", "expected_revision": 0, "links": links}
        with self.subTest(tool=name, links=links):
            errors = self.validator.errors(self.schemas[name], args, name)
            self.assertEqual(not errors, valid, errors)

    def test_link_count_boundaries_include_explicit_clear(self):
        for count, valid in [(0, True), (1, True), (50, True), (51, False)]:
            self.check_links([{"kind": "issue", "number": str(i + 1)} for i in range(count)], valid)

    def test_link_numbers_follow_the_manager_decimal_limit(self):
        for kind in ("issue", "pull_request"):
            for number, valid in [("1", True), ("9" * 20, True), ("", False), ("0", False),
                                  ("01", False), ("9" * 21, False), ("-1", False),
                                  ("1.0", False), ("1x", False), (" 1", False), (7, False)]:
                self.check_links([{"kind": kind, "number": number}], valid)
        self.check_links([{"url": "https://github.com/example/web/pull/7", "title": "Example fix"}], True)

    def test_links_require_one_complete_identity(self):
        url = "https://github.com/example/web/pull/7"
        for link, valid in [({}, False), ({"title": "Only a title"}, False),
                            ({"kind": "issue"}, False), ({"number": "7"}, False),
                            ({"url": ""}, False), ({"url": url}, True),
                            ({"kind": "issue", "number": "7"}, True),
                            ({"url": url, "kind": "issue"}, False),
                            ({"url": url, "number": "7"}, False),
                            ({"url": url, "kind": "issue", "number": "7"}, False)]:
            self.check_links([link], valid)


if __name__ == "__main__":
    unittest.main()
