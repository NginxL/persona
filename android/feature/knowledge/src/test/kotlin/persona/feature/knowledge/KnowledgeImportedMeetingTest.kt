@file:OptIn(kotlinx.coroutines.ExperimentalCoroutinesApi::class, androidx.compose.ui.test.ExperimentalTestApi::class)

package persona.feature.knowledge

import android.graphics.Bitmap
import androidx.activity.ComponentActivity
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.test.ExperimentalTestApi
import androidx.compose.ui.test.SemanticsMatcher
import androidx.compose.ui.test.assert
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.dp
import androidx.lifecycle.ViewModelStore
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.io.File
import java.io.IOException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.collect
import kotlinx.coroutines.launch
import kotlinx.coroutines.test.StandardTestDispatcher
import kotlinx.coroutines.test.UnconfinedTestDispatcher
import kotlinx.coroutines.test.resetMain
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import kotlinx.coroutines.test.setMain
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.HubScope
import persona.core.contracts.JsonCodec
import persona.core.contracts.api.DraftSnapshot
import persona.core.contracts.api.KnowledgeConnection
import persona.core.contracts.api.KnowledgeDetail
import persona.core.contracts.api.KnowledgeDetailResponse
import persona.core.contracts.api.KnowledgeEntry
import persona.core.contracts.api.KnowledgeHit
import persona.core.contracts.api.KnowledgeKind
import persona.core.contracts.api.KnowledgeList
import persona.core.contracts.api.KnowledgeRepository
import persona.core.contracts.api.MainConversationSender
import persona.core.contracts.api.OutboxMessage
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator

/** One meeting imported from a connector and recorded by Persona is one Meetings row (#9994). */
class KnowledgeImportedMeetingTest {
    private fun entry(kind: KnowledgeKind, id: String, meetingId: String? = null, source: String? = null, extra: String? = null) =
        KnowledgeEntry(kind.wire, id, id.uppercase(), meetingId = meetingId, source = source, extra = extra)

    @Test fun meetingsAndRecordingsMergeWithEveryTagOnce() {
        val meetings = KnowledgeList(listOf(entry(KnowledgeKind.Meetings, "m1")), listOf("weekly", "client", "weekly"))
        val recordings = KnowledgeList(listOf(entry(KnowledgeKind.Recordings, "r1")), listOf("weekly", "travel", "travel"))

        val merged = mergeMeetingLists(meetings, recordings)

        assertEquals(listOf("m1", "r1"), merged.items.map { it.id })
        assertEquals(listOf("weekly", "client", "travel"), merged.tags)
        assertEquals(KnowledgeList(tags = emptyList()), mergeMeetingLists(KnowledgeList(), KnowledgeList()))
    }

    @Test fun aMatchedMeetingIsOmittedAndEveryRecordingKeepsItsOwnRow() {
        val lark = entry(KnowledgeKind.Meetings, "m1", source = "lark", extra = " https://meet.example.com/m1 ")
        val otherMeeting = entry(KnowledgeKind.Meetings, "m2")
        val first = entry(KnowledgeKind.Recordings, "r1", meetingId = "m1")
        val second = entry(KnowledgeKind.Recordings, "r2", meetingId = "m1")
        val absent = entry(KnowledgeKind.Recordings, "r3", meetingId = "not-listed")
        val unmatched = entry(KnowledgeKind.Recordings, "r4")

        val merged = mergeMeetingLists(KnowledgeList(listOf(lark, otherMeeting)), KnowledgeList(listOf(first, second, absent, unmatched)))

        assertEquals(listOf("m2", "r1", "r2", "r3", "r4"), merged.items.map { it.id })
        val matched = merged.items.first { it.id == "r1" }
        assertEquals(lark, matched.imported)
        assertEquals("m1", matched.imported?.id)
        assertEquals(" https://meet.example.com/m1 ", matched.imported?.extra)
        assertEquals("lark", matched.imported?.source)
        assertEquals(lark, merged.items.first { it.id == "r2" }.imported)
        assertEquals(absent, merged.items.first { it.id == "r3" })
        assertNull(merged.items.first { it.id == "r3" }.imported)
        assertEquals(unmatched, merged.items.first { it.id == "r4" })
        assertNull(merged.items.first { it.id == "r4" }.imported)
    }

    @Test fun recordingDetailResolvesOnlyTheNamedMeetingWithANonblankNotesUrl() {
        val link = entry(KnowledgeKind.Meetings, "m1", source = "larkcli", extra = " https://meet.example.com/m1 ")
        fun detail(meta: String, links: List<KnowledgeEntry>? = listOf(link), kind: KnowledgeKind = KnowledgeKind.Recordings) =
            KnowledgeDetail(kind.wire, "r1", "data/${kind.wire}/r1", meta = JsonCodec.decodeFromString(meta), links = links)
        val named = """{"imported_meeting":{"kind":"meetings","id":"m1","name":"Weekly sync"}}"""

        val imported = detail(named).importedMeeting
        assertEquals("m1", imported?.id)
        assertEquals("https://meet.example.com/m1", imported?.url)
        assertEquals("larkcli", imported?.source)

        assertNull(detail("{}", links = listOf(link)).importedMeeting)
        assertNull(detail(named, links = null).importedMeeting)
        assertNull(detail("""{"imported_meeting":{"kind":"meetings","id":"m9"}}""").importedMeeting)
        assertNull(detail(named, links = listOf(entry(KnowledgeKind.Docs, "m1", source = "lark", extra = NotesUrl))).importedMeeting)
        assertNull(detail(named, links = listOf(entry(KnowledgeKind.Meetings, "m1", source = "lark"))).importedMeeting)
        assertNull(detail(named, links = listOf(entry(KnowledgeKind.Meetings, "m1", source = "lark", extra = "  \n "))).importedMeeting)
        assertNull(detail(named, kind = KnowledgeKind.Meetings).importedMeeting)
    }

    @Test fun newConnectorFieldsDecodeAndOlderHubRepliesKeepTheirDefaults() {
        val current = JsonCodec.decodeFromString<KnowledgeList>(
            """{"items":[
            {"kind":"recordings","id":"r1","name":"Recorded weekly","relevance":0,"meeting_id":"m1"},
            {"kind":"meetings","id":"m1","name":"Imported weekly","relevance":0,"source":"lark","extra":"https://meet.example.com/m1"}
            ],"tags":[]}""",
        )
        assertEquals("m1", current.items[0].meetingId)
        assertEquals("lark", current.items[1].source)
        assertEquals("https://meet.example.com/m1", current.items[1].extra)

        val older = JsonCodec.decodeFromString<KnowledgeList>(
            """{"items":[{"kind":"recordings","id":"r1","name":"Weekly","relevance":0},
            {"kind":"meetings","id":"m1","name":"Weekly","relevance":0}],"tags":null}""",
        )
        assertNull(older.items[0].meetingId)
        assertNull(older.items[0].source)
        assertNull(older.items[0].imported)
        assertNull(older.items[1].source)
        assertEquals("Weekly", older.items[0].name)
    }

    private companion object { const val NotesUrl = "https://meet.example.com/m1" }
}

/** Meetings reads both sources as one page; every other kind keeps its single read. */
class KnowledgeImportedMeetingFlowTest {
    @Test fun meetingsLoadTogetherAndEitherFailedReadKeepsTheShownPageUntilRetry() = runTest {
        Dispatchers.setMain(StandardTestDispatcher(testScheduler))
        val store = ViewModelStore()
        try {
            val repository = ImportedMeetingRepository()
            val connection = object : KnowledgeConnection { override val current = MutableStateFlow<KnowledgeRepository?>(repository) }
            val model = KnowledgeViewModel(connection, StandardTestDispatcher(testScheduler))
            store.put("meetings", model)
            model.kind(KnowledgeKind.Meetings)
            backgroundScope.launch(UnconfinedTestDispatcher(testScheduler)) { model.page.collect() }
            runCurrent()

            assertEquals(setOf(KnowledgeKind.Meetings, KnowledgeKind.Recordings), repository.reads.map { it.first }.toSet())
            assertEquals(listOf("m3", "r1", "r2", "r3"), model.page.value.entries.map { it.id })
            assertEquals(listOf("weekly", "client", "travel"), model.page.value.tags)
            assertTrue(model.page.value.loaded)
            assertFalse(model.page.value.loading)

            for (failed in listOf(KnowledgeKind.Meetings, KnowledgeKind.Recordings)) {
                repository.fail = failed
                repository.reads.clear()
                model.refresh(); runCurrent()
                assertEquals(setOf(KnowledgeKind.Meetings, KnowledgeKind.Recordings), repository.reads.map { it.first }.toSet())
                assertEquals(R.string.knowledge_unreachable, model.page.value.message)
                assertEquals(listOf("m3", "r1", "r2", "r3"), model.page.value.entries.map { it.id })
                assertEquals(listOf("weekly", "client", "travel"), model.page.value.tags)
                assertTrue(model.page.value.loaded)
                assertFalse(model.page.value.loading)

                repository.fail = null
                model.refresh(); runCurrent()
                assertNull(model.page.value.message)
                assertEquals(listOf("m3", "r1", "r2", "r3"), model.page.value.entries.map { it.id })
                assertTrue(model.page.value.loaded)
            }
        } finally {
            backgroundScope.cancel()
            store.clear()
            runCurrent()
            Dispatchers.resetMain()
        }
    }

    @Test fun aNonMeetingsListDoesNotReadRecordings() = runTest {
        Dispatchers.setMain(StandardTestDispatcher(testScheduler))
        val store = ViewModelStore()
        try {
            val repository = ImportedMeetingRepository()
            val connection = object : KnowledgeConnection { override val current = MutableStateFlow<KnowledgeRepository?>(repository) }
            val model = KnowledgeViewModel(connection, StandardTestDispatcher(testScheduler))
            store.put("docs", model)
            model.kind(KnowledgeKind.Docs)
            backgroundScope.launch(UnconfinedTestDispatcher(testScheduler)) { model.page.collect() }
            runCurrent()

            assertEquals(listOf(KnowledgeKind.Docs), repository.reads.map { it.first })
            assertEquals(listOf("d1"), model.page.value.entries.map { it.id })
            assertEquals(listOf("spec"), model.page.value.tags)
        } finally {
            backgroundScope.cancel()
            store.clear()
            runCurrent()
            Dispatchers.resetMain()
        }
    }
}

/** The Meetings screen, its imported marks and TalkBack value, and the recording's notes row. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1800dp-xhdpi")
class KnowledgeImportedMeetingScreenTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/i18n-shots").also { it.mkdirs() }

    @Test fun meetingsHasNoRecordingsChipAndMatchedRowsHaveTheRightAvatarAndTalkBackValue() {
        val repository = ImportedMeetingRepository()
        val connection = object : KnowledgeConnection { override val current = MutableStateFlow<KnowledgeRepository?>(repository) }
        val model = KnowledgeViewModel(connection).also { it.kind(KnowledgeKind.Meetings) }
        var dark by mutableStateOf(false)
        var style by mutableStateOf(AppStyle.Glass)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Box(Modifier.testTag("imported-meetings-shot").width(400.dp).height(1700.dp).background(PersonaTheme.palette.background)) {
                    KnowledgeScreen(model, NoImportedNavigator)
                }
            }
        }
        rule.waitUntilExactlyOneExists(hasText("R1"), 10_000)

        rule.onNodeWithText("Recordings").assertDoesNotExist()
        rule.onNodeWithText("M1").assertDoesNotExist()
        rule.onNodeWithText("M2").assertDoesNotExist()
        rule.onNodeWithText("M3").assertExists()
        rule.onNodeWithText("R1").assertExists()
        rule.onNodeWithText("R2").assertExists()
        rule.onNodeWithText("R3").assertExists()

        for ((pageStyle, isDark) in listOf(AppStyle.Glass to false, AppStyle.Glass to true,
            AppStyle.Brief to false, AppStyle.Brief to true, AppStyle.Board to false, AppStyle.Board to true)) {
            style = pageStyle
            dark = isDark
            rule.waitForIdle()
            val appearance = "${pageStyle.name.lowercase()}-${if (isDark) "dark" else "light"}"
            val lark = rule.onNodeWithTag("knowledge-imported-avatar-r1", useUnmergedTree = true).assertExists().captureToImage().asAndroidBitmap()
            assertTrue("$appearance: Lark's published blue mark", lark.hasColorNear(0x33, 0x70, 0xff))
            assertTrue("$appearance: third-party marks keep their white backing", lark.hasWhiteBacking())
            val other = rule.onNodeWithTag("knowledge-imported-avatar-r2", useUnmergedTree = true).assertExists().captureToImage().asAndroidBitmap()
            assertFalse("$appearance: another connector must not borrow Lark's mark", other.hasColorNear(0x33, 0x70, 0xff))
            assertTrue("$appearance: another connector has a gray link badge", other.hasGrayBadge())
            rule.onNode(hasText("R1") and hasClickAction()).assert(
                SemanticsMatcher.expectValue(SemanticsProperties.StateDescription, "Lark Meeting Notes"))
            rule.onNode(hasText("R2") and hasClickAction()).assert(
                SemanticsMatcher.expectValue(SemanticsProperties.StateDescription, "Original Meeting Notes"))
            rule.onAllNodesWithTag("knowledge-imported-avatar-r3", useUnmergedTree = true).assertCountEquals(0)
            assertFalse(rule.onNode(hasText("R3") and hasClickAction()).fetchSemanticsNode().config.contains(SemanticsProperties.StateDescription))
            shot("imported-meetings-shot", "knowledge-imported-meetings-en-$appearance.png")
        }
    }

    @Test fun larkAndOriginalNotesRowsSitUnderFactsAndOpenThroughTheInAppOpener() {
        val repository = ImportedMeetingRepository()
        val connection = object : KnowledgeConnection { override val current = MutableStateFlow<KnowledgeRepository?>(repository) }
        val model = KnowledgeDetailViewModel(KnowledgeKind.Recordings, "r1", connection, NoImportedSender)
        val opened = mutableListOf<String>()
        var dark by mutableStateOf(false)
        var style by mutableStateOf(AppStyle.Glass)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Box(Modifier.testTag("imported-detail-shot").width(400.dp).height(1700.dp).background(PersonaTheme.palette.background)) {
                    KnowledgeDetailScreen(model, NoImportedNavigator, openLinkInApp = { opened += it })
                }
            }
        }
        rule.waitUntilExactlyOneExists(hasText("Lark Meeting Notes"), 10_000)

        for ((pageStyle, isDark) in listOf(AppStyle.Glass to false, AppStyle.Glass to true,
            AppStyle.Brief to false, AppStyle.Brief to true, AppStyle.Board to false, AppStyle.Board to true)) {
            style = pageStyle
            dark = isDark
            rule.waitForIdle()
            val appearance = "${pageStyle.name.lowercase()}-${if (isDark) "dark" else "light"}"
            assertNotesDirectlyUnderFacts("Lark Meeting Notes")
            shot("imported-detail-shot", "knowledge-imported-detail-en-$appearance.png")
        }
        rule.onAllNodesWithText("Lark Meeting Notes").assertCountEquals(1)
        rule.onNodeWithText("Lark Meeting Notes").performClick()
        assertEquals(listOf(NotesUrl), opened)

        repository.source = "calendar-x"
        model.refresh()
        rule.waitUntilExactlyOneExists(hasText("Original Meeting Notes"), 10_000)
        rule.onNodeWithText("Lark Meeting Notes").assertDoesNotExist()
        rule.onAllNodesWithText("Original Meeting Notes").assertCountEquals(1)
        assertNotesDirectlyUnderFacts("Original Meeting Notes")
        rule.onNodeWithText("Original Meeting Notes").performClick()
        assertEquals(listOf(NotesUrl, NotesUrl), opened)
    }

    @Config(sdk = [35], qualifiers = "zh-w400dp-h1800dp-xhdpi")
    @Test fun simplifiedChineseNotesLabels() = notesLabels("飞书会议纪要", "原会议纪要")

    @Config(sdk = [35], qualifiers = "ja-w400dp-h1800dp-xhdpi")
    @Test fun japaneseNotesLabels() = notesLabels("Lark の議事録", "元の議事録")

    @Test fun missingImportedMetadataAddsNoRowAndLeavesTheRecordingReadable() {
        val repository = ImportedMeetingRepository().also { it.importedMetadata = false }
        val connection = object : KnowledgeConnection { override val current = MutableStateFlow<KnowledgeRepository?>(repository) }
        val model = KnowledgeDetailViewModel(KnowledgeKind.Recordings, "r1", connection, NoImportedSender)
        rule.setContent { PersonaTheme { KnowledgeDetailScreen(model, NoImportedNavigator, openLinkInApp = {}) } }
        rule.waitUntilExactlyOneExists(hasText("Started"), 10_000)

        rule.onNodeWithText("R1").assertExists()
        rule.onNodeWithText("A decision was recorded.").assertExists()
        rule.onNodeWithText("Lark Meeting Notes").assertDoesNotExist()
        rule.onNodeWithText("Original Meeting Notes").assertDoesNotExist()
        rule.onAllNodesWithTag("knowledge-imported-notes").assertCountEquals(0)
    }

    private fun notesLabels(lark: String, original: String) {
        val repository = ImportedMeetingRepository()
        val connection = object : KnowledgeConnection { override val current = MutableStateFlow<KnowledgeRepository?>(repository) }
        val model = KnowledgeDetailViewModel(KnowledgeKind.Recordings, "r1", connection, NoImportedSender)
        rule.setContent { PersonaTheme { KnowledgeDetailScreen(model, NoImportedNavigator, openLinkInApp = {}) } }
        rule.waitUntilExactlyOneExists(hasText(lark), 10_000)
        rule.onAllNodesWithText(lark).assertCountEquals(1)
        rule.onNodeWithText(original).assertDoesNotExist()
        repository.source = "calendar-x"
        model.refresh()
        rule.waitUntilExactlyOneExists(hasText(original), 10_000)
        rule.onAllNodesWithText(original).assertCountEquals(1)
        rule.onNodeWithText(lark).assertDoesNotExist()
    }

    private fun assertNotesDirectlyUnderFacts(label: String) {
        rule.onNodeWithTag("knowledge-imported-notes").assertExists()
        val started = rule.onNodeWithText("Started", useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
        val notes = rule.onNodeWithText(label, useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
        val summary = rule.onNodeWithText("Summary", useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
        assertTrue("The notes row follows the facts: facts=$started, notes=$notes", notes.top > started.bottom)
        assertTrue("The notes row is directly under the facts, before the next section: notes=$notes, summary=$summary",
            notes.bottom < summary.top)
    }

    private fun shot(tag: String, name: String) {
        val bitmap = rule.onNodeWithTag(tag).captureToImage().asAndroidBitmap()
        File(out(), name).outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }

    private companion object { const val NotesUrl = "https://meet.example.com/m1" }
}

private class ImportedMeetingRepository : KnowledgeRepository {
    override val scope = HubScope("demo", "imported-meetings")
    var fail: KnowledgeKind? = null
    var source: String? = "lark"
    var importedMetadata = true
    val reads = mutableListOf<Pair<KnowledgeKind, String?>>()

    private val imported = KnowledgeEntry("meetings", "m1", "M1", source = "lark", extra = NotesUrl,
        tags = listOf("weekly"), updated = "2026-11-05T13:00:00Z")
    private val otherMeeting = KnowledgeEntry("meetings", "m2", "M2", tags = listOf("client"), updated = "2026-11-04T13:00:00Z")
    private val unmatchedMeeting = KnowledgeEntry("meetings", "m3", "M3", updated = "2026-11-03T13:00:00Z")
    private val first = KnowledgeEntry("recordings", "r1", "R1", meetingId = "m1", extra = "phone",
        tags = listOf("weekly"), updated = "2026-11-05T14:00:00Z")
    private val second = KnowledgeEntry("recordings", "r2", "R2", meetingId = "m2", extra = "phone",
        tags = listOf("travel"), updated = "2026-11-04T14:00:00Z")
    private val absent = KnowledgeEntry("recordings", "r3", "R3", meetingId = "not-listed", extra = "phone",
        updated = "2026-11-03T14:00:00Z")

    override suspend fun list(kind: KnowledgeKind, tag: String?): KnowledgeList {
        reads += kind to tag
        if (fail == kind) throw IOException("offline: ${kind.wire}")
        return when (kind) {
            KnowledgeKind.Meetings -> KnowledgeList(listOf(imported, otherMeeting.copy(source = "calendar-x"), unmatchedMeeting),
                listOf("weekly", "client", "weekly"))
            KnowledgeKind.Recordings -> KnowledgeList(listOf(first, second, absent), listOf("weekly", "travel", "travel"))
            KnowledgeKind.Docs -> KnowledgeList(listOf(KnowledgeEntry("docs", "d1", "D1")), listOf("spec"))
            else -> KnowledgeList()
        }
    }

    override suspend fun detail(kind: KnowledgeKind, id: String) = KnowledgeDetail(kind.wire, id, "data/${kind.wire}/$id",
        meta = buildJsonObject {
            put("name", id.uppercase())
            put("started_at", "2026-11-05T14:00:00Z")
            put("complete", true)
            if (importedMetadata) put("imported_meeting", buildJsonObject {
                put("kind", "meetings"); put("id", "m1"); put("name", "M1")
            })
        },
        summary = "A decision was recorded.",
        links = listOf(KnowledgeEntry("meetings", "m1", "M1", source = source, extra = " $NotesUrl ")),
    )

    override suspend fun entry(kind: KnowledgeKind, id: String) = KnowledgeDetailResponse(detail(kind, id))
    override suspend fun search(query: String) = emptyList<KnowledgeHit>()

    private companion object { const val NotesUrl = "https://meet.example.com/m1" }
}

private object NoImportedNavigator : Navigator {
    override fun navigate(key: NavKey) = Unit
    override fun back() = Unit
    override fun selectTab(tab: AppTab) = Unit
}

private object NoImportedSender : MainConversationSender {
    override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
    override suspend fun sendToMain(text: String, clientMessageId: String, draft: DraftSnapshot?) = Unit
}

private fun Bitmap.hasColorNear(red: Int, green: Int, blue: Int, tolerance: Int = 8): Boolean {
    for (y in 0 until height) for (x in 0 until width) {
        val pixel = getPixel(x, y)
        if (android.graphics.Color.alpha(pixel) > 240 &&
            kotlin.math.abs(android.graphics.Color.red(pixel) - red) <= tolerance &&
            kotlin.math.abs(android.graphics.Color.green(pixel) - green) <= tolerance &&
            kotlin.math.abs(android.graphics.Color.blue(pixel) - blue) <= tolerance
        ) return true
    }
    return false
}

private fun Bitmap.hasWhiteBacking(): Boolean {
    for (y in 0 until height) for (x in 0 until width) {
        val pixel = getPixel(x, y)
        if (android.graphics.Color.alpha(pixel) > 240 && android.graphics.Color.red(pixel) > 245 &&
            android.graphics.Color.green(pixel) > 245 && android.graphics.Color.blue(pixel) > 245
        ) return true
    }
    return false
}

private fun Bitmap.hasGrayBadge(): Boolean {
    for (y in 0 until height) for (x in 0 until width) {
        val pixel = getPixel(x, y)
        val red = android.graphics.Color.red(pixel)
        val green = android.graphics.Color.green(pixel)
        val blue = android.graphics.Color.blue(pixel)
        if (android.graphics.Color.alpha(pixel) > 200 && red in 70..220 &&
            kotlin.math.abs(red - green) <= 10 && kotlin.math.abs(red - blue) <= 10 && kotlin.math.abs(green - blue) <= 10
        ) return true
    }
    return false
}
