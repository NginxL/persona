package persona.feature.inbox

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performTextInput
import androidx.compose.ui.test.onLast
import androidx.compose.ui.unit.dp
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import org.junit.Assert.assertEquals
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.After
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.api.AssistantManagerChoice
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.CurrentAssistant
import persona.core.contracts.api.HubManager
import persona.core.contracts.api.InboxEntry
import persona.core.contracts.api.ManagerRoster
import persona.core.designsystem.AppStyle
import persona.core.designsystem.LocalManagerRoster
import persona.core.designsystem.PersonaTheme
import persona.core.domain.inbox.InboxItem
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator
import java.io.File
import java.time.Instant
import persona.core.inbox.*

/**
 * The inbox with several managers (docs/assistant/managers.md "Clients"): the page lists the current assistant's items only
 * (unstamped ones are the default's), with no heading per manager and its own sections; Rules and Muted read that
 * assistant's lists, an offline one says so; in every style, light and dark. Written to `build/managers-shots/inbox-*.png`.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h1400dp-xhdpi")
class InboxManagersShotsTest {
    @get:Rule val rule = createComposeRule()

    private val roster = ManagerRoster(listOf(
        HubManager("assistant", "助手", online = true, isDefault = true, builtin = true),
        HubManager("m-3fa21c", "工作", online = true),
        HubManager("m-8b04d2", "家里", online = false, lastSeen = Instant.now().minusSeconds(3 * 3600).toString()),
        HubManager("m-c1e9f0", "测试", installing = "2026-11-05T06:07:00Z"),
    ), "assistant")

    private val records = listOf(
        InboxRecord(InboxItem("mail:a1:t:7", kind = "mail", title = "周报", sender = "示例同事", preview = "这周的进展和下周的计划，见附件。",
            tier = "action", updatedAt = "2026-11-04T09:30:00Z"), sourceLabel = "邮件", source = "mail"),
        InboxRecord(InboxItem("lark:f2:120", kind = "group", title = "项目群", sender = "示例同事", preview = "评审改到周四下午了",
            tier = "fyi", updatedAt = "2026-11-04T08:10:00Z"), sourceLabel = "飞书", feedId = "f2", count = 2, source = "lark", manager = "m-3fa21c"),
        InboxRecord(InboxItem("mail:a2:t:9", kind = "mail", title = "发布检查", sender = "示例同事", preview = "上线前还差两项检查",
            tier = "action", updatedAt = "2026-11-04T07:00:00Z"), sourceLabel = "邮件", source = "mail", manager = "m-3fa21c"),
        InboxRecord(InboxItem("mail:a3:t:2", kind = "mail", title = "物业通知", sender = "示例物业", preview = "本周六停水半天",
            tier = "fyi", updatedAt = "2026-11-04T06:00:00Z"), sourceLabel = "邮件", source = "mail", manager = "m-8b04d2"),
    )

    private class OneInbox(snapshot: InboxSnapshot) : InboxRepository {
        override val entries = MutableStateFlow(emptyList<InboxEntry>())
        override val snapshot = MutableStateFlow(snapshot)
        override val connectionState = flowOf(ConnectionState.Online)
        override suspend fun refresh() = Unit
        override fun conversation(feedId: String, startPosition: Long?) = flowOf(LarkConversation())
        override suspend fun loadOlder(feedId: String) = Unit
        override suspend fun refreshConversation(feedId: String) = Unit
        override suspend fun setDone(itemId: String, done: Boolean) = Unit
        override suspend fun readDraft(target: ReplyTarget): ReplyDraft? = null
        override suspend fun saveDraft(target: ReplyTarget, draft: ReplyDraft) = Unit
        override suspend fun reply(target: ReplyTarget, request: ReplyRequest) = Unit
        override suspend fun downloadFile(file: LarkPart.File): String = error("not used")
    }

    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }

    @After fun resetChoice() = AssistantManagerChoice.reset()

    private fun gone(vararg texts: String) = texts.forEach { assertTrue(it, rule.onAllNodesWithText(it).fetchSemanticsNodes().isEmpty()) }
    private fun see(vararg texts: String) = texts.forEach { assertTrue(it, rule.onAllNodesWithText(it).fetchSemanticsNodes().isNotEmpty()) }

    @Test fun theInboxShowsOnlyTheCurrentAssistantsItems() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var shown by mutableStateOf<ManagerRoster?>(roster)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                CompositionLocalProvider(LocalManagerRoster provides shown) {
                    Box(Modifier.testTag("shot").width(400.dp).height(1400.dp).background(PersonaTheme.palette.background)) {
                        OnAll { InboxScreen(OneInbox(InboxSnapshot(records = records)), navigator) }
                    }
                }
            }
        }
        val dir = File(System.getProperty("persona.shots") ?: "build/managers-shots").also { it.mkdirs() }
        fun shoot(name: String) {
            for (s in AppStyle.entries) for (d in listOf(false, true)) {
                style = s; dark = d
                rule.waitForIdle()
                val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
                File(dir, "inbox-$name-${s.key}-${if (d) "dark" else "light"}.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
            }
        }
        rule.waitForIdle()
        // The default assistant: its own (unstamped) item, no manager heading, none of the others' items.
        see("周报")
        gone("项目群", "发布检查", "物业通知", "助手", "工作", "家里", "测试")
        shoot("default")
        AssistantManagerChoice.pick("m-3fa21c")
        rule.waitForIdle()
        see("项目群", "发布检查")
        gone("周报", "物业通知", "助手", "工作")
        shoot("work")
        AssistantManagerChoice.pick("m-8b04d2")
        rule.waitForIdle()
        see("物业通知")
        gone("周报", "项目群", "发布检查", "家里")
        shoot("home")
        // One manager: every item is the default's, as before.
        AssistantManagerChoice.reset()
        shown = ManagerRoster(listOf(roster.managers.first()), "assistant")
        rule.waitForIdle()
        see("周报", "项目群", "发布检查", "物业通知")
        gone("助手")
    }

    /**
     * The hub's merged list leaves out an assistant that is offline (docs/protocol/protocol.md "Per-manager routing"): its
     * inbox says it is offline and when it last synced, not "empty" or how to set sources up. An online one with nothing,
     * and one assistant, read as before.
     */
    @Test fun anOfflineAssistantsInboxSaysSoInsteadOfEmpty() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var shown by mutableStateOf<ManagerRoster?>(roster)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                CompositionLocalProvider(LocalManagerRoster provides shown) {
                    Box(Modifier.testTag("shot").width(400.dp).height(900.dp).background(PersonaTheme.palette.background)) {
                        // 家里's items are not in the list: the hub could not ask it.
                        OnAll { InboxScreen(OneInbox(InboxSnapshot(records = records.filter { it.manager != "m-8b04d2" && it.manager != "m-3fa21c" })), navigator) }
                    }
                }
            }
        }
        val dir = File(System.getProperty("persona.shots") ?: "build/managers-shots").also { it.mkdirs() }
        AssistantManagerChoice.pick("m-8b04d2")
        rule.waitForIdle()
        see("家里 离线", "最后同步于 3 小时前。它上线后这里会更新。")
        gone("收件箱是空的", "都看完了")
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            rule.waitForIdle()
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            File(dir, "inbox-offline-${s.key}-${if (d) "dark" else "light"}.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
        // Online with nothing of its own: empty, as before.
        AssistantManagerChoice.pick("m-3fa21c")
        rule.waitForIdle()
        see("收件箱是空的")
        gone("家里 离线", "工作 离线")
        // One assistant, offline: as before (nothing to tell apart).
        AssistantManagerChoice.reset()
        shown = ManagerRoster(listOf(HubManager("assistant", "助手", online = false, isDefault = true, builtin = true)), "assistant")
        rule.waitForIdle()
        see("周报")
        gone("助手 离线")
    }

    /** An offline assistant whose items the list still has: a search that finds none says "no results", not "offline". */
    @Test fun aSearchOverAnOfflineAssistantsKeptItemsFindsNoneNotOffline() {
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true) {
                CompositionLocalProvider(LocalManagerRoster provides roster) {
                    Box(Modifier.testTag("shot").width(400.dp).height(900.dp).background(PersonaTheme.palette.background)) {
                        OnAll { InboxScreen(OneInbox(InboxSnapshot(records = records)), navigator) }
                    }
                }
            }
        }
        AssistantManagerChoice.pick("m-8b04d2")
        rule.waitForIdle()
        see("物业通知")
        rule.onNode(androidx.compose.ui.test.hasSetTextAction()).performTextInput("zzzz")
        rule.waitForIdle()
        see("没有找到")
        gone("家里 离线")
    }

    /** The same in English and Japanese (the words every client shares), Glass light: `inbox-offline-<language>.png`. */
    private fun offlineWords(language: String, vararg words: String) {
        AssistantManagerChoice.choose("hub", "m-8b04d2", "assistant")
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true) {
                CompositionLocalProvider(LocalManagerRoster provides roster) {
                    Box(Modifier.testTag("shot").width(400.dp).height(900.dp).background(PersonaTheme.palette.background)) {
                        OnAll { InboxScreen(OneInbox(InboxSnapshot()), navigator) }
                    }
                }
            }
        }
        rule.waitForIdle()
        see(*words)
        val dir = File(System.getProperty("persona.shots") ?: "build/managers-shots").also { it.mkdirs() }
        val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
        File(dir, "inbox-offline-$language.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }

    @Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
    @Test fun anOfflineAssistantsInboxInEnglish() = offlineWords("en", "家里 is offline", "Last synced 3 hours ago. This updates when it's back online.")

    @Config(sdk = [35], qualifiers = "ja-w400dp-h900dp-xhdpi")
    @Test fun anOfflineAssistantsInboxInJapanese() = offlineWords("ja", "家里 はオフラインです", "最終同期 3時間前。オンラインに戻ると更新されます。")

    /** Each manager's own rules and mutes: 工作's and the default's, 家里 offline. */
    private class ManagerLists : InboxRepository by OneInbox(InboxSnapshot()) {
        override suspend fun inboxPolicies(record: InboxRecord?, manager: String?) = when (manager) {
            "m-8b04d2" -> throw IllegalStateException("503")
            "m-3fa21c" -> listOf(InboxPolicy("p1", "mail", "邮件", "sender", "boss@example.com", "王经理", InboxPolicyTier.Action, InboxPolicyTwin.First,
                "2026-11-04T09:00:00Z"))
            else -> listOf(InboxPolicy("p1", "github", "GitHub", "repo", "example-org/docs", "example-org/docs", InboxPolicyTier.Noise, createdAt = "2026-11-03T09:00:00Z"))
        }
        override suspend fun inboxMutes(manager: String?) = when (manager) {
            "m-8b04d2" -> throw IllegalStateException("503")
            "m-3fa21c" -> listOf(InboxMute("m1", "github", "GitHub", "repo", "example-org/example-repo", "example-org/example-repo", "2026-11-04T09:00:00Z"))
            else -> listOf(InboxMute("m1", "mail", "邮件", "sender", "news@example.com", "news@example.com", "2026-11-03T09:00:00Z"))
        }
    }

    @Test fun rulesAndMutedAreTheCurrentAssistantsOwn() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var rules by mutableStateOf(true)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                CompositionLocalProvider(LocalManagerRoster provides roster) {
                    Box(Modifier.testTag("shot").width(400.dp).height(900.dp).background(PersonaTheme.palette.background)) {
                        if (rules) InboxRulesSheet(ManagerLists(), onDismiss = {}) else InboxMutesSheet(ManagerLists(), onDismiss = {})
                    }
                }
            }
        }
        val dir = File(System.getProperty("persona.shots") ?: "build/managers-shots").also { it.mkdirs() }
        for (r in listOf(true, false)) {
            AssistantManagerChoice.reset()
            rules = r
            rule.waitForIdle()
            val (mine, theirs, away) = if (r) Triple("example-org/docs", "王经理", "离线中：它回来后这里会显示它的规则。")
                else Triple("news@example.com", "example-org/example-repo", "离线中：它回来后这里会显示它的静音。")
            // The default's own, with no heading and none of the others'.
            see(mine)
            gone(theirs, away, "助手", "工作", "家里")
            fun shoot(name: String) {
                for (s in AppStyle.entries) for (d in listOf(false, true)) {
                    style = s; dark = d
                    rule.waitForIdle()
                    val bitmap = rule.onAllNodes(isDialog()).onLast().captureToImage().asAndroidBitmap()
                    File(dir, "inbox-${if (r) "rules" else "muted"}-$name-${s.key}-${if (d) "dark" else "light"}.png").outputStream()
                        .use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
                }
            }
            shoot("default")
            AssistantManagerChoice.pick("m-3fa21c")
            rule.waitForIdle()
            see(theirs)
            gone(mine, away)
            shoot("work")
            // Offline: its list can't be read until it is back, and the sheet says so.
            AssistantManagerChoice.pick("m-8b04d2")
            rule.waitForIdle()
            see(away)
            gone(mine, theirs)
            shoot("offline")
        }
    }

    @Test fun theSnapshotIsTheCurrentAssistantsShare() {
        fun assistant(manager: String?) = CurrentAssistant(roster, AssistantManagerChoice.Choice("hub", manager, "assistant"))
        val snapshot = InboxSnapshot(records = records)
        // An item with no stamp is the default's.
        assertEquals(listOf("mail:a1:t:7"), snapshot.ofAssistant(assistant(null)).records.map { it.item.id })
        assertEquals(listOf("mail:a1:t:7"), snapshot.ofAssistant(assistant("assistant")).records.map { it.item.id })
        assertEquals(listOf("lark:f2:120", "mail:a2:t:9"), snapshot.ofAssistant(assistant("m-3fa21c")).records.map { it.item.id })
        assertEquals(listOf("mail:a3:t:2"), snapshot.ofAssistant(assistant("m-8b04d2")).records.map { it.item.id })
        // One the roster does not list (deleted, still installing) is the default's, so no item is left on no page.
        val strays = InboxSnapshot(records = records + InboxRecord(InboxItem("mail:a4:t:1", kind = "mail", title = "旧的"), source = "mail", manager = "m-gone")
            + InboxRecord(InboxItem("mail:a5:t:1", kind = "mail", title = "装着"), source = "mail", manager = "m-c1e9f0"))
        assertEquals(listOf("mail:a1:t:7", "mail:a4:t:1", "mail:a5:t:1"), strays.ofAssistant(assistant(null)).records.map { it.item.id })
        // Without a roster the choice's own default decides; nothing to cut with the default shown and nothing stamped.
        val bare = CurrentAssistant(null, AssistantManagerChoice.Choice("hub", null, "assistant"))
        assertEquals(listOf("mail:a1:t:7"), snapshot.ofAssistant(bare).records.map { it.item.id })
        val plain = InboxSnapshot(records = records.take(1))
        assertSame(plain, plain.ofAssistant(bare))
    }
}
