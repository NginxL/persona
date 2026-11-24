import tempfile
import unittest
from pathlib import Path

from android_token_baseline import baseline_of, count, lowerable, regressions, scan, wildcard_material3


class TokenBaselineTest(unittest.TestCase):
    def test_ignores_comments_strings_and_resolves_aliases(self):
        counts = count('''
            import persona.core.designsystem.Tokens.Space as Spacing
            import androidx.compose.ui.graphics.Color as Tint
            // Color.White 19.sp 99.dp
            /* 88.dp /* Color.Red */ 55.sp */
            val text = "Color.Black 12.sp 21.dp"
            val raw = """99.dp 22.sp"""
            val a = Tint(0xFF123456)
            val b = 14.sp
            val c = 8.dp
            val d = Spacing.m
            val e = PersonaTypography.body
            val f = MaterialTheme.colorScheme.primary
        ''')
        self.assertEqual({k: {'hardcoded': 1, 'token': 1} for k in ('color', 'font', 'dp')}, {k: counts[k] for k in ('color', 'font', 'dp')})

    def test_only_canonical_persona_names_count_as_design_tokens(self):
        canonical = '''
            import persona.core.designsystem.PersonaTheme as Theme
            import persona.core.designsystem.Tokens.Space as Spacing
            val color = Theme.palette.ink
            val font = PersonaTypography.body
            val reading = Tokens.TypeGlass.Body.size
            val size = PersonaTheme.shapes.card
            val gap = Spacing.m
        '''
        counts = count(canonical)
        self.assertEqual(1, counts['color']['token'])
        self.assertEqual(2, counts['font']['token'])
        self.assertEqual(2, counts['dp']['token'])
        former = ('jar' + 'vis').capitalize()
        retired_symbols = f'{former}Theme.palette.ink {former}Typography.body {former}Theme.shapes.card'
        self.assertEqual({key: {'hardcoded': 0, 'token': 0} for key in ('color', 'font', 'dp', 'material3', 'icon')}, count(retired_symbols))

    def test_new_file_or_category_cannot_spend_another_files_reduction(self):
        baseline = baseline_of({'version': 2, 'files': {'a.kt': count('1.dp 2.dp'), 'b.kt': count('Text("ok")')}})
        self.assertEqual({'version': 2, 'files': {'a.kt': {'dp': 2}}}, baseline)  # zero counts and files are left out
        current = {'files': {'a.kt': count('1.dp'), 'b.kt': count('2.dp')}}
        self.assertEqual(1, len(regressions(current, baseline)))
        current = {'files': {'a.kt': count('Color.Red')}}
        self.assertEqual(1, len(regressions(current, baseline)))
        self.assertEqual([], regressions({'files': {'a.kt': count('Tokens.Space.m')}}, baseline))

    def test_a_count_under_the_baseline_passes_and_is_listed_for_lowering(self):
        baseline = baseline_of({'version': 2, 'files': {'a.kt': count('1.dp 2.dp'), 'gone.kt': count('3.dp')}})
        current = {'files': {'a.kt': count('1.dp')}}
        self.assertEqual([], regressions(current, baseline))
        self.assertEqual(['a.kt: 1 hard-coded dp, baseline 2', 'gone.kt: 0 hard-coded dp, baseline 1'],
                         lowerable(current, baseline))
        self.assertEqual([], lowerable({'files': {'a.kt': count('1.dp 2.dp'), 'gone.kt': count('3.dp')}}, baseline))

    def test_material3_components_count_in_pages_only(self):
        page = '''
            import androidx.compose.material3.Text
            import androidx.compose.material3.DropdownMenu
            import androidx.compose.material3.Checkbox as Box3
            // Card(  Button {
            val note = "AlertDialog("
            fun page() {
                Text("ok")
                DropdownMenu(expanded = true, onDismissRequest = {}) {}
                Box3(checked = true, onCheckedChange = null)
                androidx.compose.material3.Card { }
                JCard { }
                Checkbox(checked = true)
            }
        '''
        self.assertEqual(3, count(page)['material3']['hardcoded'])  # DropdownMenu, Box3 and the full name; Checkbox isn't imported
        self.assertEqual(0, count(page, pages=False)['material3']['hardcoded'])
        wildcard = '''
            import androidx.compose.material3.*
            fun page() { Scaffold { } ; TextButton(onClick = {}) { } ; Text("ok") ; val s = SnackbarHostState() }
        '''
        self.assertEqual(2, count(wildcard)['material3']['hardcoded'])
        self.assertEqual(1, wildcard_material3(wildcard))
        self.assertEqual(1, wildcard_material3('import androidx.compose.material3.adaptive.layout.*'))
        self.assertEqual(0, wildcard_material3('import androidx.compose.material3.Text\n// import androidx.compose.material3.*'))

    def test_icons_by_glyph_count_in_pages_and_by_meaning_as_tokens(self):
        page = '''
            import persona.core.designsystem.icons.PhIcons
            // PhIcons.Trash in a comment
            val a = PhIcons.Copy
            val b = persona.core.designsystem.icons.PhIcons.Sparkle.regular
            val c = PersonaIcons.copy
        '''
        self.assertEqual({'hardcoded': 2, 'token': 1}, count(page)['icon'])
        self.assertEqual({'hardcoded': 0, 'token': 1}, count(page, pages=False)['icon'])  # the design system draws glyphs

    def test_scan_covers_every_module_but_tests_and_generated_tokens(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            files = {
                'android/app/src/main/java/a/App.kt': 'val a = 1.dp',
                'android/core/designsystem/src/main/kotlin/d/Parts.kt': 'import androidx.compose.material3.*\nval c = Color.Red',
                'android/core/designsystem/src/main/kotlin/persona/core/designsystem/Tokens.kt': 'val c = Color(0xFF000000)',
                'android/core/hub/src/main/kotlin/h/Hub.kt': 'import androidx.compose.material3.*\nval s = 2.sp',
                'android/feature/inbox/src/main/kotlin/i/Inbox.kt': 'val b = 3.dp',
                'android/feature/inbox/src/test/kotlin/i/InboxTest.kt': 'val t = 4.dp',
                'android/platform/notify/src/main/kotlin/n/Notify.kt': 'val c = Color.Blue',
                'android/module/voice/src/main/kotlin/v/Voice.kt': 'val v = 6.dp',
                'android/build-logic/src/main/kotlin/Plugin.kt': 'val p = 5.dp',
            }
            for name, text in files.items():
                (root / name).parent.mkdir(parents=True, exist_ok=True)
                (root / name).write_text(text)
            current, wildcards = scan(root)
        self.assertEqual({'android/app/src/main/java/a/App.kt', 'android/core/designsystem/src/main/kotlin/d/Parts.kt',
                          'android/core/hub/src/main/kotlin/h/Hub.kt', 'android/feature/inbox/src/main/kotlin/i/Inbox.kt',
                          'android/platform/notify/src/main/kotlin/n/Notify.kt', 'android/module/voice/src/main/kotlin/v/Voice.kt'},
                         set(current['files']))
        self.assertEqual({'app', 'core/designsystem', 'core/hub', 'feature/inbox', 'module/voice', 'platform/notify'}, set(current['modules']))
        self.assertEqual(1, current['modules']['core/designsystem']['color']['hardcoded'])  # the design system counts too
        self.assertEqual(['android/core/hub/src/main/kotlin/h/Hub.kt'], wildcards)  # the design system may use wildcards


if __name__ == '__main__':
    unittest.main()
