#!/usr/bin/env python3
"""Standard-library tests for token validation, contrast and reproducible output."""

import contextlib
import copy
import io
import json
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parent))
import gen_tokens as gen


def color(hex_value, alpha=1):
    return {"colorSpace": "srgb", "components": [int(hex_value[i:i + 2], 16) / 255 for i in (0, 2, 4)], "alpha": alpha}


def token(kind, value):
    return {"$type": kind, "$value": value, "$description": "测试用例"}


class ContrastTests(unittest.TestCase):
    def test_known_pairs(self):
        for fg, bg, expected in (("000000", "FFFFFF", 21), ("FFFFFF", "FFFFFF", 1), ("777777", "FFFFFF", 4.478089453577214), ("FF0000", "FFFFFF", 3.9984767707539985)):
            with self.subTest(foreground=fg, background=bg):
                self.assertAlmostEqual(gen.contrast_ratio(color(fg), color(bg)), expected, places=10)
                self.assertAlmostEqual(gen.contrast_ratio(color(bg), color(fg)), expected, places=10)

    def test_alpha_compositing(self):
        fg, bg = color("000000", .5), color("FFFFFF")
        self.assertEqual(gen.composite(fg, bg)["components"], [.5, .5, .5])
        self.assertAlmostEqual(gen.contrast_ratio(fg, bg), 3.976653024912438, places=10)
        transparent = color("FF0000", 0)
        self.assertEqual(gen.contrast_ratio(transparent, bg), 1)
        self.assertAlmostEqual(gen.contrast_ratio(color("000000"), fg, bg), 5.280822809644651, places=10)
        with self.assertRaises(gen.TokenError):
            gen.contrast_ratio(color("000000"), fg)
        with self.assertRaises(gen.TokenError):
            gen.contrast_ratio(color("000000"), fg, color("FFFFFF", .5))

    def test_srgb_low_branch_and_threshold_not_rounded(self):
        self.assertAlmostEqual(gen.luminance(color("0A0A0A")), 10 / 255 / 12.92)
        self.assertFalse(gen.ContrastResult("native", "light", "x", "f", "b", 4.49999, 4.5).passed)
        self.assertTrue(gen.ContrastResult("native", "light", "x", "f", "b", 4.5, 4.5).passed)


class GeneratorTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.source = self.root / "source"
        self.source.mkdir()
        self.data = {
            "semantic": {s: {m: {"text": token("color", color("000000")), "page": token("color", color("FFFFFF"))} for m in gen.MODES} for s in gen.STYLES},
            "status": {m: {"dot": token("color", color("E5484D", .14))} for m in gen.MODES},
            "radius": {"card": token("dimension", {"value": 14, "unit": "px"})},
            "type": {"body": {"size": token("dimension", {"value": 17, "unit": "px"}), "weight": token("fontWeight", 400)}},
            "motion": {"state": token("duration", {"value": .25, "unit": "s"}), "layout": token("duration", {"value": 300, "unit": "ms"}), "reduced": token("duration", {"value": 0, "unit": "ms"})},
            "sample": {"alias": token("color", "{semantic.native.light.text}"), "number": token("number", 50), "name": token("string", 'system "$font"')},
        }
        self.rules = {"$description": "测试规则", "$extensions": {"persona": {"styles": sorted(gen.STYLES), "modes": sorted(gen.MODES), "contrast": [{"id": "body", "foreground": "semantic.{style}.{mode}.text", "background": "semantic.{style}.{mode}.page", "minimum": 4.5, "description": "正文", "level": "error"}]}}}
        self.save()

    def save(self):
        (self.source / "tokens.json").write_text(json.dumps(self.data, ensure_ascii=False), encoding="utf-8")
        (self.source / "rules.json").write_text(json.dumps(self.rules, ensure_ascii=False), encoding="utf-8")

    def run_cli(self, *args):
        output = io.StringIO()
        with contextlib.redirect_stdout(output), contextlib.redirect_stderr(output):
            code = gen.main(["--source", str(self.source), *map(str, args)])
        return code, output.getvalue()

    def test_stable_output_and_package(self):
        args = ["--kotlin", self.root / "Tokens.kt", "--css", self.root / "tokens.css", "--md", self.root / "palette.md", "--package", "example.design"]
        self.assertEqual(self.run_cli(*args)[0], 0)
        first = {p: p.read_bytes() for p in self.root.iterdir() if p.is_file()}
        self.assertEqual(self.run_cli(*args)[0], 0)
        self.assertEqual(first, {p: p.read_bytes() for p in first})
        code, log = self.run_cli("--check", *args)
        self.assertEqual(code, 0, log)
        kt = (self.root / "Tokens.kt").read_text()
        self.assertIn("package example.design", kt)
        self.assertIn("val card: Dp = 14.dp", kt)
        self.assertIn("val size: TextUnit = 17.sp", kt)
        self.assertIn("val weight: FontWeight = FontWeight(400)", kt)
        self.assertIn("const val state: Int = 250", kt)
        self.assertIn("0.14f)", kt)
        self.assertIn('\\$font', kt)
        css = (self.root / "tokens.css").read_text()
        self.assertIn("--type-body-size: 1.0625rem", css)
        self.assertIn("prefers-reduced-motion: reduce", css)
        self.assertIn('[data-style="native"][data-theme="dark"]', css)
        self.assertIn("/ 0.14)", css)
        md = (self.root / "palette.md").read_text()
        self.assertIn("21.0000 | 4.5 | 通过", md)

    def test_reordering_json_keys_does_not_change_output(self):
        catalog = gen.Catalog(self.source)
        before = gen.render_kotlin(catalog, "example.design")
        self.data = dict(reversed(list(self.data.items())))
        self.save()
        self.assertEqual(before, gen.render_kotlin(gen.Catalog(self.source), "example.design"))

    def test_check_detects_stale_files_without_writing(self):
        path = self.root / "Tokens.kt"
        self.assertEqual(self.run_cli("--kotlin", path)[0], 0)
        before = path.read_bytes()
        self.data["radius"]["card"]["$value"]["value"] = 16
        self.save()
        code, log = self.run_cli("--check", "--kotlin", path)
        self.assertEqual(code, 1)
        self.assertIn("生成文件缺失或过期", log)
        self.assertEqual(before, path.read_bytes())
        self.assertEqual(self.run_cli("--kotlin", path)[0], 0)
        self.assertEqual(self.run_cli("--check", "--kotlin", path)[0], 0)
        path.write_bytes(path.read_bytes().replace(b"\n", b"\r\n"))
        self.assertEqual(self.run_cli("--check", "--kotlin", path)[0], 1)

    def test_check_all_outputs_and_missing_output(self):
        paths = [self.root / name for name in ("Tokens.kt", "tokens.css", "palette.md")]
        args = [part for option, path in zip(("--kotlin", "--css", "--md"), paths) for part in (option, path)]
        self.assertEqual(self.run_cli(*args)[0], 0)
        for path in paths:
            original = path.read_bytes()
            path.unlink()
            code, log = self.run_cli("--check", *args)
            self.assertEqual(code, 1)
            self.assertIn(str(path), log)
            self.assertFalse(path.exists())
            path.write_bytes(original)
        self.assertEqual(self.run_cli("--check", *args)[0], 0)

    def test_check_detects_contrast_failure_and_checks_outputs_too(self):
        path = self.root / "Tokens.kt"
        self.assertEqual(self.run_cli("--kotlin", path)[0], 0)
        self.data["semantic"]["clay"]["dark"]["text"]["$value"] = color("FFFFFF")
        self.save()
        code, log = self.run_cli("--check", "--kotlin", path)
        self.assertEqual(code, 1)
        self.assertIn("clay/dark body", log)
        self.assertIn("1.0000:1 < 4.5:1", log)
        self.assertIn("生成文件缺失或过期", log)
        catalog = gen.Catalog(self.source)
        failures = [r for r in catalog.contrast_results() if not r.passed]
        self.assertEqual(len(failures), 1)
        self.assertEqual(len(catalog.contrast_results()), 12)

    def test_warn_level_reports_without_failing(self):
        self.rules["$extensions"]["persona"]["contrast"][0]["level"] = "warn"
        self.data["semantic"]["clay"]["dark"]["text"]["$value"] = color("FFFFFF")
        self.save()
        code, log = self.run_cli("--check")
        self.assertEqual(code, 0, log)
        self.assertIn("建议项不达标 1 项", log)

    def test_known_failures_pass_until_fixed(self):
        self.data["semantic"]["clay"]["dark"]["text"]["$value"] = color("FFFFFF")
        self.save()
        known = {"failures": [{"style": "clay", "mode": "dark", "rule": "body", "reason": "测试"}]}
        (self.source / gen.KNOWN_FAILURES).write_text(json.dumps(known, ensure_ascii=False), encoding="utf-8")
        code, log = self.run_cli("--check")
        self.assertEqual(code, 0, log)
        self.assertIn("已知例外 1 项", log)
        # Fixed: the stale entry has to go, so the list only shrinks.
        self.data["semantic"]["clay"]["dark"]["text"]["$value"] = color("000000")
        self.save()
        code, log = self.run_cli("--check")
        self.assertEqual(code, 1)
        self.assertIn("请从列表里删掉", log)

    def test_rule_level_is_required(self):
        del self.rules["$extensions"]["persona"]["contrast"][0]["level"]
        self.save()
        code, log = self.run_cli("--check")
        self.assertEqual(code, 1)
        self.assertIn("字段不完整", log)

    def test_transparent_rule_background_requires_canvas(self):
        self.data["semantic"]["native"]["dark"]["page"]["$value"]["alpha"] = .5
        self.save()
        code, log = self.run_cli("--check")
        self.assertEqual(code, 1)
        self.assertIn("canvas", log)
        self.rules["$extensions"]["persona"]["contrast"][0]["canvas"] = "semantic.native.light.page"
        self.save()
        self.assertEqual(self.run_cli("--check")[0], 0)

    def test_bad_json_and_duplicates(self):
        for raw in ('{"a":', '{"a": {}, "a": {}}', '[]', '{"sample":{"number":{"$type":"number","$value":NaN,"$description":"测试"}}}'):
            with self.subTest(raw=raw):
                (self.source / "tokens.json").write_text(raw)
                code, log = self.run_cli("--check")
                self.assertEqual(code, 1)
                self.assertIn("token 检查失败", log)

    def test_invalid_values_rejected(self):
        original = copy.deepcopy(self.data)
        bad = [("color", color("000000", 1.1)), ("color", {"colorSpace": "srgb", "components": [0, 0], "alpha": 1}), ("color", "#FFFFFF"), ("dimension", {"value": True, "unit": "px"}), ("dimension", {"value": -1, "unit": "px"}), ("dimension", {"value": 1, "unit": "sp"}), ("duration", {"value": .1, "unit": "ms"}), ("duration", {"value": 2**32, "unit": "ms"}), ("number", float("inf")), ("fontWeight", 1001), ("fontWeight", 500.5), ("string", "prefix {missing.token}"), ("unknown", 0)]
        for kind, value in bad:
            with self.subTest(kind=kind, value=value):
                self.data = copy.deepcopy(original)
                self.data["sample"]["bad"] = token(kind, value)
                self.save()
                self.assertEqual(self.run_cli("--check")[0], 1)

    def test_reference_validation(self):
        for alias in ("{missing.token}", "{sample.alias}", "{radius.card}"):
            with self.subTest(alias=alias):
                self.data["sample"]["alias"]["$value"] = alias
                self.save()
                self.assertEqual(self.run_cli("--check")[0], 1)
        self.data["sample"]["alias"]["$value"] = "{sample.second}"
        self.data["sample"]["second"] = token("color", "{sample.alias}")
        self.save()
        self.assertIn("循环引用", self.run_cli("--check")[1])

    def test_validation_of_matrix_names_and_rules(self):
        original = copy.deepcopy(self.data)
        del self.data["semantic"]["native"]["dark"]
        self.save()
        self.assertEqual(self.run_cli("--check")[0], 1)
        self.data = original
        self.data["bad-name"] = token("number", 1)
        self.save()
        self.assertEqual(self.run_cli("--check")[0], 1)
        del self.data["bad-name"]
        self.rules["$extensions"]["persona"]["contrast"] = []
        self.save()
        self.assertEqual(self.run_cli("--check")[0], 1)

    def test_output_name_collisions(self):
        self.data["sample"]["someColor"] = token("number", 1)
        self.data["sample"]["some"] = {"color": token("number", 2)}
        self.save()
        code, log = self.run_cli("--check")
        self.assertEqual(code, 1)
        self.assertIn("生成名称冲突", log)

    def test_cli_does_not_overwrite_inputs_or_collide_outputs(self):
        source = self.source / "tokens.json"
        before = source.read_bytes()
        self.assertEqual(self.run_cli("--css", source)[0], 1)
        self.assertEqual(source.read_bytes(), before)
        path = self.root / "same"
        self.assertEqual(self.run_cli("--css", path, "--md", path)[0], 1)
        self.assertFalse(path.exists())
        self.assertEqual(self.run_cli("--kotlin", path, "--package", "bad.package")[0], 1)
        self.assertFalse(path.exists())

    def test_cli_from_another_working_directory(self):
        result = subprocess.run([sys.executable, str(Path(gen.__file__)), "--source", str(self.source), "--check"], cwd=self.root, capture_output=True, text=True, timeout=20)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("12 个对比度组合", result.stdout)


class RepositoryTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.catalog = gen.Catalog()

    def test_ios_explicit_status_colors_and_opacity_match(self):
        swift = (gen.ROOT / "apple/Shared/StatusPalette.swift").read_text()
        palette = (gen.ROOT / "apple/Shared/Design/Palette.swift").read_text()
        pair = r'Color\(light: 0x([A-F0-9]+), dark: 0x([A-F0-9]+)\)'
        pattern = rf'static let (needsYou|working|unread) = StatusTone\(dot: ({pair}|Palette\.\w+), text: {pair}\)'
        matches = re.findall(pattern, swift)
        self.assertEqual({match[0] for match in matches}, {"needsYou", "working", "unread"})
        self.assertEqual(len(matches), 3)
        default_tint = re.search(r'tint \?\? Color.secondary.opacity\(([0-9.]+)\)', swift)
        self.assertIsNotNone(default_tint, "StatusTone default tint not found")
        alpha = float(default_tint[1])
        for state, dot_expr, dot_light, dot_dark, text_light, text_dark in matches:
            if dot_expr.startswith("Palette."):
                name = dot_expr.removeprefix("Palette.")
                resolved = re.search(rf'static let {name} = {pair}', palette)
                self.assertIsNotNone(resolved, f"{dot_expr} not found in Palette.swift")
                dot_light, dot_dark = resolved.groups()
            for mode, dot, text in (("light", dot_light, text_light), ("dark", dot_dark, text_dark)):
                self.assertEqual(self.catalog.values[f"status.{mode}.{state}.dot"], color(dot))
                self.assertEqual(self.catalog.values[f"status.{mode}.{state}.text"], color(text))
        for mode in gen.MODES:
            base = self.catalog.values[f"system.{mode}.secondary"]
            for state in ("needsYou", "working", "unread", "waiting"):
                self.assertEqual(self.catalog.values[f"status.{mode}.{state}.tint"], {**base, "alpha": base["alpha"] * alpha})
        for state in ("idle", "offline"):
            dot_alpha, tint_alpha = map(float, re.search(rf'static let {state} = StatusTone\(dot: Color.secondary.opacity\(([0-9.]+)\), text: .secondary, tint: Color.secondary.opacity\(([0-9.]+)\)', swift).groups())
            for mode in gen.MODES:
                base = self.catalog.values[f"system.{mode}.secondary"]
                self.assertEqual(self.catalog.values[f"status.{mode}.{state}.text"], base)
                for part, opacity in (("dot", dot_alpha), ("tint", tint_alpha)):
                    self.assertEqual(self.catalog.values[f"status.{mode}.{state}.{part}"], {**base, "alpha": base["alpha"] * opacity})
        # Waiting in the background: Working's dot made lighter, its words the secondary gray.
        waiting = re.search(r'static let backgroundWaiting = StatusTone\(dot: working.dot.opacity\(([0-9.]+)\), text: .secondary, tint: working.dot.opacity\(([0-9.]+)\)\)', swift)
        self.assertIsNotNone(waiting, "StatusPalette.backgroundWaiting not found")
        dot_alpha, tint_alpha = map(float, waiting.groups())
        for mode in gen.MODES:
            working = self.catalog.values[f"status.{mode}.working.dot"]
            self.assertEqual(self.catalog.values[f"status.{mode}.backgroundWaiting.text"], self.catalog.values[f"system.{mode}.secondary"])
            for part, opacity in (("dot", dot_alpha), ("tint", tint_alpha)):
                self.assertEqual(self.catalog.values[f"status.{mode}.backgroundWaiting.{part}"], {**working, "alpha": working["alpha"] * opacity})

    def test_ios_theme_explicit_colors_match(self):
        # Since 2026-11-01 iOS keeps a look as UI style × theme color (apple/Shared/Design): each catalog style is its component
        # with the colors it came with, and Claude is Liquid Glass in coral.
        design = gen.ROOT / "apple/Shared/Design"
        styles = "".join(p.read_text() for p in sorted((design / "Styles").glob("*.swift")))

        def pair(text, label):
            return re.search(rf'{label}: (?:Pair|Color)\(light: 0x([A-F0-9]+), dark: 0x([A-F0-9]+)\)', text).groups()

        def colors(name):
            return re.search(rf'static let {name} = ColorSpec\((.*?)\n\}}', styles, re.S)[1]

        # The system canvas is systemBackground under secondarySystemBackground cards.
        system = {"pageBackground": ("FFFFFF", "000000"), "card": ("F2F2F7", "1C1C1E")}
        clay, teal = colors("lavender"), colors("teal")
        island_page = re.search(r'w\.page = Pair\(light: 0x([A-F0-9]+), dark: 0x([A-F0-9]+)\)', teal).groups()
        island_card = ("FFFFFF", re.search(r'w\.card\.dark = 0x([A-F0-9]+)', teal)[1])  # white islands; the wash's cards in dark
        lark = re.search(r'canvas: \.neutral\((.*?)\),\n', styles, re.S)[1]
        canvas = {
            "glass": system, "claude": system,
            "clay": {"pageBackground": pair(clay, "page"), "card": pair(clay, "card")},
            "island": {"pageBackground": island_page, "card": island_card},
            "lark": {"pageBackground": pair(lark, "page"), "card": pair(lark, "card")},
        }
        for style, name in {"glass": "blue", "claude": "coral", "clay": "lavender", "island": "teal", "lark": "lark"}.items():
            spec = colors(name)
            expected = {"accent": pair(spec, "accent"), "userBubble": pair(spec, "bubble"), **canvas[style]}
            for token, (light, dark) in expected.items():
                for mode, h in (("light", light), ("dark", dark)):
                    self.assertEqual(self.catalog.values[f"semantic.{style}.{mode}.{token}"], color(h), f"{style} {mode} {token}")
        swift = (design / "Theme.swift").read_text()
        palette = (design / "Palette.swift").read_text()
        for name in ("brand", "danger", "positive", "caution", "cautionText", "ai", "loop", "countBadge", "codeBackground", "terminalBackground", "terminalText"):
            found = re.search(rf'static let {name} = Color\(light: 0x([A-F0-9]+), dark: 0x([A-F0-9]+)\)', swift)
            if found is None:  # a palette color by name (brand = Palette.coral)
                ref = re.search(rf'static let {name} = Palette\.(\w+)', swift)
                if ref is not None:
                    found = re.search(rf'static let {ref[1]} = Color\(light: 0x([A-F0-9]+), dark: 0x([A-F0-9]+)\)', palette)
            if found is None:  # a role an extension can recolor (danger = Role.danger, a PersonaPalette.Pair)
                found = re.search(rf'static let {name} = PersonaPalette\.Pair\(light: 0x([A-F0-9]+), dark: 0x([A-F0-9]+)\)', swift)
            self.assertIsNotNone(found, f"Theme.{name} not found in Theme.swift")
            light, dark = found.groups()
            for mode, h in (("light", light), ("dark", dark)):
                self.assertEqual(self.catalog.values[f"common.{mode}.{name}"], color(h), f"{name} {mode}")
                self.assertEqual(self.catalog.values[f"status.{mode}.waiting.text"], self.catalog.values[f"common.{mode}.loop"])
                self.assertEqual(self.catalog.values[f"status.{mode}.muted.text"], self.catalog.values[f"status.{mode}.idle.text"])

    KOTLIN = gen.ROOT / "android/core/designsystem/src/main/kotlin/persona/core/designsystem/Tokens.kt"

    def test_committed_kotlin_matches_the_generator(self):
        self.assertEqual(self.KOTLIN.read_text(), gen.render_kotlin(self.catalog, "persona.core.designsystem"),
                         f"Tokens.kt is stale: run python3 scripts/assets/gen_tokens.py --kotlin {self.KOTLIN.relative_to(gen.ROOT)}")

    def test_repository_tokens_pass_the_check(self):
        output = io.StringIO()
        with contextlib.redirect_stdout(output), contextlib.redirect_stderr(output):
            code = gen.main(["--check", "--kotlin", str(self.KOTLIN)])
        self.assertEqual(code, 0, output.getvalue())

    # iOS values the JSON doesn't carry, as (iOS, JSON); None is a missing side. Spacing is aligned (T2); motion and type have
    # their own tests below. A Swift change outside this table fails.
    KNOWN_DRIFT = {
        "radius": {"thumbnail": (3, None), "page": (55, None)},
        "space": {},
    }

    def swift_enum(self, name):
        theme = (gen.ROOT / "apple/Shared/Design/Theme.swift").read_text()
        found = re.search(rf'\n    enum {name} \{{\n(.*?)\n    \}}\n', theme, re.S)
        self.assertIsNotNone(found, f"Theme.{name} not found in Theme.swift")
        return found[1]

    def assert_drift(self, group, ios):
        tokens = {key.removeprefix(f"{group}."): value["value"] for key, value in self.catalog.values.items() if key.startswith(f"{group}.")}
        drift = {name: (ios.get(name), tokens.get(name)) for name in ios.keys() | tokens.keys() if ios.get(name) != tokens.get(name)}
        self.assertEqual(drift, self.KNOWN_DRIFT[group], f"Theme.swift and spec/tokens differ in {group}")

    def test_ios_radius_and_spacing_match(self):
        # A number, or another value of the same enum (Theme.Space.lines = xs).
        value = r'static let (\w+): CGFloat = ([0-9.]+|[a-z]\w*)$'
        for group, enum in (("radius", "Radius"), ("space", "Space")):
            found = dict(re.findall(value, self.swift_enum(enum), re.M))
            ios = {name: float(found.get(v, v)) for name, v in found.items()}
            self.assertTrue(ios, f"no Theme.{enum} values found")
            self.assert_drift(group, ios)

    def test_ios_sizes_match(self):
        # spec/tokens carries the sizes Android uses, each as Theme.Size has it; the rest of Theme.Size isn't ported.
        ios = {name: float(value) for name, value in re.findall(r'static let (\w+): CGFloat = ([0-9.]+)$', self.swift_enum("Size"), re.M)}
        tokens = {key.removeprefix("size."): value["value"] for key, value in self.catalog.values.items() if key.startswith("size.")}
        self.assertTrue(tokens)
        self.assertEqual({name: ios.get(name) for name in tokens}, tokens, "a size in spec/tokens differs from Theme.Size")

    def test_ios_type_levels_match(self):
        # Theme.Font on the iPhone (the #else branch): each name is a Dynamic Type style, maybe with a weight or monospaced,
        # and Theme.Level gives its size and line height. sectionHeader is the caption level in semibold.
        font = self.swift_enum("Font")
        iphone = font[font.index("#else"):font.index("#endif")]
        styles = dict(re.findall(r'static let (\w+) = SwiftUI\.Font\.(\w+(?:\.[\w(.)]*)?)$', iphone, re.M))
        theme = (gen.ROOT / "apple/Shared/Design/Theme.swift").read_text()
        level = theme[theme.index("    enum Level {"):theme.index("var extraLeading")]
        sizes_block = level[level.index("#else"):level.index("#endif")]
        heights_block = level[level.index("var lineHeight"):]

        def cases(block):
            out = {}
            for names, value in re.findall(r'case ([.\w, ]+): ([0-9.]+)', block):
                for name in re.findall(r'\.(\w+)', names):
                    out[name] = float(value)
            return out

        sizes, heights = cases(sizes_block), cases(heights_block)
        weights = {"bold()": 700, "weight(.semibold)": 600, "weight(.medium)": 500}
        expected = {}
        for name, expr in styles.items():
            if name in ("scale",) or name.endswith(("Strong", "Heavy", "Medium")):
                continue
            style, _, rest = expr.partition(".")
            weight = weights.get(rest, 600 if style == "headline" else 400)
            lvl = "caption" if name == "sectionHeader" else name
            expected[name] = {"size": sizes[lvl], "weight": weight, "lineHeight": heights[lvl], "iosTextStyle": style,
                              "family": "monospace" if rest == "monospaced()" else "system"}
        tokens = {}
        for key, value in self.catalog.values.items():
            if key.startswith("type."):
                _, name, part = key.split(".")
                tokens.setdefault(name, {})[part] = value["value"] if isinstance(value, dict) and "value" in value else value
        self.assertEqual(len(expected), 9)
        self.assertEqual(expected, tokens)

    def test_android_glass_reading_sizes_use_font_units(self):
        # Android's intentional reading-size difference (#5780) does not enter the shared iOS levels above.
        self.assertEqual(self.catalog.values["type.body.size"], {"value": 17, "unit": "px"})
        self.assertEqual(self.catalog.values["typeGlass.body.size"], {"value": 16, "unit": "px"})
        self.assertEqual(self.catalog.values["typeGlass.secondary.size"], {"value": 14, "unit": "px"})
        block = gen.render_kotlin(self.catalog, "persona.core.designsystem").split("object TypeGlass {", 1)[1]
        self.assertIn("val size: TextUnit = 16.sp", block)
        self.assertIn("val size: TextUnit = 14.sp", block)
        for path, expected in (("typeGlass.body.size", "1rem"), ("typeGlass.secondary.size", "0.875rem")):
            self.assertEqual(gen.css_value(self.catalog.tokens[path]["$type"], self.catalog.values[path], path), expected)

    def test_ios_motion_matches(self):
        # The shared motions (phase 1 R3) as Theme.Motion writes them; the rest of Theme.Motion belongs to single components
        # (the composer's quote bar and growth, holding the mic, attaching, blinks). SwiftUI's snappy has a bounce of 0.15,
        # smooth none.
        motion = self.swift_enum("Motion")

        def seconds(pattern):
            found = re.search(pattern, motion)
            self.assertIsNotNone(found, pattern)
            return [float(value) for value in found.groups()]

        bounce = {"snappy": 0.15, "smooth": 0}
        expected = {}
        for name in ("state", "layout", "expand"):
            kind, duration = re.search(rf'static let {name} = Animation\.(snappy|smooth)\(duration: ([0-9.]+)\)', motion).groups()
            expected[name] = {"duration": round(float(duration) * 1000), "bounce": bounce[kind]}
        for name, curve in (("reveal", "easeOut"), ("conceal", "easeIn")):
            self.assertIn(f"static let {name} = Animation.{curve}(duration: {name}Time)", motion)
            expected[name] = {"duration": round(seconds(rf'static let {name}Time = ([0-9.]+)\n')[0] * 1000), "curve": curve}
        duration, drawer_bounce = seconds(r'\.interpolatingSpring\(duration: ([0-9.]+), bounce: ([0-9.]+), initialVelocity: velocity\)')
        expected["drawer"] = {"duration": round(duration * 1000), "bounce": drawer_bounce}
        duration, back_bounce = seconds(r'static let springBack = Animation\.spring\(duration: ([0-9.]+), bounce: ([0-9.]+)\)')
        expected["springBack"] = {"duration": round(duration * 1000), "bounce": back_bounce}
        mass, stiffness, damping = seconds(r'static let keyboard = Animation\.interpolatingSpring\(mass: ([0-9.]+), stiffness: ([0-9.]+), damping: ([0-9.]+)\)')
        self.assertEqual(mass, 1)
        expected["keyboard"] = {"stiffness": stiffness, "damping": damping}
        breath = {part: seconds(rf'static let needsYou{part}(?:: \w+)? = ([0-9.]+)\n')[0] for part in ("BreathDuration", "BreathScale", "RingOpacity")}
        expected["needsYouBreath"] = {"duration": round(breath["BreathDuration"] * 1000), "scale": breath["BreathScale"], "ringOpacity": breath["RingOpacity"]}
        # The composer's send swap and press (#4210 T3). Theme.Motion writes each as a line of its own; the JSON groups them by
        # what moves. composerRelease is on composerSwap's curve, which the JSON holds once.
        bezier = r'Animation\.timingCurve\(([0-9.]+), ([0-9.]+), ([0-9.]+), ([0-9.]+), duration: ([0-9.]+)\)'
        swap = seconds(rf'static let composerSwap = {bezier}')
        release = seconds(rf'static let composerRelease = {bezier}')
        self.assertEqual(release[:4], swap[:4], "composerRelease and composerSwap share one curve")
        fade = re.search(r'static let composerSwapFade = Animation\.(\w+)\(duration: ([0-9.]+)\)', motion)
        press = re.search(r'static let composerPress = Animation\.(\w+)\(duration: ([0-9.]+)\)', motion)
        self.assertIsNotNone(fade)
        self.assertIsNotNone(press)
        expected["composerSwap"] = {
            "duration": round(swap[4] * 1000), "curveX1": swap[0], "curveY1": swap[1], "curveX2": swap[2], "curveY2": swap[3],
            "fadeDuration": round(float(fade[2]) * 1000), "fadeCurve": fade[1],
            "blur": seconds(r'static let composerSwapBlur: CGFloat = ([0-9.]+)\n')[0],
            "from": seconds(r'static let composerSwapFrom: CGFloat = ([0-9.]+)\n')[0],
            "scale": seconds(r'static let composerSwapScale: CGFloat = ([0-9.]+)\n')[0],
        }
        expected["composerPress"] = {
            "scale": seconds(r'static let composerPressScale: CGFloat = ([0-9.]+)\n')[0],
            "duration": round(float(press[2]) * 1000), "curve": press[1], "releaseDuration": round(release[4] * 1000),
        }
        tokens = {}
        for key, value in self.catalog.values.items():
            parts = key.split(".")
            if parts[0] == "motion" and len(parts) == 3:
                tokens.setdefault(parts[1], {})[parts[2]] = value["value"] if isinstance(value, dict) else value
        self.assertEqual(expected, tokens)
        self.assertEqual(self.catalog.values["motion.reduced"], {"value": 0, "unit": "ms"})

    def test_existing_contrast_conflicts_are_not_silenced(self):
        failures = {(r.style, r.mode, r.rule) for r in self.catalog.contrast_results() if not r.passed}
        self.assertIn(("lark", "dark", "onAccent"), failures)
        self.assertIn(("clay", "light", "status-working"), failures)
        self.assertIn(("island", "light", "status-waiting"), failures)
        self.assertEqual(len(self.catalog.contrast_results()), 12 * len(self.catalog.rules["contrast"]))


if __name__ == "__main__":
    unittest.main()
