#!/usr/bin/env python3
"""Every Go package, Apple directory, Gradle project and web directory is classified as Persona Core or a feature module
in schema/module-classification.txt, and no line there is stale (#13843; docs/architecture/core-and-modules.md
"Classification").

  python3 scripts/test/test_module_classification.py           run the check (unittest)
  python3 scripts/test/test_module_classification.py --print   print every unit with its class
"""
import sys
import unittest

import module_classification as mc

SETTINGS = 'include(":app", ":core:hub")\ninclude(\n    ":module:voice",\n)\nincludeBuild("build-logic")\n'
FIXTURE = """
module voice
go server/internal/modules/*/... *
go server/internal/hub core
go server/internal/voice/... voice
apple apple/Persona/App core
apple apple/Persona/Views core
apple apple/Persona/Views/Mail voice
gradle :app core
gradle :core:hub core
gradle :module:* *
web web/src/views core
web web/src/views/chat core
"""
FILES = [
    "server/internal/hub/hub.go", "server/internal/voice/voice.go", "server/internal/voice/voicetest/fake.go",
    "server/internal/voice/testdata/x.go", "server/internal/modules/voice/module.go",
    "apple/Persona/App/App.swift", "apple/Persona/Views/Mail/MailView.swift",
    "web/src/views/chat/Chat.tsx",
]


def check(text=FIXTURE, files=FILES, settings=SETTINGS):
    modules, rules = mc.parse(text)
    units, dirs = mc.inventory(files, settings)
    return mc.problems(modules, rules, units, dirs)


class Repository(unittest.TestCase):
    def test_every_unit_is_classified_and_no_line_is_stale(self):
        found = mc.problems(*mc.load())
        self.assertEqual([], found, "\n" + "\n".join(found))

    def test_the_apple_ratchet_reads_the_file(self):
        self.assertIn("apple/Persona/Glasses", mc.apple_module_dirs()["glasses"])


class Fixture(unittest.TestCase):
    def test_a_classified_tree_passes(self):
        self.assertEqual([], check())

    def test_inventory(self):
        units, _ = mc.inventory(FILES, SETTINGS)
        self.assertEqual({"server/internal/hub", "server/internal/voice", "server/internal/voice/voicetest",
                          "server/internal/modules/voice"}, units["go"])  # testdata is not a package
        self.assertEqual({":app", ":core:hub", ":module:voice"}, units["gradle"])  # includeBuild is not a project
        self.assertEqual({"apple/Persona/App", "apple/Persona/Views"}, units["apple"])
        self.assertEqual({"web/src/views", "web/src/views/chat"}, units["web"])

    def test_gradle_inventory_ignores_mentions_outside_literal_includes(self):
        settings = '''
        // include(":feature:comment")
        /* Outer /* include(":feature:nested") */ comment */
        val example = """include(":feature:example")"""
        include(":app", /* include(":feature:inline") */ ":core:hub")
        include(
            ":module:voice",
        )
        includeBuild("build-logic")
        '''
        self.assertEqual([], check(settings=settings))

    def test_gradle_inventory_refuses_computed_includes(self):
        with self.assertRaisesRegex(ValueError, "Computed include"):
            check(settings=SETTINGS + 'include(extraProjects)\n')

    def test_an_unclassified_new_unit_fails(self):
        for kind, extra, settings in [
            ("go", "server/internal/newthing/x.go", SETTINGS),
            ("apple", "apple/Persona/NewThing/X.swift", SETTINGS),
            ("web", "web/src/views/newthing/x.tsx", SETTINGS),
            ("gradle", None, SETTINGS + 'include(":feature:newthing")\n'),
        ]:
            with self.subTest(kind=kind):
                found = check(files=FILES + ([extra] if extra else []), settings=settings)
                self.assertEqual(1, len(found), found)
                self.assertTrue(found[0].startswith(f"unclassified {kind} unit "), found)

    def test_a_stale_line_fails(self):
        for line in ["go server/internal/gone core", "apple apple/Persona/Gone core", "web web/src/gone core",
                     "gradle :feature:gone core", "go server/internal/gone/... voice"]:
            with self.subTest(line=line):
                found = check(FIXTURE + line + "\n")
                self.assertEqual(1, len(found), found)
                self.assertTrue(found[0].startswith("stale line "), found)

    def test_a_deeper_directory_line_is_kept_while_the_directory_exists(self):
        files = [f for f in FILES if "Views/Mail" not in f] + ["apple/Persona/Views/Other.swift"]
        self.assertEqual(1, len(check(files=files)))  # apple/Persona/Views/Mail is gone: stale

    def test_moving_a_directory_under_a_module_root_is_one_deleted_line(self):
        files = [f for f in FILES if not f.startswith("server/internal/voice/")] + ["server/internal/modules/voice/fake/f.go"]
        found = check(files=files)
        self.assertEqual(1, len(found), found)
        self.assertIn("server/internal/voice/...", found[0])
        self.assertEqual([], check(FIXTURE.replace("go server/internal/voice/... voice\n", ""), files=files))

    def test_a_module_root_directory_needs_a_declared_id(self):
        found = check(files=FILES + ["server/internal/modules/mystery/m.go"])
        self.assertEqual(1, len(found), found)
        self.assertIn("'mystery'", found[0])

    def test_the_most_specific_line_wins(self):
        _, rules = mc.parse(FIXTURE + "go server/internal/voice/voicetest core\n")
        self.assertEqual("voice", mc.classify("server/internal/voice", "go", rules)[0])
        self.assertEqual("core", mc.classify("server/internal/voice/voicetest", "go", rules)[0])
        self.assertEqual("voice", mc.classify("server/internal/modules/voice", "go", rules)[0])

    def test_malformed_lines_are_refused(self):
        for bad in ["go server/internal/x/... core",   # core is never inherited
                    "go server/internal/x mystery",    # undeclared module
                    "go server/internal/hub core",     # duplicate
                    "web web/src/*/x core",            # wildcard without `*` class
                    "gradle :module:x/... voice",      # Gradle takes no /...
                    "rust native/app-core core",       # unknown kind
                    "go server/internal/x"]:           # missing class
            with self.subTest(line=bad), self.assertRaises(mc.FormatError):
                mc.parse(FIXTURE + bad + "\n")


def print_units():
    modules, rules, units, _ = mc.load()
    for kind in mc.KINDS:
        for unit in sorted(units[kind]):
            print(kind, unit, mc.classify(unit, kind, rules)[0] or "UNCLASSIFIED")


if __name__ == "__main__":
    if "--print" in sys.argv:
        print_units()
    else:
        unittest.main()
