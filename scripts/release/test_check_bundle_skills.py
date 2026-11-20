from pathlib import Path
import sys
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parent))
import check_bundle_skills as cbs


def skill(name: str) -> bytes:
    return b"\x00junk---\nname: " + name.encode() + b"\ndescription: something\n---\n# body\x00"


class CheckBundleSkillsTests(unittest.TestCase):
    def test_reads_the_redistributable_keys_from_embed_go(self):
        self.assertIn("persona-office", cbs.redistributable(cbs.EMBED_GO.read_text()))

    def test_resolves_the_persona_work_constant_key(self):
        self.assertIn("persona-work", cbs.redistributable(cbs.EMBED_GO.read_text()))
        self.assertEqual(cbs.check(skill("persona-work"), cbs.redistributable(cbs.EMBED_GO.read_text())), [])

    def test_constant_key_is_resolved_only_from_the_same_source(self):
        source = """const workGuideName = \"persona-work\"
var Redistributable = map[string]string{
    workGuideName: \"Persona's own guide\",
    otherGuide: \"Not a constant\",
}
"""
        self.assertEqual(cbs.redistributable(source), {"persona-work"})

    def test_allowed_skills_pass(self):
        self.assertEqual(cbs.check(skill("persona-office"), {"persona-office"}), [])

    def test_a_copied_skill_fails(self):
        problems = cbs.check(skill("persona-office") + skill("copied-skill"), {"persona-office"})
        self.assertEqual(len(problems), 1)
        self.assertIn("copied-skill", problems[0])

    def test_any_frontmatter_shape_is_read(self):
        quoted = b"\x00---\ndescription: x\nlicense: y\nname: \"copied-skill\"\n---\n"
        problems = cbs.check(skill("persona-office") + quoted, {"persona-office"})
        self.assertEqual(len(problems), 1)
        self.assertIn("copied-skill", problems[0])

    def test_runtime_memory_frontmatter_is_not_a_bundled_skill(self):
        note = (b"---\ntitle: User preferences and corrections\n"
                b"description: Background summary of stable user preferences and corrections\n"
                b"metadata:\n  type: user\n---\n\n")
        self.assertEqual(cbs.check(skill("persona-office") + note, {"persona-office"}), [])
        self.assertEqual(len(cbs.check(note + skill("copied-skill"), {"persona-office"})), 1)

        self.assertEqual(len(cbs.check(b"\x00nothing here\x00", {"persona-office"})), 1)


if __name__ == "__main__":
    unittest.main()
