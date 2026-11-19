#!/usr/bin/env python3
"""Count Android literal animation specs per file and reject new ones above a reviewed baseline."""
import argparse
import json
import re
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from android_token_baseline import MODULE_GROUPS, ROOT, code_only  # noqa: E402

BASELINE = ROOT / 'android/core/designsystem/tools/motion-baseline.json'
# Tokens.kt is generated; PersonaTheme.kt defines IosSpring / IosMassSpring / SharedMotion / PersonaMotion adapters.
EXEMPT = {
    'android/core/designsystem/src/main/kotlin/persona/core/designsystem/Tokens.kt',
    'android/core/designsystem/src/main/kotlin/persona/core/designsystem/PersonaTheme.kt',
}
# Names that mark a *parameter* as token-backed (duration / spring numbers), not the whole call.
TOKENISH = re.compile(
    r'Tokens\.Motion|IosSpring\(|IosMassSpring\(|SharedMotion\.|ComposerMotion\.|Motions\.of|'
    r'PersonaTheme\.motion|motion\.(enter|size|quick|standard|count|breathe|state|layout)|'
    r'PageTransitionTokens|sidebarColumnSpec|drawerSpec|columnDragSettleSpring|ColumnDrag\.'
)
TYPE_ARGS = r'(?:<[^<>]*>)?'
# tween / spring / snap call starts (empty, typed, or with args).
TWEEN_CALL = re.compile(rf'\btween\s*{TYPE_ARGS}\s*\(')
SPRING_CALL = re.compile(rf'\bspring\s*{TYPE_ARGS}\s*\(')
SNAP_CALL = re.compile(rf'\bsnap\s*{TYPE_ARGS}\s*\(')
KEYFRAMES = re.compile(rf'\bkeyframes\s*{TYPE_ARGS}\s*[({{]')
ANIMATED_VISIBILITY = re.compile(r'\bAnimatedVisibility\s*\(')
CROSSFADE = re.compile(r'\bCrossfade\s*\(')
ANIMATED_CONTENT = re.compile(r'\bAnimatedContent\s*\(')
ANIMATE_CONTENT_SIZE = re.compile(r'\.animateContentSize\s*\(')
ANIMATE_AS_STATE = re.compile(
    r'\banimate(?:Float|Dp|Int|Offset|Color|Value|Size|IntSize|IntOffset)AsState\s*\('
)
ANIMATE_TO = re.compile(r'\.animateTo\s*\(')
TRANSITION_ANIMATE = re.compile(
    r'\.animate(?:Float|Dp|Int|Offset|Color|Value|Size|IntSize|IntOffset)\s*\('
)
DEFAULT_TRANSITION = re.compile(
    r'\b(?:fadeIn|fadeOut|expandVertically|shrinkVertically|expandHorizontally|shrinkHorizontally|'
    r'slideIn(?:Horizontally|Vertically)?|slideOut(?:Horizontally|Vertically)?|scaleIn|scaleOut)'
    rf'\s*{TYPE_ARGS}\s*\('
)


def matching_group(code, open_idx):
    """Index of the matching ) or } for code[open_idx] in '(', '{'."""
    open_ch = code[open_idx]
    close_ch = ')' if open_ch == '(' else '}'
    depth = 0
    i = open_idx
    while i < len(code):
        ch = code[i]
        if ch == open_ch:
            depth += 1
        elif ch == close_ch:
            depth -= 1
            if depth == 0:
                return i
        i += 1
    return len(code) - 1


def matching_paren(code, open_idx):
    return matching_group(code, open_idx)


def call_head(code, start):
    """Arguments of a '(' call (not counting a trailing trailing-lambda body outside the parens)."""
    open_idx = code.find('(', start)
    if open_idx < 0:
        return ''
    end = matching_paren(code, open_idx)
    return code[open_idx + 1:end]


def split_top_level_args(args):
    """Comma-split call arguments, ignoring commas inside nested () [] {} <> and strings."""
    parts = []
    buf = []
    depth = 0
    angle = 0
    quote = None
    i = 0
    while i < len(args):
        ch = args[i]
        if quote:
            buf.append(ch)
            if ch == '\\' and i + 1 < len(args):
                buf.append(args[i + 1])
                i += 2
                continue
            if ch == quote:
                quote = None
            i += 1
            continue
        if ch in ('"', "'"):
            quote = ch
            buf.append(ch)
        elif ch in '([{':
            depth += 1
            buf.append(ch)
        elif ch in ')]}':
            depth = max(0, depth - 1)
            buf.append(ch)
        elif ch == '<' and depth == 0:
            angle += 1
            buf.append(ch)
        elif ch == '>' and angle:
            angle -= 1
            buf.append(ch)
        elif ch == ',' and depth == 0 and angle == 0:
            parts.append(''.join(buf).strip())
            buf = []
        else:
            buf.append(ch)
        i += 1
    tail = ''.join(buf).strip()
    if tail:
        parts.append(tail)
    return parts


def is_named(arg):
    return bool(re.match(r'^[A-Za-z_][A-Za-z0-9_]*\s*=', arg))


def named_value(arg, name):
    match = re.match(rf'^{name}\s*=\s*(.*)$', arg, re.S)
    return match.group(1).strip() if match else None


def expr_is_tokenish(expr):
    return bool(TOKENISH.search(expr or ''))


def expr_is_numeric_or_spring_const(expr):
    expr = (expr or '').strip()
    if not expr:
        return False
    if re.match(r'^[\d.]+f?$', expr):
        return True
    if re.match(r'^Spring\.\w+', expr):
        return True
    return False


def has_animation_spec_arg(args):
    """True when the call passes an animationSpec (named or a second positional)."""
    if re.search(r'\banimationSpec\s*=', args):
        return True
    positionals = [a for a in split_top_level_args(args) if not is_named(a)]
    return len(positionals) >= 2


def tween_is_literal(args):
    """Literal when the duration is a number (or empty default); token duration skips."""
    parts = split_top_level_args(args)
    if not parts:
        return True  # tween()
    for part in parts:
        for name in ('durationMillis', 'duration'):
            value = named_value(part, name)
            if value is not None:
                return not expr_is_tokenish(value)
        if not is_named(part):
            # First positional is duration.
            return not expr_is_tokenish(part)
    # Only easing= / delayMillis= etc.: Compose default duration — still a literal timing.
    return True


def spring_is_literal(args):
    """Literal when any damping/stiffness value is numeric or Spring.*; both tokenish → skip."""
    parts = split_top_level_args(args)
    if not parts:
        return True  # spring() / spring<Float>()
    damping = stiffness = None
    positionals = []
    for part in parts:
        value = named_value(part, 'dampingRatio')
        if value is not None:
            damping = value
            continue
        value = named_value(part, 'stiffness')
        if value is not None:
            stiffness = value
            continue
        if not is_named(part):
            positionals.append(part)
    if len(positionals) >= 2:
        damping, stiffness = positionals[0], positionals[1]
    elif len(positionals) == 1 and damping is None:
        damping = positionals[0]
    values = [v for v in (damping, stiffness) if v is not None]
    if not values:
        return True  # spring with only visibilityThreshold etc. — treat as default-ish literal
    if all(expr_is_tokenish(v) for v in values):
        return False
    return any(expr_is_numeric_or_spring_const(v) or not expr_is_tokenish(v) for v in values)


def snap_is_literal(args):
    parts = split_top_level_args(args)
    if not parts:
        return True  # snap()
    for part in parts:
        value = named_value(part, 'delayMillis') or (part if not is_named(part) else None)
        if value is None:
            continue
        return not expr_is_tokenish(value)
    return True


def default_transition_is_literal(args):
    """fadeIn() / expandVertically(expandFrom = Top) with no animationSpec."""
    if re.search(r'\banimationSpec\s*=', args):
        return False
    if any(not is_named(a) for a in split_top_level_args(args) if a):
        return False  # positional FiniteAnimationSpec
    return True


def literal_hits(code):
    """Each literal animation spec: (line, kind)."""
    hits = []

    for match in TWEEN_CALL.finditer(code):
        start = match.start()
        open_idx = code.find('(', start)
        args = code[open_idx + 1:matching_paren(code, open_idx)]
        if tween_is_literal(args):
            hits.append((code.count('\n', 0, start) + 1, 'tween'))

    for match in SPRING_CALL.finditer(code):
        start = match.start()
        open_idx = code.find('(', start)
        args = code[open_idx + 1:matching_paren(code, open_idx)]
        if spring_is_literal(args):
            hits.append((code.count('\n', 0, start) + 1, 'spring'))

    for match in SNAP_CALL.finditer(code):
        start = match.start()
        open_idx = code.find('(', start)
        args = code[open_idx + 1:matching_paren(code, open_idx)]
        if snap_is_literal(args):
            hits.append((code.count('\n', 0, start) + 1, 'snap'))

    for match in KEYFRAMES.finditer(code):
        start = match.start()
        hits.append((code.count('\n', 0, start) + 1, 'keyframes'))

    for match in DEFAULT_TRANSITION.finditer(code):
        start = match.start()
        open_idx = code.find('(', start)
        args = code[open_idx + 1:matching_paren(code, open_idx)]
        if default_transition_is_literal(args):
            hits.append((code.count('\n', 0, start) + 1, 'transition-default'))

    for match in ANIMATED_VISIBILITY.finditer(code):
        start = match.start()
        args = call_head(code, start)
        if re.search(r'\b(enter|exit)\s*=', args):
            continue
        hits.append((code.count('\n', 0, start) + 1, 'AnimatedVisibility-default'))

    for match in CROSSFADE.finditer(code):
        start = match.start()
        args = call_head(code, start)
        if re.search(r'\banimationSpec\s*=', args):
            continue
        hits.append((code.count('\n', 0, start) + 1, 'Crossfade-default'))

    for match in ANIMATED_CONTENT.finditer(code):
        start = match.start()
        args = call_head(code, start)
        if re.search(r'\btransitionSpec\s*=', args):
            continue
        hits.append((code.count('\n', 0, start) + 1, 'AnimatedContent-default'))

    for match in ANIMATE_CONTENT_SIZE.finditer(code):
        start = match.start()
        args = call_head(code, start)
        if re.search(r'\banimationSpec\s*=', args):
            continue
        if any(not is_named(a) for a in split_top_level_args(args) if a):
            continue
        hits.append((code.count('\n', 0, start) + 1, 'animateContentSize-default'))

    for match in ANIMATE_AS_STATE.finditer(code):
        start = match.start()
        args = call_head(code, start)
        if has_animation_spec_arg(args):
            continue
        hits.append((code.count('\n', 0, start) + 1, 'animateAsState-default'))

    for match in ANIMATE_TO.finditer(code):
        start = match.start()
        args = call_head(code, start)
        if has_animation_spec_arg(args) or re.search(r'\banimationSpec\s*=', args):
            continue
        # One positional target (and optional named listeners) → default spring.
        positionals = [a for a in split_top_level_args(args) if not is_named(a)]
        if len(positionals) == 1:
            hits.append((code.count('\n', 0, start) + 1, 'animateTo-default'))

    for match in TRANSITION_ANIMATE.finditer(code):
        start = match.start()
        args = call_head(code, start)
        if re.search(r'\btransitionSpec\s*=', args) or re.search(r'\banimationSpec\s*=', args):
            continue
        # Transition.animate*: positional transitionSpec is a trailing-lambda-shaped `{ … }`.
        # InfiniteTransition.animate*: initial, target, animationSpec[, label] — three+ positionals.
        positionals = [a for a in split_top_level_args(args) if not is_named(a)]
        if any(p.lstrip().startswith('{') for p in positionals):
            continue
        if len(positionals) >= 3:
            continue
        if any(re.search(
            r'\b(tween|spring|snap|infiniteRepeatable|keyframes|SharedMotion|ComposerMotion|'
            r'IosSpring|IosMassSpring|Tokens\.Motion)\b', p,
        ) for p in positionals):
            continue
        hits.append((code.count('\n', 0, start) + 1, 'transitionAnimate-default'))

    hits.sort(key=lambda item: (item[0], item[1]))
    return hits


def count(source):
    return len(literal_hits(code_only(source)))


def scan(root):
    files = {}
    for group in MODULE_GROUPS:
        base = root / 'android' / group
        if not base.is_dir():
            continue
        candidates = [base] if (base / 'src').is_dir() else sorted(base.iterdir())
        for module in candidates:
            if not module.is_dir() or not (module / 'src').is_dir():
                continue
            for source_set in sorted((module / 'src').iterdir()):
                if 'test' in source_set.name.lower() or not source_set.is_dir():
                    continue
                for path in sorted(source_set.rglob('*.kt')):
                    name = path.relative_to(root).as_posix()
                    if name in EXEMPT:
                        continue
                    n = count(path.read_text())
                    if n:
                        files[name] = n
    return {'version': 1, 'files': files}


def baseline_of(current):
    return {'version': current['version'], 'files': dict(sorted(current['files'].items()))}


def regressions(current, baseline):
    failures = []
    for path, actual in current['files'].items():
        limit = baseline['files'].get(path, 0)
        if actual > limit:
            failures.append(f'{path}: {actual} literal animation specs, baseline {limit}')
    return failures


def update_raises(current, baseline):
    """Files that would raise the baseline (higher count or a new file). Counts may only go down."""
    rises = []
    for path, actual in sorted(current['files'].items()):
        limit = baseline['files'].get(path)
        if limit is None:
            rises.append(f'{path}: new file with {actual} literal animation specs')
        elif actual > limit:
            rises.append(f'{path}: {actual} literal animation specs, baseline {limit}')
    return rises


def reduction_hint(current, baseline):
    total_now = sum(current['files'].values())
    total_base = sum(baseline['files'].values())
    return (
        f'literal animation specs: {total_now} across {len(current["files"])} files '
        f'(baseline {total_base} across {len(baseline["files"])} files). '
        f'To accept a reviewed reduction: python3 scripts/app/android_motion_baseline.py --update-baseline'
    )


def main():
    parser = argparse.ArgumentParser(
        description='Count Android literal animation specs and reject counts above the baseline')
    parser.add_argument('--root', type=Path, default=ROOT, help='repository root')
    parser.add_argument('--baseline', type=Path, default=BASELINE, help='baseline file')
    parser.add_argument('--report', type=Path, help="also write this run's counts here")
    parser.add_argument('--update-baseline', action='store_true',
                        help='accept a reduction of the current counts and rewrite the baseline '
                             '(refuses any rise or new file)')
    args = parser.parse_args()
    current = scan(args.root)
    payload = json.dumps(current, ensure_ascii=False, indent=2, sort_keys=True) + '\n'
    if args.report:
        args.report.write_text(payload)
    if args.update_baseline:
        if args.baseline.is_file():
            baseline = json.loads(args.baseline.read_text())
            if baseline.get('version') != current['version']:
                parser.error('baseline version does not match; cannot --update-baseline')
            rises = update_raises(current, baseline)
            if rises:
                print('Refusing to raise the motion baseline (counts may only go down):\n' + '\n'.join(rises))
                return 1
        args.baseline.write_text(json.dumps(baseline_of(current), ensure_ascii=False, indent=2, sort_keys=True) + '\n')
        print('motion baseline written')
        return 0
    if not args.baseline.is_file():
        parser.error('no baseline; run --update-baseline first')
    baseline = json.loads(args.baseline.read_text())
    if baseline.get('version') != current['version']:
        parser.error('baseline version does not match')
    print(reduction_hint(current, baseline))
    failures = regressions(current, baseline)
    if failures:
        print('\nAbove the baseline (counts may only go down):\n' + '\n'.join(failures))
        return 1
    print('motion baseline check passed')
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
