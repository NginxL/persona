#!/usr/bin/env python3
"""Count Android token adoption and reject hard-coded values, and Material 3 components in pages, above a reviewed baseline."""
import argparse
import json
import re
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
BASELINE = ROOT / 'android/core/designsystem/tools/token-baseline.json'
CATEGORIES = ('color', 'font', 'dp', 'material3', 'icon')
# Production Kotlin of every app module; build-logic holds Gradle plugins, not app code.
MODULE_GROUPS = ('app', 'core', 'feature', 'module', 'platform')
DESIGN_SYSTEM = 'android/core/designsystem/'
# Generated from spec/tokens, which scripts/assets/gen_tokens.py checks: its literals are the tokens themselves.
GENERATED = {'android/core/designsystem/src/main/kotlin/persona/core/designsystem/Tokens.kt'}
# Material 3 components pages may not use (docs/android/android-design.md section 2.3); the design system wraps them.
# Text, MaterialTheme, Local* and material3.adaptive stay allowed.
MATERIAL3_DENIED = frozenset({
    'Scaffold', 'BottomSheetScaffold', 'TopAppBar', 'CenterAlignedTopAppBar', 'MediumTopAppBar', 'LargeTopAppBar',
    'NavigationBar', 'NavigationBarItem', 'NavigationRail', 'NavigationRailItem',
    'Button', 'OutlinedButton', 'TextButton', 'ElevatedButton', 'FilledTonalButton',
    'IconButton', 'FilledIconButton', 'FilledTonalIconButton', 'OutlinedIconButton', 'IconToggleButton',
    'FloatingActionButton', 'ExtendedFloatingActionButton',
    'Card', 'OutlinedCard', 'ElevatedCard', 'TextField', 'OutlinedTextField',
    'FilterChip', 'AssistChip', 'InputChip', 'SuggestionChip', 'ElevatedFilterChip', 'ElevatedAssistChip',
    'ElevatedSuggestionChip', 'AlertDialog', 'BasicAlertDialog', 'ModalBottomSheet', 'DropdownMenu', 'DropdownMenuItem',
    'Snackbar', 'SnackbarHost', 'Checkbox', 'TriStateCheckbox', 'Switch', 'RadioButton',
    'SegmentedButton', 'SingleChoiceSegmentedButtonRow', 'MultiChoiceSegmentedButtonRow',
})
MATERIAL3_IMPORT = r'^\s*import\s+androidx\.compose\.material3\.(\w+)(?:\s+as\s+(\w+))?\s*$'
MATERIAL3_WILDCARD = r'^\s*import\s+androidx\.compose\.material3(?:\.\w+)*\.\*'
NUMBER = r'(?:0[xX][0-9a-fA-F_]+|\d[\d_]*(?:\.\d[\d_]*)?(?:[eE][+-]?\d+)?)[fFlLuU]*'
PATTERNS = {
    'color': rf'\bColor\s*\(\s*(?:(?:color|red|green|blue|alpha)\s*=\s*)?[+-]?{NUMBER}|\bColor\s*\.\s*(?:Black|White|Red|Green|Blue|Yellow|Cyan|Magenta|Gray|LightGray|DarkGray|Transparent)\b|\b(?:Color\s*\.\s*)?parseColor\s*\(',
    'font': rf'(?<![\w.]){NUMBER}\s*\.\s*sp\b',
    'dp': rf'(?<![\w.]){NUMBER}\s*\.\s*dp\b',
}
TOKEN_PATTERNS = {
    'color': r'\b(?:Tokens\.(?:Common|Status|System|Semantic)|(?:PersonaTheme|PersonaTheme)\.(?:colors|palette)|MaterialTheme\.colorScheme)\.[\w.]+',
    'font': r'\b(?:Tokens\.Type(?:Glass)?|(?:PersonaTypography|PersonaTypography)|(?:PersonaTheme|PersonaTheme)\.type|MaterialTheme\.typography)\.[\w.]+',
    'dp': r'\b(?:Tokens\.(?:Space|Size|Radius|MinTouch|Shape|Surface)|(?:PersonaTheme|PersonaTheme)\.shapes|MaterialTheme\.shapes)\.[\w.]+',
}


def code_only(source):
    # Keep offsets/newlines stable; Kotlin supports nested block comments.
    out = list(source)
    i = 0
    while i < len(source):
        start = i
        if source.startswith('//', i):
            end = source.find('\n', i)
            i = len(source) if end < 0 else end
        elif source.startswith('/*', i):
            i += 2
            depth = 1
            while i < len(source) and depth:
                if source.startswith('/*', i):
                    depth += 1
                    i += 2
                elif source.startswith('*/', i):
                    depth -= 1
                    i += 2
                else:
                    i += 1
        elif source.startswith('"""', i):
            end = source.find('"""', i + 3)
            i = len(source) if end < 0 else end + 3
        elif source[i] in '\"\'':
            quote = source[i]
            i += 1
            while i < len(source):
                if source[i] == '\\':
                    i += 2
                elif source[i] == quote:
                    i += 1
                    break
                else:
                    i += 1
        else:
            i += 1
            continue
        for pos in range(start, min(i, len(source))):
            if out[pos] != '\n':
                out[pos] = ' '
    return ''.join(out)


def material3_names(code):
    """The denied Material 3 components a file can call, by the names it calls them."""
    names = set(MATERIAL3_DENIED) if re.search(r'^\s*import\s+androidx\.compose\.material3\.\*', code, re.M) else set()
    for name, alias in re.findall(MATERIAL3_IMPORT, code, re.M):
        if name in MATERIAL3_DENIED:
            names.add(alias or name)
    return names


def count(source, pages=True):
    """Counts per category; `pages` is False in the design system, where Material 3 components are allowed."""
    code = code_only(source)
    imports = re.findall(r'^\s*import\s+([\w.]+)(?:\s+as\s+(\w+))?\s*$', code, re.M)
    denied = material3_names(code) if pages else set()
    code = re.sub(r'^\s*(?:import|package)\s+[^\n]+', '', code, flags=re.M)
    # Expand explicit member imports and aliases before applying the same rules.
    for path, alias in imports:
        name = alias or path.rsplit('.', 1)[-1]
        if 'designsystem.' in path:
            replacement = path.split('designsystem.', 1)[1]
        elif '.Color' in path:
            replacement = 'Color' + path.split('.Color', 1)[1]
        elif alias and path.endswith(('.dp', '.sp')):
            replacement = path.rsplit('.', 1)[-1]
        else:
            continue
        if name != replacement:
            code = re.sub(rf'(?<![\w.]){re.escape(name)}\b', replacement, code)
    code = re.sub(r'\s*\.\s*', '.', code)
    counts = {category: {
        'hardcoded': len(re.findall(PATTERNS[category], code)),
        'token': len(re.findall(TOKEN_PATTERNS[category], code)),
    } for category in ('color', 'font', 'dp')}
    # A call or a trailing lambda, by its imported name or written out in full; design-system components are the tokens.
    calls = len(re.findall(rf'(?<![\w.])(?:{"|".join(map(re.escape, sorted(denied)))})(?=\s*[({{])', code)) if denied else 0
    calls += 0 if not pages else len(re.findall(
        rf'\bandroidx\.compose\.material3\.(?:{"|".join(sorted(MATERIAL3_DENIED))})(?=\s*[({{])', code))
    counts['material3'] = {'hardcoded': calls, 'token': 0}
    # Icons by meaning (#4209): pages name what an icon means (PersonaIcons), not which glyph draws it (PhIcons).
    counts['icon'] = {'hardcoded': len(re.findall(r'\bPhIcons\.[A-Z]\w*', code)) if pages else 0,
                      'token': len(re.findall(r'\bPersonaIcons\.\w+', code))}
    return counts


def wildcard_material3(source):
    """Wildcard Material 3 imports hide which components a page uses; only the design system may write them."""
    return len(re.findall(MATERIAL3_WILDCARD, code_only(source), re.M))


def scan(root):
    files = {}
    modules = {}
    wildcards = []
    for group in MODULE_GROUPS:
        base = root / 'android' / group
        if not base.is_dir():
            continue
        candidates = [base] if (base / 'src').is_dir() else sorted(base.iterdir())
        for module in candidates:
            if not module.is_dir() or not (module / 'src').is_dir():
                continue
            totals = {category: {'hardcoded': 0, 'token': 0} for category in CATEGORIES}
            for source_set in sorted((module / 'src').iterdir()):
                if 'test' in source_set.name.lower() or not source_set.is_dir():
                    continue
                for path in sorted(source_set.rglob('*.kt')):
                    name = path.relative_to(root).as_posix()
                    if name in GENERATED:
                        continue
                    source = path.read_text()
                    pages = not name.startswith(DESIGN_SYSTEM)
                    if pages and wildcard_material3(source):
                        wildcards.append(name)
                    counts = count(source, pages)
                    files[name] = counts
                    for category in CATEGORIES:
                        for kind in ('hardcoded', 'token'):
                            totals[category][kind] += counts[category][kind]
            for counts in totals.values():
                total = counts['token'] + counts['hardcoded']
                counts['token_ratio'] = round(counts['token'] / total, 6) if total else None
            modules[module.relative_to(root / 'android').as_posix()] = totals
    if not files:
        raise ValueError('no Android Kotlin sources found; check the repository root')
    return {'version': 2, 'files': files, 'modules': modules}, wildcards


def baseline_of(current):
    """What the baseline keeps: each file's nonzero hard-coded counts (a missing file or category is zero)."""
    files = {}
    for path, counts in current['files'].items():
        limits = {category: counts[category]['hardcoded'] for category in CATEGORIES if counts[category]['hardcoded']}
        if limits:
            files[path] = limits
    return {'version': current['version'], 'files': files}


def regressions(current, baseline):
    failures = []
    for path, counts in current['files'].items():
        for category in CATEGORIES:
            limit = baseline['files'].get(path, {}).get(category, 0)
            actual = counts[category]['hardcoded']
            if actual > limit:
                failures.append(f'{path}: {actual} hard-coded {category}, baseline {limit}')
    return failures


def lowerable(current, baseline):
    """Files under the baseline in some category: they pass, and lowering the baseline for them is tidying."""
    lower = []
    for path, limits in sorted(baseline['files'].items()):
        counts = current['files'].get(path)
        for category, limit in sorted(limits.items()):
            actual = counts[category]['hardcoded'] if counts and category in counts else 0
            if actual < limit:
                lower.append(f'{path}: {actual} hard-coded {category}, baseline {limit}')
    return lower


def main():
    parser = argparse.ArgumentParser(description='Count Android design-token adoption and reject hard-coded values above the baseline')
    parser.add_argument('--root', type=Path, default=ROOT, help='repository root')
    parser.add_argument('--baseline', type=Path, default=BASELINE, help='baseline file')
    parser.add_argument('--report', type=Path, help='also write this run\'s counts here')
    parser.add_argument('--update-baseline', action='store_true', help='accept the current counts and rewrite the baseline')
    args = parser.parse_args()
    current, wildcards = scan(args.root)
    payload = json.dumps(current, ensure_ascii=False, indent=2, sort_keys=True) + '\n'
    if args.report:
        args.report.write_text(payload)
    if wildcards:
        # Not a baseline: there is nothing to keep, and the baseline must not accept one.
        print('Wildcard Material 3 imports outside the design system (import each name):\n' + '\n'.join(wildcards))
        return 1
    if args.update_baseline:
        args.baseline.write_text(json.dumps(baseline_of(current), ensure_ascii=False, indent=2, sort_keys=True) + '\n')
        print('token baseline written')
        return 0
    if not args.baseline.is_file():
        parser.error('no baseline; run --update-baseline first')
    baseline = json.loads(args.baseline.read_text())
    if baseline.get('version') != current['version']:
        parser.error('baseline version does not match')
    for module, counts in current['modules'].items():
        parts = []
        for category, values in counts.items():
            if category == 'material3':
                parts.append(f"material3 components {values['hardcoded']}")
                continue
            if category == 'icon':
                parts.append(f"icons by glyph {values['hardcoded']} / by meaning {values['token']}")
                continue
            ratio = values['token_ratio']
            rate = 'unused' if ratio is None else f'{ratio:.1%}'
            parts.append(f"{category} {rate} (token {values['token']} / hard-coded {values['hardcoded']})")
        print(f"{module}: {', '.join(parts)}")
    failures = regressions(current, baseline)
    if failures:
        print('\nAbove the baseline:\n' + '\n'.join(failures))
        return 1
    lower = lowerable(current, baseline)
    if lower:
        # Only a rise fails (docs/operations/testing.md, "Count ratchets"); a count under its baseline is a notice.
        print(f'\n{len(lower)} count(s) under the baseline; lowering it is tidying anyone can do, not this change\'s job: '
              'python3 scripts/ci/lower_baselines.py')
    print('token baseline check passed')
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
