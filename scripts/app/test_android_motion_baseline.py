import io
import json
import tempfile
import unittest
from contextlib import redirect_stdout
from pathlib import Path
from unittest import mock

from android_motion_baseline import (
    baseline_of,
    count,
    literal_hits,
    main,
    regressions,
    scan,
    update_raises,
)
from android_token_baseline import code_only


class MotionBaselineTest(unittest.TestCase):
    def kinds(self, source):
        return [kind for _, kind in literal_hits(code_only(source))]

    def test_counts_numeric_tween_spring_and_default_visibility(self):
        source = '''
            val a = tween(1100, easing = LinearEasing)
            val b = tween(Tokens.Motion.State.duration)
            val c = spring(dampingRatio = Spring.DampingRatioNoBouncy, stiffness = Spring.StiffnessMediumLow)
            val d = IosSpring(300, 0.25f).spec<Float>()
            val e = SharedMotion.reveal<Float>()
            AnimatedVisibility(visible = open) { Text("x") }
            AnimatedVisibility(visible = open, enter = fadeIn(SharedMotion.reveal()), exit = fadeOut(SharedMotion.reveal())) { Text("y") }
            Crossfade(targetState = tab) { Text(it) }
            Crossfade(targetState = tab, animationSpec = tween(Tokens.Motion.Reveal.duration)) { Text(it) }
            snap()
            snap(0)
        '''
        kinds = self.kinds(source)
        self.assertEqual(
            ['tween', 'spring', 'AnimatedVisibility-default', 'Crossfade-default', 'snap', 'snap'],
            kinds,
        )

    def test_counts_generic_tween_spring_snap_type_args(self):
        source = '''
            val a = tween<Float>(900)
            val b = tween<IntOffset>(durationMillis = 200)
            val c = spring<Float>(dampingRatio = 0.5f, stiffness = 400f)
            val d = snap<Float>(0)
            val e = tween(Tokens.Motion.Reveal.duration)
            val f = spring<Float>(dampingRatio = Tokens.Motion.State.bounce, stiffness = Tokens.Motion.State.stiffness)
        '''
        self.assertEqual(['tween', 'tween', 'spring', 'snap'], self.kinds(source))

    def test_counts_durationMillis_named_arg(self):
        self.assertEqual(['tween', 'tween'], self.kinds(
            'val a = tween(durationMillis = 200)\nval b = tween(duration = 180)\n'))

    def test_counts_keyframes_block_forms(self):
        source = '''
            val a = keyframes { durationMillis = 300; 0f at 0; 1f at 300 }
            val b = keyframes<Float> { durationMillis = 200; 0f at 0 }
            val c = keyframes(300) { 0f at 0 }
        '''
        self.assertEqual(['keyframes', 'keyframes', 'keyframes'], self.kinds(source))

    def test_counts_empty_and_positional_spring_tween_snap(self):
        source = '''
            val a = spring<Float>()
            val b = spring(0.8f, 400f)
            val c = tween()
            val d = snap()
        '''
        self.assertEqual(['spring', 'spring', 'tween', 'snap'], self.kinds(source))

    def test_token_easing_does_not_exempt_literal_duration(self):
        source = 'val a = tween<Float>(900, easing = ComposerMotion.swapEasing)\n'
        self.assertEqual(['tween'], self.kinds(source))

    def test_counts_animateTo_default_and_transition_animateFloat(self):
        source = '''
            LaunchedEffect(Unit) { anim.animateTo(1f) }
            LaunchedEffect(Unit) { anim.animateTo(1f, SharedMotion.reveal()) }
            val t = updateTransition(state, label = "x")
            val a by t.animateFloat(label = "a") { if (it) 1f else 0f }
            val b by t.animateFloat(transitionSpec = { tween(Tokens.Motion.State.duration) }, label = "b") { 1f }
            val c by t.animateFloat({ ComposerMotion.swap() }, "scale") { 1f }
            val d by rememberInfiniteTransition().animateFloat(0f, 1f, infiniteRepeatable(tween(500)), label = "wave")
        '''
        kinds = self.kinds(source)
        self.assertEqual(['animateTo-default', 'transitionAnimate-default', 'tween'], kinds)

    def test_counts_default_enter_exit_transitions(self):
        source = '''
            AnimatedVisibility(visible = open, enter = fadeIn(), exit = fadeOut()) { Text("x") }
            AnimatedVisibility(visible = open, enter = fadeIn(SharedMotion.reveal()) + expandVertically(SharedMotion.reveal()),
                exit = fadeOut(SharedMotion.reveal())) { Text("y") }
            val e = expandVertically(expandFrom = Alignment.Top)
            val f = scaleIn()
        '''
        kinds = self.kinds(source)
        self.assertIn('transition-default', kinds)
        self.assertEqual(4, kinds.count('transition-default'))  # fadeIn, fadeOut, expandVertically(expandFrom), scaleIn
        self.assertNotIn('AnimatedVisibility-default', kinds)

    def test_call_head_sees_enter_after_modifier_lambda(self):
        source = '''
            AnimatedVisibility(
                visible = open,
                modifier = Modifier.clickable { open = !open },
                enter = fadeIn(SharedMotion.reveal()),
                exit = fadeOut(SharedMotion.reveal()),
            ) { Text("x") }
            Crossfade(targetState = tab, modifier = Modifier.clickable { }, animationSpec = tween(Tokens.Motion.Reveal.duration)) { Text(it) }
        '''
        kinds = self.kinds(source)
        self.assertNotIn('AnimatedVisibility-default', kinds)
        self.assertNotIn('Crossfade-default', kinds)

    def test_counts_animateAsState_without_animationSpec(self):
        source = '''
            val a by animateFloatAsState(if (pressed) 0.96f else 1f, label = "press")
            val b by animateDpAsState(if (on) 10.dp else 0.dp, label = "thumb")
            val c by animateFloatAsState(target, tween(150), label = "key")
            val d by animateFloatAsState(target, spec, label = "amber")
            val e by animateFloatAsState(target, animationSpec = SharedMotion.reveal(), label = "x")
        '''
        self.assertEqual(['animateAsState-default', 'animateAsState-default', 'tween'], self.kinds(source))

    def test_counts_animateContentSize_without_spec(self):
        source = '''
            Modifier.animateContentSize()
            Modifier.animateContentSize(alignment = Alignment.Top)
            Modifier.animateContentSize(PersonaTheme.motion.size())
            Modifier.animateContentSize(animationSpec = SharedMotion.reveal())
        '''
        self.assertEqual(['animateContentSize-default', 'animateContentSize-default'], self.kinds(source))

    def test_counts_animatedContent_without_transitionSpec(self):
        source = '''
            AnimatedContent(targetState = tab) { Text(it) }
            AnimatedContent(
                targetState = action,
                transitionSpec = { fadeIn() togetherWith fadeOut() },
                label = "composer-action",
            ) { shown -> Text("$shown") }
        '''
        # AnimatedContent default + fadeIn/fadeOut inside the provided transitionSpec body still count as transitions.
        kinds = self.kinds(source)
        self.assertIn('AnimatedContent-default', kinds)
        self.assertEqual(1, kinds.count('AnimatedContent-default'))

    def test_new_file_or_rise_fails(self):
        baseline = baseline_of({'version': 1, 'files': {'a.kt': 2}})
        self.assertEqual(1, len(regressions({'files': {'a.kt': 3}}, baseline)))
        self.assertEqual(1, len(regressions({'files': {'b.kt': 1}}, baseline)))
        self.assertEqual([], regressions({'files': {'a.kt': 1}}, baseline))

    def test_update_baseline_refuses_raise_or_new_file(self):
        baseline = {'version': 1, 'files': {'a.kt': 2}}
        self.assertEqual(
            ['a.kt: 3 literal animation specs, baseline 2'],
            update_raises({'files': {'a.kt': 3}}, baseline),
        )
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            base = root / 'baseline.json'
            base.write_text('{"version": 1, "files": {"android/app/src/main/java/a/App.kt": 1}}\n')
            (root / 'android/app/src/main/java/a').mkdir(parents=True)
            (root / 'android/app/src/main/java/a/App.kt').write_text('val a = tween(180)\nval b = tween(200)\n')
            with mock.patch('sys.argv', ['android_motion_baseline.py', '--root', str(root),
                                         '--baseline', str(base), '--update-baseline']):
                buf = io.StringIO()
                with redirect_stdout(buf):
                    code = main()
            self.assertEqual(1, code)
            self.assertIn('Refusing to raise', buf.getvalue())

    def test_update_baseline_rejects_version_mismatch(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            base = root / 'baseline.json'
            base.write_text('{"version": 0, "files": {}}\n')
            (root / 'android/app/src/main/java/a').mkdir(parents=True)
            (root / 'android/app/src/main/java/a/App.kt').write_text('val a = tween(180)\n')
            with mock.patch('sys.argv', ['android_motion_baseline.py', '--root', str(root),
                                         '--baseline', str(base), '--update-baseline']):
                with self.assertRaises(SystemExit):
                    main()

    def test_update_baseline_accepts_reduction(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            base = root / 'baseline.json'
            base.write_text('{"version": 1, "files": {"android/app/src/main/java/a/App.kt": 2}}\n')
            (root / 'android/app/src/main/java/a').mkdir(parents=True)
            (root / 'android/app/src/main/java/a/App.kt').write_text('val a = tween(180)\n')
            with mock.patch('sys.argv', ['android_motion_baseline.py', '--root', str(root),
                                         '--baseline', str(base), '--update-baseline']):
                buf = io.StringIO()
                with redirect_stdout(buf):
                    code = main()
            self.assertEqual(0, code)
            self.assertEqual(1, json.loads(base.read_text())['files'][
                'android/app/src/main/java/a/App.kt'])

    def test_scan_covers_main_sources_not_tests_or_tokens(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            files = {
                'android/app/src/main/java/a/App.kt': 'val a = tween(180)',
                'android/core/designsystem/src/main/kotlin/d/Parts.kt': 'val c = tween<Float>(900)',
                'android/core/designsystem/src/main/kotlin/persona/core/designsystem/Tokens.kt': 'val c = tween(1)',
                'android/feature/inbox/src/main/kotlin/i/Inbox.kt': 'AnimatedVisibility(visible = true) {}',
                'android/feature/inbox/src/test/kotlin/i/InboxTest.kt': 'val t = tween(500)',
            }
            for name, text in files.items():
                (root / name).parent.mkdir(parents=True, exist_ok=True)
                (root / name).write_text(text)
            current = scan(root)
        self.assertEqual(
            {
                'android/app/src/main/java/a/App.kt': 1,
                'android/core/designsystem/src/main/kotlin/d/Parts.kt': 1,
                'android/feature/inbox/src/main/kotlin/i/Inbox.kt': 1,
            },
            current['files'],
        )


if __name__ == '__main__':
    unittest.main()
