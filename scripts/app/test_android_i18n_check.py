import tempfile
import unittest
from pathlib import Path

from android_i18n_check import (count, literals, regressions, resource_problems, write_baselines, baselines, languages, language_problems,
                                english, english_hits_by_file, ENGLISH_BASELINE)


def write(root, path, text):
    file = root / path
    file.parent.mkdir(parents=True, exist_ok=True)
    file.write_text(text, encoding='utf-8')


def strings(*items):
    return '<resources>\n' + '\n'.join(items) + '\n</resources>\n'


class I18nCheckTest(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.TemporaryDirectory()
        self.root = Path(self.dir.name)
        write(self.root, 'feature/demo/build.gradle.kts', '')
        self.module = self.root / 'feature/demo'
        for code, name, matches, directory, plural in [
            ('en', 'English', 'en', 'values', 'one,other'),
            ('zh-Hans', '简体中文', 'zh', 'values-zh', 'other'),
            ('ja', '日本語', 'ja', 'values-ja', 'other'),
        ]:
            write(self.root, f'i18n/{code}.properties',
                  f'name={name}\nmatches={matches}\nresources={directory}\nplurals={plural}\n')

    def tearDown(self):
        self.dir.cleanup()

    def resources(self, en, zh, ja):
        write(self.module, 'src/main/res/values/strings.xml', strings(*en))
        write(self.module, 'src/main/res/values-zh/strings.xml', strings(*zh))
        write(self.module, 'src/main/res/values-ja/strings.xml', strings(*ja))
        return resource_problems(self.module, self.root)

    def test_new_language_is_discovered_and_missing_translations_fail(self):
        write(self.root, 'i18n/ko.properties', 'name=한국어\nmatches=ko\nresources=values-ko\nplurals=other\n')
        self.assertEqual([], language_problems(self.root))
        self.assertIn('values-ko', languages(self.root))
        problems = self.resources(['<string name="demo_hi">Hi</string>'], ['<string name="demo_hi">你好</string>'],
                                  ['<string name="demo_hi">こんにちは</string>'])
        self.assertIn('"demo_hi" is missing from values-ko/', '\n'.join(problems))
        write(self.module, 'src/main/res/values-ko/strings.xml', strings('<string name="demo_hi">안녕</string>'))
        self.assertEqual([], resource_problems(self.module, self.root))

    def test_invalid_language_metadata_fails(self):
        write(self.root, 'i18n/ko.properties', 'name=\nmatches=en\nresources=values-ja\nplurals=one\n')
        text = '\n'.join(language_problems(self.root))
        for expected in ['name is empty', 'duplicate or invalid resources', 'own language subtag', 'duplicate or invalid match', 'invalid CLDR']:
            self.assertIn(expected, text)

    def test_matching_resources_pass(self):
        self.assertEqual([], self.resources(
            ['<string name="demo_hi">Hi %1$s</string>', '<string name="demo_name" translatable="false">日本語</string>',
             '<plurals name="demo_items"><item quantity="one">%d item</item><item quantity="other">%d items</item></plurals>'],
            ['<string name="demo_hi">你好 %1$s</string>', '<plurals name="demo_items"><item quantity="other">%d 项</item></plurals>'],
            ['<string name="demo_hi">%1$s さん</string>', '<plurals name="demo_items"><item quantity="other">%d 件</item></plurals>'],
        ))

    def test_missing_keys_placeholders_plurals_and_prefix_are_reported(self):
        problems = self.resources(
            ['<string name="demo_hi">Hi %1$s</string>', '<string name="other_key">Bye</string>',
             '<plurals name="demo_items"><item quantity="other">%d items</item></plurals>'],
            ['<string name="demo_hi">你好</string>', '<plurals name="demo_items"><item quantity="other">%d 项</item></plurals>'],
            ['<string name="demo_hi">%1$s さん</string>', '<string name="demo_extra">余分</string>',
             '<plurals name="demo_items"><item quantity="other">%d 件</item></plurals>'],
        )
        text = '\n'.join(problems)
        self.assertIn('"other_key" should start with "demo_"', text)
        self.assertIn('"other_key" is missing from values-zh/', text)
        self.assertIn('"demo_hi" has placeholders', text)
        self.assertIn('"demo_extra" in values-ja/ is not in values/', text)
        self.assertIn('plurals "demo_items" in values/ needs', text)

    def test_the_english_fallback_has_no_chinese(self):
        problems = self.resources(['<string name="demo_hi">你好</string>'], ['<string name="demo_hi">你好</string>'],
                                  ['<string name="demo_hi">こんにちは</string>'])
        self.assertIn('values/ is the English fallback', '\n'.join(problems))

    def test_string_arrays_validate_length_and_placeholders_by_index(self):
        def array(*items):
            return ('<string-array name="demo_items">' + ''.join(f'<item>{text}</item>' for text in items)
                    + '</string-array>')

        en = [array('Name %1$s', 'Label', 'Count %1$d')]
        ja = [array('%1$s', 'Label', '%1$d')]
        self.assertEqual([], self.resources(en, [array('%1$s', 'Label', '%1$d')], ja))
        with self.subTest('missing item'):
            self.assertEqual(['feature/demo: string-array "demo_items" has 3 items in values/ but 2 in values-zh/'],
                             self.resources(en, [array('%1$s', '%1$d')], ja))
        with self.subTest('placeholders swapped across items'):
            problems = self.resources(en, [array('%1$d', 'Label', '%1$s')], ja)
            self.assertEqual([
                'feature/demo: string-array "demo_items" item 1 has placeholders [\'%1$s\'] in values/ but [\'%1$d\'] in values-zh/',
                'feature/demo: string-array "demo_items" item 3 has placeholders [\'%1$d\'] in values/ but [\'%1$s\'] in values-zh/',
            ], problems)

    def test_counts_code_literals_not_comments_or_ignored_lines(self):
        write(self.module, 'src/main/kotlin/A.kt', '\n'.join([
            '// 注释里的中文不算',
            '/* 块注释 /* 嵌套 */ 也不算 */',
            'val a = "需要你"',
            'val b = "ok"; val c = "完成"',
            'val d = """多行',
            '文本"""',
            'val e = "需要你" // i18n-ignore: matches the hub\'s text',
            "val f = '中'",
            'val g = "日本語のテキスト"',
        ]))
        write(self.module, 'src/test/kotlin/T.kt', 'val t = "测试里的不算"')
        write(self.module, 'src/main/res/values-zh/strings.xml', strings('<string name="demo_x">中文</string>'))
        self.assertEqual({'feature/demo/src/main/kotlin/A.kt': 4}, count(self.root))

    def test_only_the_designsystem_debug_catalog_is_exempt(self):
        catalog = 'core/designsystem/src/debug/kotlin/persona/core/designsystem/catalog'
        write(self.root, 'core/designsystem/build.gradle.kts', '')
        write(self.root, f'{catalog}/Catalog.kt', 'val t = "开发者示例"')
        write(self.root, f'{catalog}/nested/Sample.kt', 'val t = "开发者示例"')
        clients = [
            'feature/demo/src/debug/kotlin/Catalog.kt',
            'feature/demo/src/debug/res/layout/client.xml',
            'feature/demo/src/debug/kotlin/persona/core/designsystem/catalog/Client.kt',
            'core/designsystem/src/debug/kotlin/persona/core/designsystem/Client.kt',
            'core/designsystem/src/debug/kotlin/persona/core/designsystem/catalogue/Client.kt',
            'core/designsystem/src/main/kotlin/persona/core/designsystem/catalog/Client.kt',
        ]
        for path in clients:
            write(self.root, path, '<TextView text="客户端中文" />' if path.endswith('.xml') else 'val t = "客户端中文"')
        counts = count(self.root)
        self.assertEqual({path: 1 for path in clients}, counts)
        self.assertEqual(len(clients), len(regressions(counts, {})))

    def test_counts_may_only_go_down(self):
        self.assertEqual([], regressions({'a.kt': 2}, {'a.kt': 3}))
        self.assertEqual(1, len(regressions({'a.kt': 4}, {'a.kt': 3})))
        self.assertEqual(1, len(regressions({'new.kt': 1}, {})))

    def test_update_records_decreases_per_module(self):
        (self.module / 'src').mkdir(exist_ok=True)
        write(self.root, 'feature/other/build.gradle.kts', '')
        (self.root / 'feature/other/src').mkdir(parents=True)
        write_baselines({'feature/demo/src/main/kotlin/A.kt': 2}, self.root)
        self.assertEqual({'feature/demo/src/main/kotlin/A.kt': 2}, baselines(self.root))
        self.assertFalse((self.root / 'feature/other/i18n-baseline.json').exists())
        write_baselines({}, self.root)
        self.assertFalse((self.module / 'i18n-baseline.json').exists())

    def test_escaped_quotes_stay_inside_the_literal(self):
        self.assertEqual([(1, r'"a \" 中"')], literals(r'val x = "a \" 中"'))

    def english_lines(self, *lines):
        return sorted({line for line, _ in self.english_hits(*lines)})

    def english_hits(self, *lines):
        write(self.module, 'src/main/kotlin/A.kt', '\n'.join(lines))
        return english_hits_by_file(self.root).get('feature/demo/src/main/kotlin/A.kt', [])

    def test_english_ui_text_in_compose_positions_is_found(self):
        write(self.root, 'core/designsystem/build.gradle.kts', '')
        write(self.root, 'core/designsystem/src/main/kotlin/J.kt', '\n'.join([
            '@Composable',
            'fun JButton(text: String, onClick: () -> Unit, modifier: Modifier = Modifier) {}',
            '@Composable fun JTextField(value: String, onValueChange: (String) -> Unit, label: String? = null, placeholder: String? = null) {}',
            '@Composable',
            'internal fun DetailField(name: String, value: String, trailing: (@Composable () -> Unit)? = null) {}',
        ]))
        self.assertEqual([1, 2, 3, 4, 5, 6, 7, 8, 9, 11], self.english_lines(
            'Text("Session")',
            'Text(text = "Needs you", style = s)',
            'JButton("Try again", { retry() })',
            'JTextField(v, { }, label = "Base URL")',
            'Icon(PhIcons.Copy, "Copy session")',
            'Box(Modifier.semantics { contentDescription = "Close the page" })',
            'DetailField("Session", session)',
            'Text("Sent to ${name}")',
            'JAction("Mark all as seen") { }',
            'JButton(',
            '    "Open settings",',
            '    onClick = { })',
        ))

    def test_english_check_skips_what_is_not_ui_text(self):
        write(self.root, 'core/designsystem/build.gradle.kts', '')
        write(self.root, 'core/designsystem/src/main/kotlin/J.kt',
              '@Composable fun JTextField(value: String, onValueChange: (String) -> Unit, label: String? = null, placeholder: String? = null) {}')
        self.assertEqual([], self.english_lines(
            'Text("Persona")',
            'Text("Persona Link", style = s)',
            'Text("HEAD")',
            'Text("ok")',
            'Text("%1$s · %2$s")',
            'Text("${a} → ${b}")',
            'Text("+$added")',
            'Text("https://Example.com/Path")',
            'JTextField(v, { }, placeholder = "https://hub.example.com")',
            'JTextField("Hub Address", { })',
            'Text("Retry") // i18n-ignore: matches the hub',
            'Log.w("Persona", "Request failed")',
            'Modifier.testTag("Send Button")',
            'val alpha by animateFloatAsState(1f, label = "Fade In")',
            'AnimatedContent(pages, label = "Proactive Pages")',
            'throw IllegalStateException("Not connected")',
            'Text("Hello " + name)',
            'Text("""Raw Text""")',
            'Text("MainActivity")',
            'Text("Agents")',
            'val label = "Session"',
            '// Text("Commented out")',
            'Text("需要你")',
        ))

    def test_english_in_if_when_and_elvis_branches_of_a_ui_argument_is_found(self):
        write(self.root, 'core/designsystem/build.gradle.kts', '')
        write(self.root, 'core/designsystem/src/main/kotlin/J.kt',
              '@Composable fun JRow(title: String, modifier: Modifier = Modifier) {}')
        self.assertEqual([1, 2, 2, 3, 4, 4, 6, 7, 8, 8, 13, 14, 17], [line for line, _ in self.english_hits(
            'Text(a ?: "Some label")',
            'Text(if (x) "Yes" else "No")',
            'JRow(title = name ?: "Agent", modifier = m)',
            'Text(text = if (kind == "agent") "Agent" else if (b) stringResource(R.string.x) else "Other one")',
            'JRow(',
            '    title = when (k) { 1 -> "One thing" else -> "Other" })',
            'Box(Modifier.semantics { contentDescription = label ?: "Close the page" })',
            'Text(if (on) "On" else "Off")',
            'val s = when {',
            '    a -> "Not shown"',
            '}',
            'Text(when (k) {',
            '    Kind.A -> "First kind"',
            '    else -> "Second kind"',
            '})',
            'Text(a',
            '    ?: "Multi line")',
        )])

    def test_english_branches_skip_what_is_not_the_argument_value(self):
        write(self.root, 'core/designsystem/build.gradle.kts', '')
        write(self.root, 'core/designsystem/src/main/kotlin/J.kt',
              '@Composable fun JRow(title: String, modifier: Modifier = Modifier) {}')
        self.assertEqual([], self.english_lines(
            '@Composable fun JCell(title: String = a ?: "Default title", subtitle: String? = null) {}',
            '@Composable fun JTile(title: String = if (a) "Default one" else "Default two") {}',
            'JRow(title = name ?: return)',
            'Text(a ?: stringResource(R.string.x))',
            'Text(if (x) label else other)',
            'Text(if (kind == "agent") agentLabel else name)',
            'Text(if (x) "agent" else "session")',
            'Text(if (x) "Yes" + y else "No".uppercase())',
            'Text(when (k) { 1 -> "One thing" }.uppercase())',
            'Text(when (k) { 1 -> { "One thing" } })',
            'Text(items.firstOrNull { it.name == "Some Name" } ?: other)',
            'val label = a ?: "Other label"',
            'val title = if (x) "Yes" else "No"',
            'Text(label(if (x) "Yes" else "No"))',
            'Text(x.let { if (it) "Yes" else "No" })',
            'Log.w(tag, a ?: "Request failed")',
            'Text(a ?: "Persona")',
            'Text(if (x) "Yes" else "No") // i18n-ignore: test',
        ))

    def test_unicode_template_names_do_not_break_either_check(self):
        write(self.module, 'src/main/kotlin/A.kt', '\n'.join([
            'val price = "合计 $金额"',
            'val tail = "$"',
            'val x = "a $_b $1"',
            'Text("Total $金额 left")',
        ]))
        # The Chinese / Japanese count is the same as before the English check: lines 1 and 4.
        self.assertEqual({'feature/demo/src/main/kotlin/A.kt': 2}, count(self.root))
        self.assertEqual([4], [line for line, _ in english_hits_by_file(self.root)['feature/demo/src/main/kotlin/A.kt']])

    def test_generic_composables_are_registered(self):
        write(self.root, 'core/designsystem/build.gradle.kts', '')
        write(self.root, 'core/designsystem/src/main/kotlin/J.kt', '\n'.join([
            '@Composable fun <T : Comparable<T>> JPicker(items: List<T>, title: String) {}',
            '@Composable internal fun <K, V> JTable(rows: Map<K, V>, caption: String? = null) {}',
        ]))
        self.assertEqual([1, 2], self.english_lines('JPicker(xs, "Pick a model")', 'JTable(rows, caption = "Recent runs")'))

    def test_english_check_skips_shared_test_source_sets(self):
        write(self.module, 'src/sharedTest/kotlin/T.kt', 'Text("Pick one")')
        write(self.module, 'src/test/kotlin/T.kt', 'Text("Pick one")')
        self.assertEqual({}, english_hits_by_file(self.root))

    def test_english_words(self):
        for text in ['Session', 'Try again', 'API Key', 'Couldn’t sync', 'Open Persona settings']:
            self.assertTrue(english(text), text)
        for text in ['session', 'Persona', 'Claude Code', 'URL', 'SSH', 'name@example.com', 'Foo.bar', 'snake_case',
                     'PascalCase', '·', '', '  ', '1/2', 'A']:
            self.assertFalse(english(text), text)

    def test_english_counts_have_their_own_baseline(self):
        (self.module / 'src').mkdir(exist_ok=True)
        write_baselines({'feature/demo/src/main/kotlin/A.kt': 1}, self.root, name=ENGLISH_BASELINE)
        self.assertTrue((self.module / ENGLISH_BASELINE).exists())
        self.assertFalse((self.module / 'i18n-baseline.json').exists())
        self.assertEqual({'feature/demo/src/main/kotlin/A.kt': 1}, baselines(self.root, name=ENGLISH_BASELINE))
        self.assertEqual({}, baselines(self.root))
        self.assertIn('hardcoded English text', regressions({'a.kt': 2}, {'a.kt': 1}, 'English')[0])
        self.assertIn('hardcoded Chinese / Japanese text', regressions({'a.kt': 2}, {'a.kt': 1})[0])


if __name__ == '__main__':
    unittest.main()
