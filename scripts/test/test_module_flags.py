#!/usr/bin/env python3
"""Every module flag a client gates on is one a publishing hub announces (#14236, docs/architecture/core-and-modules.md).

A hub that publishes module flags sends the marker "modules"; a client then turns a module with feature module.<id> off
when the flag is missing. So each id a client gates needs a built-in server module whose module.json declares the
feature, or a place in modapi.ClientModules (server/internal/modapi/modapi.go). Without one the module vanishes from
every client of a current hub.
"""
import json
import re
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SKIP = {"node_modules", "build", ".build", "dist", "DerivedData"}


def files(top, *suffixes):
    base = ROOT / top
    if not base.is_dir():
        return
    for path in base.rglob("*"):
        if path.suffix in suffixes and not SKIP & set(path.parts) and "test" not in path.name.lower() \
                and "Tests" not in path.parts and "test" not in path.parts:
            yield path


def gated():
    """{module id: where} for every module a client gates on the hub's hello."""
    found = {}
    for path in files("web/src/modules", ".ts", ".tsx"):
        for m in re.finditer(r'moduleFeature\("([a-z0-9-]+)"\)', path.read_text(encoding="utf-8")):
            found.setdefault(m.group(1), str(path.relative_to(ROOT)))
    for path in files("android/module", ".kt"):
        text = path.read_text(encoding="utf-8")
        if re.search(r"override val feature\s*=\s*ModuleRegistry\.moduleFeature\(id\)", text):
            for m in re.finditer(r'override val id\s*=\s*"([a-z0-9-]+)"', text):
                found.setdefault(m.group(1), str(path.relative_to(ROOT)))
        for m in re.finditer(r'ModuleRegistry\.moduleFeature\("([a-z0-9-]+)"\)', text):
            found.setdefault(m.group(1), str(path.relative_to(ROOT)))
    for path in files("apple", ".swift"):
        for m in re.finditer(r'ModuleDescriptor\(\s*id:\s*"([a-z0-9-]+)",\s*hubGated:\s*true', path.read_text(encoding="utf-8")):
            found.setdefault(m.group(1), str(path.relative_to(ROOT)))
    return found


def announced():
    ids = set()
    for manifest in (ROOT / "server/internal/modules").glob("*/module.json"):
        info = json.loads(manifest.read_text(encoding="utf-8"))
        if info.get("feature") == "module." + info["id"]:
            ids.add(info["id"])
    return ids


def client_only():
    text = (ROOT / "server/internal/modapi/modapi.go").read_text(encoding="utf-8")
    body = re.search(r"func ClientModules\(\) \[\]string \{\s*return \[\]string\{([^}]*)\}", text)
    return set(re.findall(r'"([a-z0-9-]+)"', body.group(1))) if body else set()


class ModuleFlags(unittest.TestCase):
    def test_the_scan_sees_the_gated_modules(self):
        self.assertTrue(gated(), "no gated module found: the patterns in this test no longer match the clients")
        self.assertTrue(client_only(), "modapi.ClientModules was not found")

    def test_every_gated_module_is_announced(self):
        known = announced() | client_only()
        missing = [f"{mid} ({where})" for mid, where in sorted(gated().items()) if mid not in known]
        self.assertEqual([], missing, "gated by a client but announced by no hub: give the server module a feature in its "
                         "module.json or add the id to modapi.ClientModules (server/internal/modapi/modapi.go)")

    def test_no_id_is_both_built_in_and_client_only(self):
        self.assertEqual(set(), announced() & client_only(), "a built-in server module is still in modapi.ClientModules")


if __name__ == "__main__":
    unittest.main()
