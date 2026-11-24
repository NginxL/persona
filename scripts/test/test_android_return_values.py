"""The Android modules where a dropped must-use result is still only a warning (#13024).

Every Android module compiles a dropped must-use result as an error (android/build-logic/convention/src/main/kotlin/
persona/buildlogic/ReturnValues.kt), except the modules its stillWarnings map lists, each with the call sites it has left.
A module is listed only while an open pull request rewrites its sites, so that fixing them would only make it conflict.
That list may only shrink, and this suite holds it to that:

- a module or a site outside FROZEN, what was listed when the error was turned on, fails: nothing new joins;
- a listed site whose line no longer stands as written fails: fix the dropped result where it now is, drop the site;
- a listed module with no sites left fails: drop the module, and it compiles a dropped result as an error from then on.

A site is "<module-relative file>: <line>", the line that drops the result, indentation aside. It stands while a line
of the file reads exactly that. Any change to it ends the exception, whether it fixes the result (`val _ = <line>`, a
result that is read) or not (`if (draft) <line>`, a reworded call): once the pull request it waits for rewrites the
line, there is nothing left to conflict with, so the site is fixed as it then reads.
"""
from pathlib import Path
import re
import unittest

ROOT = Path(__file__).resolve().parents[2]
ANDROID = ROOT / "android"
RETURN_VALUES = ANDROID / "build-logic/convention/src/main/kotlin/persona/buildlogic/ReturnValues.kt"
SETTINGS = ANDROID / "settings.gradle.kts"
# What stillWarnings listed when the error was turned on (#13024), each module with its sites. One may leave; none may
# join. #13128 tracks the last module out.
FROZEN = {
    ":feature:chat": frozenset({
        "src/main/kotlin/persona/feature/chat/ComposerAttachments.kt: "
        "runCatching { resolver.takePersistableUriPermission(uri, Intent.FLAG_GRANT_READ_URI_PERMISSION) }",
    }),
}

_MAP = re.compile(r"^private val stillWarnings: Map<String, List<String>> = mapOf\($(.*?)^\)$", re.M | re.S)
# Kotlin tokens of the map: a string literal, a word, one punctuation character; whitespace and // comments between.
_TOKEN = re.compile(r'\s+|//[^\n]*|"((?:[^"\\\n]|\\.)*)"|([A-Za-z_]\w*)|(.)')


def literal(body):
    """A Kotlin string literal's value; None when it holds a template, which a site must not."""
    if re.search(r"(?<!\\)\$", body):
        return None
    return re.sub(r"\\(.)", lambda m: {"n": "\n", "t": "\t"}.get(m.group(1), m.group(1)), body)


def tokens(text):
    """(kind, value) for each token: ("string", its value or None for a template), ("word", w) or ("mark", c)."""
    out = []
    for m in _TOKEN.finditer(text):
        if m.group(1) is not None:
            out.append(("string", literal(m.group(1))))
        elif m.group(2) is not None:
            out.append(("word", m.group(2)))
        elif m.group(3) is not None:
            out.append(("mark", m.group(3)))
    return out


def entries(source):
    """stillWarnings as (module, [site, ...]) pairs, and the problems reading it."""
    found = _MAP.search(source)
    if not found:
        return [], ["ReturnValues.kt: no `private val stillWarnings: Map<String, List<String>> = mapOf(` ... `)` block"]
    toks, at, result = tokens(found.group(1)), 0, []

    def take(kind, value=None):
        nonlocal at
        if at < len(toks) and toks[at][0] == kind and (value is None or toks[at][1] == value):
            at += 1
            return toks[at - 1]
        raise ValueError("expected %s at %s" % (value or kind, toks[at] if at < len(toks) else "the end"))

    def string():
        value = take("string")[1]
        if value is None:
            raise ValueError("a site or module holds a string template")
        return value

    try:
        while at < len(toks):
            module = string()
            take("word", "to"), take("word", "listOf"), take("mark", "(")
            sites = []
            while toks[at] != ("mark", ")"):
                site = string()
                while toks[at] == ("mark", "+"):
                    take("mark", "+")
                    site += string()
                sites.append(site)
                if toks[at] != ("mark", ")"):
                    take("mark", ",")
            take("mark", ")")
            if at < len(toks) and toks[at] == ("mark", ","):
                take("mark", ",")
            result.append((module, sites))
    except (ValueError, IndexError) as error:
        return result, ["ReturnValues.kt: stillWarnings is not `\":module\" to listOf(\"<file>: <statement>\", ...)`, "
                        "with string literals joined by +: %s" % (error or "it ends early")]
    return result, []


def stands(text, line):
    """Whether a line of text reads exactly line, leading and trailing spaces aside (the module docstring's rule)."""
    return any(candidate.strip() == line.strip() for candidate in text.splitlines())


def problems(source, settings, read, frozen=None):
    """What breaks the rules: source is ReturnValues.kt, settings settings.gradle.kts, read(path) a module file's text
    (None when the file is gone), with paths relative to android/, and frozen what may be listed (FROZEN)."""
    frozen = FROZEN if frozen is None else frozen
    listed, found = entries(source)
    seen = set()
    for module, sites in listed:
        if module in seen:
            found.append("%s is listed twice" % module)
        seen.add(module)
        if module not in frozen:
            found.append("%s is not one of the modules listed when the error was turned on (%s): the list may only "
                         "shrink, so fix its dropped results instead (val _ = call() for one dropped on purpose)"
                         % (module, ", ".join(sorted(frozen))))
        if '"%s"' % module not in settings:
            found.append("%s is not a module in android/settings.gradle.kts: drop it from stillWarnings" % module)
        if not sites:
            found.append("%s has no sites left: drop it from stillWarnings, and it compiles a dropped result as an "
                         "error like every other module" % module)
        directory = module.strip(":").replace(":", "/")
        for site in sites:
            path, sep, line = site.partition(": ")
            if not sep or not path or not line.strip():
                found.append("%s: %r is not \"<module-relative file>: <line>\"" % (module, site))
                continue
            if module in frozen and site not in frozen[module]:
                found.append("%s: %r is not one of the sites listed when the error was turned on: the list may only "
                             "shrink, so fix the dropped result where it is (val _ = call() for one dropped on "
                             "purpose)" % (module, site))
                continue
            text = read("%s/%s" % (directory, path))
            if text is None:
                found.append("%s: %s is gone; drop its site from stillWarnings" % (module, path))
            elif not stands(text, line):
                found.append("%s: %s no longer has the line %r as written; fix the dropped result where it now is "
                             "(val _ = call() for one dropped on purpose) and drop the site from stillWarnings"
                             % (module, path, line))
    return found


def read_android(path):
    file = ANDROID / path
    return file.read_text(encoding="utf-8") if file.is_file() else None


SOURCE = '''fun Project.configureReturnValueChecks() {}

private val stillWarnings: Map<String, List<String>> = mapOf(
    // #1 rewrites this statement.
    ":feature:chat" to listOf(
        "src/main/A.kt: " +
            "runCatching { a() }",
        "src/test/B.kt: b()",
    ),
)
'''
SETTINGS_TEXT = 'include(":app")\ninclude(\n    ":feature:chat", ":feature:files",\n)\n'
FILES = {"feature/chat/src/main/A.kt": "fun x() {\n    runCatching { a() }\n}\n",
         "feature/chat/src/test/B.kt": "    b()\n"}
PLANTED_FROZEN = {":feature:chat": frozenset({"src/main/A.kt: runCatching { a() }", "src/test/B.kt: b()"})}


class ReadingTests(unittest.TestCase):
    def test_modules_and_joined_sites(self):
        self.assertEqual(entries(SOURCE), ([(":feature:chat", ["src/main/A.kt: runCatching { a() }",
                                                               "src/test/B.kt: b()"])], []))

    def test_an_empty_list_and_an_empty_module(self):
        empty = SOURCE.replace(SOURCE[SOURCE.index("    // #1"):SOURCE.rindex(")")], "")
        self.assertEqual(entries(empty), ([], []))
        self.assertEqual(entries('private val stillWarnings: Map<String, List<String>> = mapOf(\n'
                                 '    ":feature:chat" to listOf(),\n)\n'), ([(":feature:chat", [])], []))

    def test_a_missing_block_and_a_template(self):
        self.assertTrue(entries("val other = 1\n")[1][0].startswith("ReturnValues.kt: no "))
        templated = SOURCE.replace('"src/test/B.kt: b()"', '"src/test/B.kt: ${b}()"')
        self.assertIn("a site or module holds a string template", entries(templated)[1][0])
        self.assertIn("expected , at", entries(SOURCE.replace('" +\n', '"\n'))[1][0])

    def test_the_real_list_reads_cleanly(self):
        listed, found = entries(RETURN_VALUES.read_text(encoding="utf-8"))
        self.assertEqual(found, [])
        self.assertTrue(all(sites for _, sites in listed), listed)


class RuleTests(unittest.TestCase):
    def check(self, source=SOURCE, files=None, settings=SETTINGS_TEXT):
        files = FILES if files is None else files
        return problems(source, settings, files.get, PLANTED_FROZEN)

    def test_the_planted_list_passes(self):
        self.assertEqual(self.check(), [])

    def test_a_module_may_not_join(self):
        added = SOURCE.replace("\n)\n", '\n    ":feature:files" to listOf("src/main/C.kt: c()"),\n)\n')
        files = dict(FILES, **{"feature/files/src/main/C.kt": "c()\n"})
        found = self.check(added, files)
        self.assertEqual(len(found), 1, found)
        self.assertIn(":feature:files is not one of the modules listed when the error was turned on", found[0])

    def test_a_site_may_not_join(self):
        added = SOURCE.replace('"src/test/B.kt: b()",', '"src/test/B.kt: b()", "src/test/B.kt: c()",')
        found = self.check(added, dict(FILES, **{"feature/chat/src/test/B.kt": "    b()\n    c()\n"}))
        self.assertEqual(found, [":feature:chat: 'src/test/B.kt: c()' is not one of the sites listed when the error "
                                 "was turned on: the list may only shrink, so fix the dropped result where it is "
                                 "(val _ = call() for one dropped on purpose)"])

    def test_a_line_stands_only_as_written(self):
        line = "runCatching { a() }"
        for text in ("runCatching { a() }", "    runCatching { a() }  ", "    val b = 1\n    runCatching { a() }\n"):
            with self.subTest(text=text):
                self.assertTrue(stands(text, line))
        for text in ("val _ = runCatching { a() }", "runCatching { a() }.getOrNull()",
                     "    if (draft) runCatching { a() }", "} else runCatching { a() }", "runCatching { a() } // why",
                     "runCatching { a() + 1 }", "onClick = { runCatching { a() } }"):
            with self.subTest(text=text):
                self.assertFalse(stands(text, line))

    def test_a_changed_line_fails_until_its_site_is_dropped(self):
        # Fixed, removed, or rewritten by the pull request the site waits for (still dropping its result): each ends it.
        for changed in ("fun x() {\n    val _ = runCatching { a() }\n}\n", "fun x() {\n    a()\n}\n",
                        "fun x() {\n    if (y) runCatching { a() }\n}\n"):
            with self.subTest(changed=changed):
                found = self.check(files=dict(FILES, **{"feature/chat/src/main/A.kt": changed}))
                self.assertEqual(found, [":feature:chat: src/main/A.kt no longer has the line 'runCatching { a() }' as "
                                         "written; fix the dropped result where it now is (val _ = call() for one "
                                         "dropped on purpose) and drop the site from stillWarnings"])

    def test_the_rewritten_line_may_not_take_the_old_ones_place(self):
        relisted = SOURCE.replace('"runCatching { a() }"', '"if (y) runCatching { a() }"')
        found = self.check(relisted, dict(FILES, **{"feature/chat/src/main/A.kt": "    if (y) runCatching { a() }\n"}))
        self.assertEqual(len(found), 1, found)
        self.assertIn("'src/main/A.kt: if (y) runCatching { a() }' is not one of the sites listed", found[0])

    def test_a_gone_file_and_a_clean_module(self):
        found = self.check(files={"feature/chat/src/test/B.kt": "b()\n"})
        self.assertEqual(found, [":feature:chat: src/main/A.kt is gone; drop its site from stillWarnings"])
        clean = ('private val stillWarnings: Map<String, List<String>> = mapOf(\n'
                 '    ":feature:chat" to listOf(),\n)\n')
        self.assertEqual(len(self.check(clean)), 1)
        self.assertIn(":feature:chat has no sites left", self.check(clean)[0])

    def test_a_malformed_site_and_a_module_not_in_settings(self):
        bad = SOURCE.replace('"src/test/B.kt: b()"', '"src/test/B.kt"')
        self.assertIn("is not \"<module-relative file>: <line>\"", self.check(bad)[0])
        self.assertIn("is not a module in android/settings.gradle.kts", self.check(settings='include(":app")\n')[0])


class RepositoryTests(unittest.TestCase):
    def test_still_warnings_only_shrinks_and_every_site_is_still_there(self):
        found = problems(RETURN_VALUES.read_text(encoding="utf-8"), SETTINGS.read_text(encoding="utf-8"), read_android)
        self.assertEqual(found, [], "\n".join(["stillWarnings in %s:" % RETURN_VALUES.relative_to(ROOT)] + found))


if __name__ == "__main__":
    unittest.main()
