package persona.feature.inbox

import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.jsonObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test
import persona.core.contracts.api.InboxDoneTarget
import persona.core.domain.inbox.InboxItem
import persona.core.domain.inbox.InboxListSettings
import persona.core.domain.inbox.buildInboxList
import persona.core.inbox.*

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class InboxBulkTest {
    /** The app's strings; Robolectric runs in Simplified Chinese (robolectric.properties). */
    private val res get() = androidx.test.core.app.ApplicationProvider.getApplicationContext<android.content.Context>().resources
    private val items = listOf(
        InboxItem("dm", account = "feishu", kind = "dm", tier = "action", updatedAt = "2026-11-04T10:00:00Z", version = "v1"),
        InboxItem("news", account = "acc1", kind = "mail", tier = "noise", updatedAt = "2026-11-04T09:00:00Z", version = "v2"),
        InboxItem("group", account = "feishu", kind = "group", tier = "fyi", updatedAt = "2026-11-04T08:00:00Z", version = "v3"),
        InboxItem("old", account = "feishu", kind = "dm", tier = "action", updatedAt = "2026-11-04T07:00:00Z",
            doneAt = "2026-11-04T07:30:00Z", version = "v4"),
    )
    private val records = items.associate { it.id to InboxRecord(it) }

    @Test fun theWholeInboxIsEveryOpenItemWithItsVersionFoldedSectionsToo() {
        val list = buildInboxList(items)
        assertEquals(listOf(InboxDoneTarget("dm", "v1"), InboxDoneTarget("group", "v3"), InboxDoneTarget("news", "v2")),
            bulkTargets(list.groups, records).sortedBy { it.id })
    }

    @Test fun aFilterOnlyCoversWhatItShows() {
        val feishu = buildInboxList(items, settings = InboxListSettings(source = "feishu"))
        assertEquals(setOf("dm", "group"), bulkTargets(feishu.groups, records).map { it.id }.toSet())
        val action = buildInboxList(items).groups.filter { it.id == "action" }
        assertEquals(listOf("dm"), bulkTargets(action, records).map { it.id })
        val searched = buildInboxList(items, settings = InboxListSettings(query = "没有这个"))
        assertEquals(emptyList<InboxDoneTarget>(), bulkTargets(searched.groups, records))
    }

    @Test fun theLabelSaysWhetherItIsTheWholeInbox() {
        assertNull(bulkScope(res, "", null, "", ""))
        assertEquals("全部清理 · 3 件", bulkLabel(res, 3, null))
        val scope = bulkScope(res, "action", "飞书", "@我", " 周报 ")
        assertEquals("需要你 · 飞书 · @我 · 搜索「周报」", scope)
        assertEquals("清理当前筛选 · 2 件", bulkLabel(res, 2, scope))
    }

    @Test fun theHubsAnswerSaysWhatMovedAndWhatTookNewMessages() {
        val result = InboxBulkResult.from(Json.parseToJsonElement("""
            {"changed":[{"id":"a"},{"id":"b"}],"unchanged":["c"],
             "skipped":[{"id":"d","reason":"changed"},{"id":"e","reason":"missing"}]}
        """).jsonObject)
        assertEquals(InboxBulkResult(listOf("a", "b"), listOf("c"), listOf("d"), listOf("e")), result)
        assertEquals("已将 2 件标为已处理，1 件有新消息没标", bulkNotice(res, result))
        assertEquals("已将 2 件标为已处理", bulkNotice(res, result.copy(changedSince = emptyList())))
        assertEquals("有新消息进来，1 件没有标记", bulkNotice(res, result.copy(changed = emptyList())))
        assertNull(bulkNotice(res, InboxBulkResult(unchanged = listOf("c"))))
        // Clean up all says it in its own words.
        assertEquals("已清理 2 件，1 件有新消息没动", bulkNotice(res, result, cleaned = true))
        assertEquals("已清理 2 件", bulkNotice(res, result.copy(changedSince = emptyList()), cleaned = true))
    }

    @Test fun theHubsAnswerSaysWhatItFailedToWrite() {
        val result = InboxBulkResult.from(Json.parseToJsonElement("""
            {"changed":[],"unchanged":[],"skipped":[],"failed":["a"]}
        """).jsonObject)
        assertEquals(listOf("a"), result.failed)
    }

    @Test fun theConfirmationSaysHowManyOverWhat() {
        assertEquals("全部清理 · 12 件", bulkLabel(res, 12, null))
        assertEquals("清理当前筛选 · 3 件", bulkLabel(res, 3, "飞书"))
        assertEquals("将清理 12 件（整个收件箱）。之后进来的新消息不受影响，完成后可以撤销。", bulkConfirmText(res, 12, null))
        assertEquals("将清理 3 件（飞书 · 私聊）。之后进来的新消息不受影响，完成后可以撤销。", bulkConfirmText(res, 3, "飞书 · 私聊"))
    }
}
