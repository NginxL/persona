#!/usr/bin/env python3
"""Check the Android app's translations (docs/protocol/i18n.md).

- Every language in android/i18n/*.properties has complete resources in each module, matching keys and placeholders,
  plurals with the quantities their language needs, and string arrays that match the fallback's length and each item's
  placeholders. values/ is English, so its translatable text has no Chinese or Japanese.
- String keys start with the module's name (`settings_…`), since all modules' resources merge into one table.
- Kotlin code and XML outside the translated values directories (and outside tests and the design system's debug catalog)
  only lose hardcoded Chinese / Japanese text: each file may hold
  no more than its module's i18n-baseline.json records (one file per module, so modules move in parallel). A literal that is not UI text (a hub token it matches, a prompt
  sent to the model) says why on its line with `// i18n-ignore: <reason>`.
- The same Kotlin files also lose hardcoded English text where Compose shows it: a literal passed as `Text("…")`, as a
  `contentDescription`, or as a String parameter named text / title / label / placeholder / name … of most composables
  declared in android/ (`JButton("…")`, `JTextField(v, f, label = "…")`; an animation's `label` is not one). Calls are
  matched by name, so a class or function named like a composable can be flagged too: mark that line `// i18n-ignore`.
  When such an argument is an `if` / `else`, `when` or elvis expression, its literal branches count the same
  (`Text(a ?: "…")`, `Text(if (x) "…" else "…")`, `title = when (k) { 1 -> "…" else -> "…" }`); a literal inside a
  function whose result is passed, a lambda or a `{ … }` branch block is not followed.
  Only literals that read as words count (no identifiers, keys, URLs, hosts, formats, acronyms or product names such as
  Persona); `// i18n-ignore` works the same way. Counts live in each module's i18n-english-baseline.json and every
  recorded line is listed, so it can be moved into strings.xml later.

  python3 scripts/app/android_i18n_check.py            # check
  python3 scripts/app/android_i18n_check.py --update   # record the lower counts (even while another file is over)
"""
import argparse
import json
import re
import sys
import xml.etree.ElementTree as ET
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
ANDROID = ROOT / 'android'
BASELINE = 'i18n-baseline.json'
ENGLISH_BASELINE = 'i18n-english-baseline.json'
# Chinese, Japanese kana, and full-width punctuation.
CJK = re.compile(r'[　-〿぀-ヿ㐀-䶿一-鿿＀-￯]')
PLACEHOLDER = re.compile(r'%(?:\d+\$)?[-#+ 0,(]*\d*(?:\.\d+)?[sdfxXc%]')
IGNORE = 'i18n-ignore'
SKIP_DIRS = {'build', '.gradle', 'generated'}


def languages(root=ANDROID):
    """Resource directory -> metadata, discovered from i18n/<BCP47 tag>.properties (also read by Gradle)."""
    out = {}
    for file in sorted((root / 'i18n').glob('*.properties')):
        meta = dict(line.split('=', 1) for line in file.read_text(encoding='utf-8').splitlines()
                    if line.strip() and not line.startswith('#'))
        meta['tag'] = file.stem
        meta['matches'] = meta.get('matches', '').split(',')
        meta['plurals'] = set(meta.get('plurals', '').split(','))
        out[meta.get('resources', '')] = meta
    return out


def language_problems(root=ANDROID):
    """Every language has usable metadata; no subtag or resources directory is claimed twice."""
    problems, claimed, resources = [], set(), set()
    files = sorted((root / 'i18n').glob('*.properties'))
    if not {'en', 'zh-Hans', 'ja'} <= {p.stem for p in files}:
        problems.append('i18n/: source, fallback, and Japanese language resources are required')
    for file in files:
        try:
            meta = dict(line.split('=', 1) for line in file.read_text(encoding='utf-8').splitlines()
                        if line.strip() and not line.startswith('#'))
            tag = file.stem
            if not re.fullmatch(r'[a-z]{2,3}(?:-[A-Za-z0-9]{2,8})*', tag):
                problems.append(f'i18n/{file.name}: invalid BCP 47 tag')
            if not meta.get('name', '').strip():
                problems.append(f'i18n/{file.name}: name is empty')
            directory = meta.get('resources', '')
            if directory in resources or not re.fullmatch(r'values(?:-[a-z]{2,3}|-b\+[A-Za-z0-9+]+)?', directory):
                problems.append(f'i18n/{file.name}: duplicate or invalid resources directory {directory}')
            resources.add(directory)
            if (tag == 'en') != (directory == 'values'):
                problems.append(f'i18n/{file.name}: only en uses the values/ fallback')
            matches = meta.get('matches', '').split(',')
            if tag.split('-')[0] not in matches:
                problems.append(f'i18n/{file.name}: matches must include its own language subtag')
            for subtag in matches:
                if not re.fullmatch(r'[a-z]{2,3}', subtag) or subtag in claimed:
                    problems.append(f'i18n/{file.name}: duplicate or invalid match {subtag}')
                claimed.add(subtag)
            plural = set(meta.get('plurals', '').split(','))
            if not 'other' in plural or not plural <= {'zero', 'one', 'two', 'few', 'many', 'other'}:
                problems.append(f'i18n/{file.name}: invalid CLDR plural quantities')
        except ValueError:
            problems.append(f'i18n/{file.name}: expected one key=value per line')
    return problems


def modules(root=ANDROID):
    """Each Gradle module: the directory holding build.gradle.kts and a src/."""
    for build in sorted(root.glob('**/build.gradle.kts')):
        directory = build.parent
        if directory == root or 'build-logic' in directory.parts or not (directory / 'src').is_dir():
            continue
        if any(part in SKIP_DIRS for part in directory.relative_to(root).parts):
            continue
        yield directory


def prefix(module, root=ANDROID):
    return module.name.replace('-', '_') + '_'


def strings(directory):
    """name -> (kind, text or {quantity: text}, translatable) for every strings*.xml in a values directory."""
    found = {}
    for path in sorted(directory.glob('strings*.xml')):
        for element in ET.parse(path).getroot():
            name = element.get('name')
            if name is None:
                continue
            translatable = element.get('translatable') != 'false'
            if element.tag == 'string':
                found[name] = ('string', ''.join(element.itertext()), translatable)
            elif element.tag == 'plurals':
                found[name] = ('plurals', {item.get('quantity'): ''.join(item.itertext()) for item in element}, translatable)
            elif element.tag == 'string-array':
                found[name] = ('array', [''.join(item.itertext()) for item in element], translatable)
    return found


def placeholders(value):
    texts = value.values() if isinstance(value, dict) else value if isinstance(value, list) else [value]
    return sorted({p for text in texts for p in PLACEHOLDER.findall(text) if p != '%%'})


def resource_problems(module, root=ANDROID):
    problems = []
    res = module / 'src/main/res'
    if not res.is_dir():
        return problems
    name = module.relative_to(root).as_posix()
    metadata = languages(root)
    locales = ['values'] + sorted(d for d in metadata if d != 'values')
    plural = {d: m['plurals'] for d, m in metadata.items()}
    sets = {locale: strings(res / locale) for locale in locales}
    if not any(sets.values()):
        return problems
    for directory in res.glob('values-*'):
        if directory.name not in metadata and list(directory.glob('strings*.xml')):
            problems.append(f'{name}: {directory.name}/ has strings but no i18n language metadata')
    base = sets['values']
    for key, (kind, value, translatable) in base.items():
        if not key.startswith(prefix(module)):
            problems.append(f'{name}: string "{key}" should start with "{prefix(module)}"')
        if translatable and any(CJK.search(text) for text in (value.values() if isinstance(value, dict) else [value] if isinstance(value, str) else value)):
            problems.append(f'{name}: values/ is the English fallback, but "{key}" is not English')
        if kind == 'plurals' and not plural['values'] <= set(value):
            problems.append(f'{name}: plurals "{key}" in values/ needs {sorted(plural["values"])}')
    wanted = {key for key, (_, _, translatable) in base.items() if translatable}
    for locale in locales[1:]:
        have = sets[locale]
        for key in sorted(wanted - have.keys()):
            problems.append(f'{name}: "{key}" is missing from {locale}/')
        for key in sorted(have.keys() - wanted):
            why = 'is not translatable' if key in base else 'is not in values/'
            problems.append(f'{name}: "{key}" in {locale}/ {why}')
        for key in sorted(wanted & have.keys()):
            kind, value, _ = base[key]
            other_kind, other, _ = have[key]
            if kind != other_kind:
                problems.append(f'{name}: "{key}" is a {kind} in values/ but a {other_kind} in {locale}/')
                continue
            if kind == 'array':
                if len(value) != len(other):
                    problems.append(f'{name}: string-array "{key}" has {len(value)} items in values/ '
                                    f'but {len(other)} in {locale}/')
                    continue
                for index, (original, translated) in enumerate(zip(value, other), 1):
                    expected, found = placeholders(original), placeholders(translated)
                    if found != expected:
                        problems.append(f'{name}: string-array "{key}" item {index} has placeholders {expected} '
                                        f'in values/ but {found} in {locale}/')
                continue
            if placeholders(value) != placeholders(other):
                problems.append(f'{name}: "{key}" has placeholders {placeholders(value)} in values/ but {placeholders(other)} in {locale}/')
            if kind == 'plurals' and not plural[locale] <= set(other):
                problems.append(f'{name}: plurals "{key}" in {locale}/ needs {sorted(plural[locale])}')
    return problems


def literals(source):
    """The string literals of Kotlin [source] that are code (not comments), with their line numbers."""
    found = []
    i, line = 0, 1
    n = len(source)
    while i < n:
        c = source[i]
        if c == '\n':
            line += 1
            i += 1
        elif source.startswith('//', i):
            end = source.find('\n', i)
            i = n if end < 0 else end
        elif source.startswith('/*', i):
            depth, i = 1, i + 2
            while i < n and depth:
                if source.startswith('/*', i):
                    depth, i = depth + 1, i + 2
                elif source.startswith('*/', i):
                    depth, i = depth - 1, i + 2
                else:
                    line += source[i] == '\n'
                    i += 1
        elif source.startswith('"""', i):
            end = source.find('"""', i + 3)
            end = n if end < 0 else end + 3
            found.append((line, source[i:end]))
            line += source.count('\n', i, end)
            i = end
        elif c == '"':
            start, i = i, i + 1
            while i < n and source[i] not in '"\n':
                i += 2 if source[i] == '\\' else 1
            found.append((line, source[start:i + 1]))
            i += 1
        elif c == "'":
            # A character literal (a single CJK character in quotes), or an apostrophe of nothing in particular: skip a short one.
            end = source.find("'", i + 1)
            i = end + 1 if 0 < end - i <= 8 else i + 1
        else:
            i += 1
    return found


def kotlin_count(path):
    text = path.read_text(encoding='utf-8')
    lines = text.split('\n')
    hits = set()
    for line, literal in literals(text):
        if CJK.search(literal) and IGNORE not in lines[line - 1]:
            hits.add(line)
    return len(hits)


# Where Compose shows a string. A composable declared in android/ shows its String parameters with these names (by
# position or by name; `label` of an animation call is not a composable's String parameter) ...
TEXT_NAMES = {'text', 'title', 'subtitle', 'label', 'placeholder', 'supportingText', 'description', 'actionLabel', 'summary',
              'contentDescription', 'name', 'message', 'hint', 'caption', 'heading', 'header', 'detail'}
# ... and so do these calls from libraries, or design-system data classes, at the given positions.
SHOWN_AT = {'Text': {0}, 'BasicText': {0}, 'Icon': {1}, 'Image': {1}, 'JAction': {0}, 'JSegment': {0}, 'JStat': {0, 2}}
SHOWN_NAMED = {'Text': {'text'}, 'BasicText': {'text'}}
# Product and brand names shown as they are in every language (docs/protocol/i18n.md, "Glossary").
BRANDS = re.compile(r"\b(?:Persona Link|Persona|Agents|Claude Code|Claude|Codex|GitHub|Gmail|Google|Feishu|Lark|WeChat|"
                    r"Discord|Android|Apple|Orca|Paseo|Markdown|Git)\b")
IDENTIFIER = re.compile(r'[A-Za-z_][A-Za-z0-9_]*')
# A `$name` template: Kotlin names may use any Unicode letter (`"合计 $金额"`).
TEMPLATE_NAME = re.compile(r'[^\W\d]\w*')


def _template(source, i):
    """End of the `${…}` template expression starting at source[i] (the `$`)."""
    depth, i, n = 1, i + 2, len(source)
    while i < n and depth:
        c = source[i]
        if c == '"':
            i = _string(source, i)[0]
            continue
        depth += {'{': 1, '}': -1}.get(c, 0)
        i += 1
    return i


def _string(source, i):
    """(end, text) of the string literal at source[i]: text with templates and escapes blanked; None for a raw string."""
    n = len(source)
    if source.startswith('"""', i):
        end = source.find('"""', i + 3)
        return (n if end < 0 else end + 3), None
    out, i = [], i + 1
    while i < n and source[i] not in '"\n':
        c = source[i]
        if c == '\\':
            out.append(' ')
            i += 2
        elif source.startswith('${', i):
            out.append(' ')
            i = _template(source, i)
        elif c == '$' and TEMPLATE_NAME.match(source, i + 1):
            out.append(' ')
            i += 1 + len(TEMPLATE_NAME.match(source, i + 1).group())
        else:
            out.append(c)
            i += 1
    return i + 1, ''.join(out)


class _Frame:
    """An open `(` / `{` / `[` while scanning: the call it belongs to, the current argument (or statement) and, for a
    `when (…) { … }` that is a whole argument, that argument's (call, name or position) and its branch literals."""
    __slots__ = ('call', 'opener', 'index', 'arg', 'prev', 'where', 'when', 'pending')

    def __init__(self, call, opener, where, arg):
        self.call, self.opener, self.where, self.index = call, opener, where, 0
        self.arg = self.prev = arg
        self.when, self.pending = None, []


ENDS = ',)}\n;'


def _argument(top, segment):
    """(call, name or position, expression) of the argument whose text so far is [segment] in frame [top], or None
    when it is not an argument of a call or a `name = …` (a declaration's default value is neither)."""
    if top is None:
        return None
    named = re.match(r'\s*([A-Za-z_]\w*)\s*=(?!=)(.*)\Z', segment, re.S)
    call = top.call if top.opener == '(' else None
    if named:
        return call, named.group(1), named.group(2)
    if call and not re.match(r'\s*(?:\w+\s+)*\w+\s*:(?!:)', segment):
        return call, top.index, segment
    return None


def _branch(source, start, before, after, top, closed):
    """(call, argument) when the literal at source[start] is a result of the whole argument's `if` / `else` or elvis
    expression: `Text(a ?: "…")`, `Text(if (x) "…" else "…")`."""
    if top is None:
        return None
    segment = source[top.arg:start]
    if top.opener == '{' and segment.lstrip().startswith('?:'):
        segment = source[top.prev:start]  # an elvis continued on the next line
    found = _argument(top, segment)
    if not found:
        return None
    call, argument, expression = found
    ends = after[0] in ENDS or re.match(r'else\b', after)
    if before.endswith('?:'):
        return (call, argument) if ends else None
    if not re.match(r'\s*if\s*\(', expression):
        return None
    after_if = closed and closed[0] == 'if' and not source[closed[1]:start].strip()
    if after_if or re.search(r'\belse$', before):
        return (call, argument) if ends else None
    return None


def ui_literals(source):
    """(line, text, call, argument, source) for each string literal of Kotlin [source] that is a whole argument, or a
    branch of an argument's `if` / `else`, `when` or elvis expression: `call` is the innermost call it is passed to
    (None outside one), `argument` its parameter name or position (an int), or the name assigned to; `text` has
    templates and escapes blanked."""
    found, stack = [], []
    closed = None  # (name, end) of the last `(…)` closed: `if (…)` / `when (…)`
    i, line, n = 0, 1, len(source)
    while i < n:
        c = source[i]
        if c == '\n':
            line += 1
            if stack and stack[-1].opener == '{':
                stack[-1].prev, stack[-1].arg = stack[-1].arg, i + 1
            i += 1
        elif source.startswith('//', i):
            end = source.find('\n', i)
            i = n if end < 0 else end
        elif source.startswith('/*', i):
            depth, i = 1, i + 2
            while i < n and depth:
                if source.startswith('/*', i):
                    depth, i = depth + 1, i + 2
                elif source.startswith('*/', i):
                    depth, i = depth - 1, i + 2
                else:
                    line += source[i] == '\n'
                    i += 1
        elif c == '"':
            start = i
            i, text = _string(source, i)
            here, line = line, line + source.count('\n', start, i)
            after = source[i:i + 200].lstrip(' \t')
            if text is None or not after:
                continue
            before = source[max(0, start - 200):start].rstrip()
            top = stack[-1] if stack else None
            call = top.call if top and top.opener == '(' else None
            named = re.search(r'([A-Za-z_]\w*)\s*=$', before)
            if after[0] in ENDS and named and not re.search(r'[=!<>+\-*/]=$', before):
                found.append((here, text, call, named.group(1), source[start:i]))
            elif after[0] in ENDS and call and before.endswith(('(', ',')):
                found.append((here, text, call, top.index, source[start:i]))
            elif top and top.when and before.endswith('->') and after[0] in '\n};':
                top.pending.append((here, text, *top.when, source[start:i]))
            else:
                branch = _branch(source, start, before, after, top, closed)
                if branch:
                    found.append((here, text, *branch, source[start:i]))
        elif c == "'":
            end = source.find("'", i + 1)
            i = end + 1 if 0 < end - i <= 8 else i + 1
        elif c in '([{':
            head = source[max(0, i - 80):i].rstrip()
            name = IDENTIFIER.fullmatch(re.split(r'[^A-Za-z0-9_]', head)[-1] or '-')
            top = stack[-1] if stack else None
            frame = _Frame(name.group() if c == '(' and name else None, c, max(0, i - 80) + len(head) - len(name.group()) if name else i, i + 1)
            if c == '{' and top:
                # A `when` whose value is the whole argument: its branches' literals are that argument's.
                if closed and closed[0] == 'when' and not source[closed[1]:i].strip():
                    keyword = closed[2]
                else:
                    bare = re.search(r'\bwhen\s*$', source[top.arg:i])
                    keyword = top.arg + bare.start() if bare else None
                found_when = keyword is not None and _argument(top, source[top.arg:keyword])
                if found_when and not found_when[2].strip():
                    frame.when = found_when[:2]
            stack.append(frame)
            i += 1
        elif c in ')]}':
            opener = {')': '(', ']': '[', '}': '{'}[c]
            while stack:
                frame = stack.pop()
                if frame.opener == opener:
                    break
            else:
                frame = None
            if frame and frame.pending and source[i + 1:i + 200].lstrip(' \t')[:1] in tuple(ENDS) + ('',):
                found.extend(frame.pending)
            closed = (frame.call, i + 1, frame.where) if frame and c == ')' else None
            i += 1
        elif c == ',':
            if stack and stack[-1].opener in '([':
                stack[-1].index += 1
                stack[-1].arg = i + 1
            i += 1
        elif c == ';':
            if stack and stack[-1].opener == '{':
                stack[-1].arg = i + 1
            i += 1
        else:
            i += 1
    return sorted(found, key=lambda hit: hit[0])


COMPOSABLE = re.compile(r'@Composable\s+(?:(?:private|internal|public|inline)\s+)*fun\s+(?:<(?:[^<>]|<[^<>]*>)*>\s*)?([A-Za-z_]\w*)\s*\(')


def parameters(source, i):
    """[(name, type)] of the parameter list whose `(` is at source[i]."""
    depth, start, out, n = 0, i + 1, [], len(source)
    while i < n:
        c = source[i]
        if c in '([{<':
            depth += 1
        elif c in ')]}>' and not (c == '>' and source[i - 1] == '-'):
            depth -= 1
            if depth == 0:
                out.append(source[start:i])
                break
        elif c == ',' and depth == 1:
            out.append(source[start:i])
            start = i + 1
        i += 1
    params = []
    for text in out:
        text = re.sub(r'/\*.*?\*/|//[^\n]*|@\w+(?:\([^)]*\))?', ' ', text, flags=re.S)
        match = re.match(r'\s*(?:vararg\s+|crossinline\s+|noinline\s+)*([A-Za-z_]\w*)\s*:\s*([^=]*)', text)
        if match:
            params.append((match.group(1), match.group(2).strip()))
    return params


def composables(root=ANDROID):
    """Composable name -> (positions, names) of its String parameters that show text, over every overload declared in
    the Android sources (tests included, so previews and helpers declared there are known too)."""
    shown_at = {call: (set(at), set(SHOWN_NAMED.get(call, ()))) for call, at in SHOWN_AT.items()}
    for path in sorted(root.rglob('*.kt')):
        if any(part in SKIP_DIRS for part in path.relative_to(root).parts):
            continue
        source = path.read_text(encoding='utf-8')
        for match in COMPOSABLE.finditer(source):
            at, names = shown_at.setdefault(match.group(1), (set(), set()))
            for index, (name, kind) in enumerate(parameters(source, match.end() - 1)):
                if name in TEXT_NAMES and re.fullmatch(r'String\??', kind):
                    at.add(index)
                    names.add(name)
    return shown_at


def shown(call, argument, known):
    """Whether a literal passed to [call] as [argument] is shown to the user (or read out as a description)."""
    if argument == 'contentDescription':
        return True  # also `semantics { contentDescription = "…" }`
    at, names = known.get(call, ((), ()))
    return argument in (at if isinstance(argument, int) else names)


def english(text):
    """Whether literal [text] (templates and escapes blanked) reads as English words rather than an identifier, key,
    URL, host, path, format, symbol, acronym or product name."""
    plain = PLACEHOLDER.sub(' ', text).strip()
    if CJK.search(plain) or '://' in plain or '@' in plain:
        return False
    if ' ' not in plain and re.search(r'[./_:#=]', plain):
        return False
    letters = re.search(r'[A-Za-z]', plain)
    if not letters or not letters.group().isupper():
        return False
    words = re.findall(r"[A-Za-z][A-Za-z'’]*", BRANDS.sub(' ', plain))
    if ' ' not in plain and re.fullmatch(r'[A-Z][a-z0-9]+[A-Z]\w*', plain):
        return False  # PascalCase: a type or key
    return any(len(w) > 1 and re.search(r'[a-z]', w) for w in words)


def english_hits(path, known):
    """(line, literal) of each hardcoded English UI literal in a Kotlin file; [known] is composables()."""
    text = path.read_text(encoding='utf-8')
    lines = text.split('\n')
    return [(line, raw) for line, literal, call, argument, raw in ui_literals(text)
            if shown(call, argument, known) and english(literal) and IGNORE not in lines[line - 1]]


def xml_count(path):
    count = 0
    for line in path.read_text(encoding='utf-8').split('\n'):
        stripped = re.sub(r'<!--.*?-->', '', line)
        if CJK.search(stripped) and IGNORE not in line and 'translatable="false"' not in line:
            count += 1
    return count


def source_files(root=ANDROID):
    for module in modules(root):
        src = module / 'src'
        for path in sorted(src.rglob('*')):
            if not path.is_file():
                continue
            parts = path.relative_to(src).parts
            if parts[0] in ('test', 'androidTest') or parts[0].startswith('test'):
                continue
            # Only the design system's developer catalog is exempt; ordinary debug client code is still checked.
            catalog = ('core', 'designsystem', 'src', 'debug', 'kotlin', 'persona', 'core', 'designsystem', 'catalog')
            if path.relative_to(root).parts[:len(catalog)] == catalog:
                continue
            if path.suffix == '.kt':
                yield path, kotlin_count
            elif path.suffix == '.xml' and not any(p in (set(languages(root)) - {'values'}) for p in parts):
                yield path, xml_count


def count(root=ANDROID):
    counts = {}
    for path, counter in source_files(root):
        n = counter(path)
        if n:
            counts[path.relative_to(root).as_posix()] = n
    return counts


def english_hits_by_file(root=ANDROID):
    """path -> [(line, literal)] of hardcoded English UI text in Kotlin sources (same files as the Chinese count)."""
    hits, known = {}, composables(root)
    for path, counter in source_files(root):
        relative = path.relative_to(root).parts
        # Shared or fixture test source sets (`sharedTest`, `testFixtures`) are not shown either.
        if counter is kotlin_count and 'test' not in relative[relative.index('src') + 1].lower():
            found = english_hits(path, known)
            if found:
                hits[path.relative_to(root).as_posix()] = found
    return hits


def regressions(counts, baseline, what='Chinese / Japanese'):
    return [f'{path}: {n} lines with hardcoded {what} text (baseline {baseline.get(path, 0)}); move them to strings.xml'
            for path, n in sorted(counts.items()) if n > baseline.get(path, 0)]


def baselines(root=ANDROID, name=BASELINE):
    """path -> recorded count, from every module's baseline file."""
    recorded = {}
    for module in modules(root):
        file = module / name
        if file.exists():
            recorded.update(json.loads(file.read_text()))
    return recorded


def write_baselines(counts, root=ANDROID, name=BASELINE):
    file_name = name
    for module in modules(root):
        name = module.relative_to(root).as_posix() + '/'
        mine = {path: n for path, n in counts.items() if path.startswith(name)}
        file = module / file_name
        if mine:
            file.write_text(json.dumps(mine, indent=2, ensure_ascii=False, sort_keys=True) + '\n')
        elif file.exists():
            file.unlink()


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('--update', action='store_true', help='record the current counts; they may only go down')
    parser.add_argument('--init', action='store_true', help=argparse.SUPPRESS)
    args = parser.parse_args()
    problems = language_problems()
    if problems:
        for problem in problems:
            print(problem, file=sys.stderr)
        return 1
    problems = [p for module in modules() for p in resource_problems(module)]
    counts = count()
    baseline = counts if args.init else baselines()
    problems += regressions(counts, baseline)
    hits = english_hits_by_file()
    english_counts = {path: len({line for line, _ in found}) for path, found in hits.items()}
    english_baseline = english_counts if args.init else baselines(name=ENGLISH_BASELINE)
    english_problems = regressions(english_counts, english_baseline, 'English')
    problems += english_problems
    if args.init and not problems:
        write_baselines(counts)
        write_baselines(english_counts, name=ENGLISH_BASELINE)
    elif args.update:
        # Record what went down even while another module is over its baseline (that one stays reported): agents
        # migrating different modules at once must not block each other.
        write_baselines({path: min(n, baseline[path]) for path, n in counts.items() if path in baseline})
        write_baselines({path: min(n, english_baseline[path]) for path, n in english_counts.items() if path in english_baseline},
                        name=ENGLISH_BASELINE)
    over = {problem.split(':', 1)[0] for problem in english_problems}
    for path, found in sorted(hits.items()):
        # Recorded lines are listed (to move into strings.xml later); a file over its baseline goes to stderr.
        for line, literal in found:
            print(f'{path}:{line}: hardcoded English {literal}', file=sys.stderr if path in over else sys.stdout)
    for problem in problems:
        print(problem, file=sys.stderr)
    total, before = sum(counts.values()), sum(baseline.values())
    print(f'android i18n: {len(problems)} problem(s); {total} hardcoded lines in {len(counts)} files (baseline {before})')
    total, before = sum(english_counts.values()), sum(english_baseline.values())
    print(f'android i18n: {total} hardcoded English lines in {len(english_counts)} files (baseline {before})')
    return 1 if problems else 0


if __name__ == '__main__':
    sys.exit(main())
