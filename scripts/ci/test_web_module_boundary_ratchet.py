"""Disables of web/src's feature-module boundary rules can stay or fall, never rise (#13434).

The cases build throwaway trees; one case checks this checkout's committed baseline.
"""
import contextlib
import io
import json
from pathlib import Path
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parent))
import web_module_boundary_ratchet as ratchet

ROOT = Path(__file__).resolve().parents[2]


def tree(files, baseline=None):
    root = Path(tempfile.mkdtemp(prefix="module-boundary-ratchet-"))
    for rel, text in files.items():
        path = root / rel
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text, encoding="utf-8")
    if baseline is not None:
        path = root / ratchet.BASELINE_REL
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(baseline), encoding="utf-8")
    return root


class CountTests(unittest.TestCase):
    def test_disables_of_the_boundary_rules_count_and_others_do_not(self):
        text = "\n".join([
            "// eslint-disable-next-line no-restricted-imports -- reaching into a module",
            "import x from '../modules/health/Health'; // eslint-disable-line no-restricted-syntax",
            "/* eslint-disable no-restricted-imports, no-unreachable */",
            "/* eslint-disable */",
            "// eslint-disable-next-line react-hooks/exhaustive-deps",
            "// eslint-disable-next-line no-unreachable -- mentions no-restricted-imports only in the reason",
            "// eslint-disable-next-line persona-modules/boundary",
        ])
        self.assertEqual(ratchet.count_disables(text), 5)


class CheckTests(unittest.TestCase):
    def run_main(self, root, *args):
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            code = ratchet.main(["--root", str(root), *args])
        return code, out.getvalue(), err.getvalue()

    def test_a_new_disable_fails(self):
        root = tree({"web/src/App.tsx": "// eslint-disable-next-line no-restricted-imports\nimport '../modules/health/Health';\n"}, {})
        code, _, err = self.run_main(root)
        self.assertEqual(code, 1)
        self.assertIn("web/src/App.tsx: 1 disable(s) of the module boundary, baseline 0", err)

    def test_none_passes_and_a_fall_passes_with_a_notice(self):
        self.assertEqual(self.run_main(tree({"web/src/App.tsx": "export {};\n"}, {}))[0], 0)
        code, out, _ = self.run_main(tree({"web/src/App.tsx": "export {};\n"}, {"web/src/App.tsx": 2}))
        self.assertEqual(code, 0)
        self.assertIn("under the baseline", out)

    def test_update_lowers_and_refuses_to_raise(self):
        root = tree({"web/src/A.ts": "/* eslint-disable */\n"}, {"web/src/A.ts": 2, "web/src/Gone.ts": 1})
        self.assertEqual(self.run_main(root, "--update")[0], 0)
        self.assertEqual(json.loads((root / ratchet.BASELINE_REL).read_text()), {"web/src/A.ts": 1})
        (root / "web/src/B.ts").write_text("// eslint-disable-line no-restricted-syntax\n")
        code, _, err = self.run_main(root, "--update")
        self.assertEqual(code, 1)
        self.assertIn("refusing to raise web/src/B.ts", err)

    def test_a_broken_baseline_cannot_run(self):
        root = tree({}, None)
        (root / ratchet.BASELINE_REL).parent.mkdir(parents=True, exist_ok=True)
        (root / ratchet.BASELINE_REL).write_text('{"elsewhere/x.ts": 1}')
        self.assertEqual(self.run_main(root)[0], 2)

    def test_this_checkout_passes(self):
        self.assertEqual(ratchet.check(ROOT), [])


if __name__ == "__main__":
    unittest.main()
