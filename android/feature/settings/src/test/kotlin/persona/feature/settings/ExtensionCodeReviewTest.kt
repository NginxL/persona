package persona.feature.settings

import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.JsonCodec
import persona.core.contracts.api.*
import java.io.File
import java.lang.management.ManagementFactory

// UI version 5's code review (#3136): the same cases as the web's lib/code-review.test.ts, and what the app reads from
// the component showcase page code-review-v5.
class ExtensionCodeReviewTest {
    private val r = ExtensionCodeReview
    private fun cut(s: String, spans: List<CharSpan>) = spans.map { s.substring(it.start, it.end) }
    private fun hunk(oldStart: Int, newStart: Int, vararg lines: String) = ExtensionDiffHunk(oldStart = oldStart, newStart = newStart, lines = lines.toList())

    @Test fun splitsWords() {
        assertEquals(listOf("return", " ", "fmt", ".", "Errorf", "(", "x", ")"), r.words("return fmt.Errorf(x)"))
        assertEquals(listOf("名字", " ", "=", " ", "1"), r.words("名字 = 1"))
    }

    @Test fun marksOnlyTheWordsThatChanged() {
        val a = "\t\treturn err"
        val b = "\t\treturn fmt.Errorf(\"upload: %w\", err)"
        val (old, new) = r.wordDiff(a, b)!!
        assertEquals(emptyList<String>(), cut(a, old))
        assertEquals(listOf("fmt.Errorf(\"upload: %w\", ", ")"), cut(b, new))
        val (o2, n2) = r.wordDiff("let a = old value;", "let a = new thing;")!!
        assertEquals(listOf("old value"), cut("let a = old value;", o2))
        assertEquals(listOf("new thing"), cut("let a = new thing;", n2))
    }

    @Test fun givesUpOnUnlikeOrLongLines() {
        assertNull(r.wordDiff("completely different text here", "nothing in common at all, really"))
        assertNull(r.wordDiff("x".repeat(2000), "y".repeat(2000)))
        assertEquals(emptyList<CharSpan>() to emptyList<CharSpan>(), r.wordDiff("same", "same"))
    }

    @Test fun pairsDeletionsWithTheAdditionsAfter() {
        val rows = ExtensionCodeText.rows(hunk(1, 1, " a", "-const x = 1", "-const y = 2", "+const x = 10", "+const y = 2 // same", " b"))
        val spans = r.wordSpans(rows)
        assertEquals(listOf("1"), cut(rows[1].text, spans.getValue(1)))
        assertEquals(listOf("10"), cut(rows[3].text, spans.getValue(3)))
        assertEquals(" // same", cut(rows[4].text, spans.getValue(4)).joinToString(""))
        assertFalse(0 in spans)
    }

    @Test fun cutsTokensAtTheChangedWords() {
        val pieces = r.overlay(listOf(CodeToken("return", TokenKind.Keyword), CodeToken(" err", TokenKind.Plain)), "return err", listOf(CharSpan(3, 8)))
        assertEquals(listOf(Piece("ret", TokenKind.Keyword, false), Piece("urn", TokenKind.Keyword, true), Piece(" e", TokenKind.Plain, true), Piece("rr", TokenKind.Plain, false)), pieces)
        assertEquals(listOf(Piece("plain", TokenKind.Plain, false)), r.overlay(null, "plain", null))
    }

    @Test fun ignoresWhitespace() {
        val rows = ExtensionCodeText.rows(hunk(10, 10, "-if x {", "-  y()", "+if x {", "+    y()", "+    z()", " }"))
        val out = r.ignoreWhitespace(rows)
        assertEquals(listOf(DiffKind.Context, DiffKind.Context, DiffKind.Add, DiffKind.Context), out.map { it.kind })
        assertEquals(DiffRow(DiffKind.Context, 11, 11, "    y()"), out[1])
        assertEquals(12, out[2].new)
        assertTrue(r.onlyWhitespace(listOf(hunk(1, 1, "-a  b", "+a b"))))
        assertFalse(r.onlyWhitespace(listOf(hunk(1, 1, "-a", "+b"))))
    }

    // #5704: a whole file put in another layout (as many lines as a diff may have) is paired without a table of every pair
    // (10,000 × 10,000 ints, some 400 MB: more than a phone's heap)
    @Test(timeout = 5000) fun ignoresWhitespaceInABigRun() {
        val n = 10_000
        val lines = (0 until n).map { "-val v$it = f($it)" } + (0 until n).map { "+    val v$it  =  f( $it )" }
        val changed = lines.mapIndexed { i, l -> when (i) { 5000 -> "-gone()"; n + 5000 -> "+came()"; else -> l } }
        val rows = ExtensionCodeText.rows(hunk(1, 1, *changed.toTypedArray()))
        val mx = ManagementFactory.getThreadMXBean() as com.sun.management.ThreadMXBean
        val before = mx.currentThreadAllocatedBytes
        assertTrue(r.onlyWhitespace(listOf(hunk(1, 1, *lines.toTypedArray()))))
        val out = r.ignoreWhitespace(rows)
        val mb = (mx.currentThreadAllocatedBytes - before) shr 20
        assertTrue("allocated $mb MB", mb < 64)
        assertEquals(n + 1, out.size)
        assertEquals(List(5000) { DiffKind.Context } + DiffKind.Del + DiffKind.Add + List(n - 5001) { DiffKind.Context }, out.map { it.kind })
        assertEquals(DiffRow(DiffKind.Context, 1, 1, "    val v0  =  f( 0 )"), out[0])
        assertEquals(DiffRow(DiffKind.Del, 5001, null, "gone()") to DiffRow(DiffKind.Add, null, 5001, "came()"), out[5000] to out[5001])
        assertEquals(DiffRow(DiffKind.Context, n, n, "    val v${n - 1}  =  f( ${n - 1} )"), out.last())
        // a line taken out whose like comes again further on stays alone, not pairing past the lines between
        val braces = (0 until n).map { if (it % 10 == 9) "}" else "x$it" }
        val extra = braces.take(101).map { "-$it" } + "-}" + braces.drop(101).map { "-$it" } + braces.map { "+  $it" }
        assertEquals(listOf(DiffRow(DiffKind.Del, 102, null, "}")), r.ignoreWhitespace(ExtensionCodeText.rows(hunk(1, 1, *extra.toTypedArray()))).filter { it.kind != DiffKind.Context })
    }

    @Test fun knowsGeneratedFiles() {
        listOf("go.sum", "web/package-lock.json", "a/yarn.lock", "api/x.pb.go", "gen/y_gen.go", "dist/app.min.js", "vendor/x/y.go", "Cargo.lock").forEach { assertTrue(it, r.isGenerated(it)) }
        listOf("main.go", "src/lock.ts", "docs/vendor.md").forEach { assertFalse(it, r.isGenerated(it)) }
        assertTrue(r.isGenerated("main.go", true))
        assertEquals(FoldReason.Generated, r.foldedAtFirst(ExtensionViewItem(type = "diff", path = "go.sum", collapsed = true), false))
        assertEquals(FoldReason.Big, r.foldedAtFirst(ExtensionViewItem(type = "diff", path = "a.go", additions = 300, deletions = 200), false))
        assertEquals(FoldReason.Viewed, r.foldedAtFirst(ExtensionViewItem(type = "diff", path = "a.go"), true))
        assertNull(r.foldedAtFirst(ExtensionViewItem(type = "diff", path = "a.go", additions = 3), false))
    }

    private val hunks = listOf(hunk(5, 5, " a", "-b", "+b2", "+b3", " c"), hunk(30, 31, " x", " y"))

    @Test fun findsTheGaps() {
        assertEquals(listOf(Gap(0, 1, 4, 0), Gap(1, 9, 30, -1), Gap(2, 33, null, -1)), r.gaps(hunks))
        assertEquals(40, r.gaps(hunks, 40)[2].to)
        assertEquals(emptyList<Gap>(), r.gaps(listOf(hunk(1, 0, "-gone"))))
    }

    @Test fun showsAGapTwentyLinesAtATime() {
        val g = r.gaps(hunks)[1]
        var (shown, need) = r.reveal(g, Shown(), "up")
        assertEquals(Shown(0, 20) to Need(11, 30), shown to need)
        r.reveal(g, shown, "down").let { (s, n) -> assertEquals(Shown(2, 20) to Need(9, 10), s to n) }
        assertEquals(Shown(22, 0), r.reveal(g, Shown(), "all").first)
        assertEquals(Need(33, 52), r.reveal(r.gaps(hunks)[2], Shown(), "up").second)
    }

    @Test fun drawsTheLinesShownFromTheCache() {
        val cache = LineCache()
        cache.put(1, (1..40).map { "line $it" }, 40)
        val g = r.gaps(hunks, cache.total)[1]
        val (top, bottom, hidden) = r.gapRows(g, Shown(2, 3), cache)
        assertEquals(listOf(Triple(8, 9, "line 9"), Triple(9, 10, "line 10")), top.map { Triple(it.old, it.new, it.text) })
        assertEquals(listOf(28, 29, 30), bottom.map { it.new })
        assertEquals(17, hidden)
        assertEquals(0, r.gapRows(g, Shown(22, 0), cache).third)
    }

    @Test fun keepsWhatCameOfAFile() {
        val c = LineCache()
        c.put(10, listOf("a", "b"), null)
        assertTrue(c.has(10, 11))
        assertFalse(c.has(9, 11))
        assertFalse(c.has(10, null))
        c.put(12, emptyList(), null)
        assertEquals(11, c.total)
        assertTrue(c.has(10, null))
    }

    @Test fun idsAViewedChange() {
        assertEquals("a.go@r1", r.changeId("a.go", "r1", null))
        assertNotEquals(r.changeId("a.go", null, listOf(hunk(1, 1, "+a"))), r.changeId("a.go", null, listOf(hunk(1, 1, "+b"))))
    }

    @Test fun cutsSuggestions() {
        assertEquals(listOf(BodyPart.Text("Maybe:\n\n"), BodyPart.Suggestion(listOf("return nil")), BodyPart.Text("\nthanks")),
            r.splitSuggestions("Maybe:\n\n```suggestion\nreturn nil\n```\nthanks"))
        assertEquals(listOf(BodyPart.Suggestion(emptyList())), r.splitSuggestions("```suggestion\n```"))
        assertEquals(listOf(BodyPart.Text("no block")), r.splitSuggestions("no block"))
        assertEquals("```suggestion\na\nb\n```\n", r.suggestionBlock(listOf("a", "b")))
    }

    @Test fun picksARangeOnOneSide() {
        assertEquals(LinePick("new", 9, 5), r.pickRange(LinePick("new", 9), LinePick("new", 5)))
        assertEquals(LinePick("new", 5), r.pickRange(LinePick("old", 9), LinePick("new", 5)))
        assertEquals(LinePick("new", 5), r.pickRange(null, LinePick("new", 5)))
        assertTrue(LinePick("new", 9, 5).has("new", 7))
        assertFalse(LinePick("new", 9, 5).has("old", 7))
    }

    @Test fun fillsALineLink() {
        assertEquals("https://example.com/a.go#L3-L7", r.lineLink("https://example.com/a.go#L{start}-L{end}", 3, 7))
        assertNull(r.lineLink("javascript:alert({start})", 1))
    }

    private fun diff(key: String, lines: List<String>? = null) =
        ExtensionViewItem(type = "diff", key = key, path = key, hunks = lines?.let { listOf(ExtensionDiffHunk(oldStart = 1, newStart = 1, lines = it)) },
            load = if (lines == null) ExtensionViewAction("load") else null)

    @Test fun putsComponentsInPlace() {
        val view = ExtensionView(sections = listOf(ExtensionViewSection(items = listOf(diff("a"), diff("b")))))
        val out = r.patchView(view, listOf(diff("b", listOf("+x"))))
        assertEquals(listOf("+x"), out.sections[0].items[1].hunks!![0].lines)
        assertSame(view, r.patchView(view, listOf(diff("zzz", listOf("+x")))))
        val before = ExtensionView(sections = listOf(ExtensionViewSection(items = listOf(diff("a", listOf("+1")), diff("b")))))
        val kept = r.keepLoaded(before, view)
        assertEquals(listOf("+1"), kept.sections[0].items[0].hunks!![0].lines)
        assertNull(kept.sections[0].items[1].hunks)
    }

    @Test fun readsTheReviewPage() {
        val v = JsonCodec.decodeFromString(ExtensionView.serializer(), File("../../../schema/extension/examples/showcase/code-review-v5.json").readText())
        assertEquals(2, v.review!!.count)
        assertEquals(listOf("submit_review", "submit_review", "submit_review"), v.review!!.buttons.map { it.action.id })
        val retry = v.sections[2].items[0]
        assertEquals(true, retry.viewed)
        assertEquals("mark_viewed", retry.markViewed!!.id)
        assertEquals("context", retry.context!!.id)
        assertEquals("draft_comment", retry.draft!!.id)
        assertTrue(retry.threads!![1].pending)
        assertEquals("resolve", v.sections[2].items[1].threads!![0].resolve!!.id)
        assertEquals(96, v.sections[2].items[2].similarity)
        assertEquals(true, v.sections[1].items[0].entries!!.last().generated)
        val res = JsonCodec.decodeFromString(ExtensionUiResult.serializer(), """{"lines":{"path":"a.go","side":"new","start":3,"lines":["x"],"total":9},"items":[{"type":"diff","key":"k","path":"a.go"}]}""")
        assertEquals(ExtensionFileLines("a.go", "new", 3, listOf("x"), 9), res.lines)
        assertEquals("k", res.items.single().key)
    }

    @Test fun viewModelPreservesLoadedDiffsAcrossRedrawQuietlyAndPressAndRethrowsCancellation() = kotlinx.coroutines.runBlocking {
        val unloaded = ExtensionView(sections = listOf(ExtensionViewSection(items = listOf(diff("a"), diff("b")))))
        var cancelRedraw = false
        var actionResult = ExtensionUiResult()
        val api = object : ExtensionUi {
            override suspend fun uiPlaces(placement: String, launcher: String?, machine: String?) = emptyList<ExtensionUiPlace>()
            override suspend fun renderUi(extension: String, ui: String, context: ExtensionUiContext?, state: String?): ExtensionView {
                if (cancelRedraw) throw kotlinx.coroutines.CancellationException("page left")
                return unloaded
            }
            override suspend fun uiAction(extension: String, ui: String, action: String, context: ExtensionUiContext?,
                                          state: String?, values: Map<String, String>?) = actionResult
            override suspend fun uiImage(path: String) = byteArrayOf()
        }
        val model = ExtensionViewModel(api, "review-ext", "pr")
        model.load()
        actionResult = ExtensionUiResult(items = listOf(diff("a", listOf("+loaded"))))
        model.fetch(ExtensionViewAction("load"), emptyMap())
        assertEquals(listOf("+loaded"), model.state.value.view!!.sections[0].items[0].hunks!!.single().lines)

        val rowControl = ExtensionRowControl(kind = "switch", name = "viewed", action = ExtensionViewAction("mark_viewed"))
        // 1. Silent control change triggers redraw(): loaded diff stays
        actionResult = ExtensionUiResult()
        model.change(rowControl, "true")
        assertEquals(listOf("+loaded"), model.state.value.view!!.sections[0].items[0].hunks!!.single().lines)

        // 2. quietly() returning a fresh view: loaded diff stays
        actionResult = ExtensionUiResult(view = unloaded)
        model.quietly("more", emptyMap()) { it }
        assertEquals(listOf("+loaded"), model.state.value.view!!.sections[0].items[0].hunks!!.single().lines)

        // 3. press() returning a fresh view: loaded diff stays
        actionResult = ExtensionUiResult(view = unloaded)
        model.press(ExtensionViewAction("refresh"))
        assertEquals(listOf("+loaded"), model.state.value.view!!.sections[0].items[0].hunks!!.single().lines)

        // 4. CancellationException inside redraw() propagates out of change() instead of being swallowed
        actionResult = ExtensionUiResult()
        cancelRedraw = true
        val thrown = runCatching { model.change(rowControl, "false") }.exceptionOrNull()
        assertTrue("expected CancellationException, got $thrown", thrown is kotlinx.coroutines.CancellationException)
    }
}
