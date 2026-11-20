package persona.feature.chat

import android.content.Context
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.Session
import persona.core.contracts.SessionCapabilities
import persona.core.contracts.SessionInfo
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.time.ZoneId

/** The old App's SessionInfoTest (81dc6928): tokens, rows and what can be copied. */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class ChatSessionInfoTest {
    private val res get() = ApplicationProvider.getApplicationContext<Context>().resources
    private val zone = ZoneId.of("Asia/Shanghai")
    private fun rows(session: Session, info: SessionInfo) = sessionInfoSections(res, session, info, zone).associate { section ->
        section.title to section.rows.associate { it.label to it.value }
    }

    // Token counts use the language's compact unit, as the compaction divider does (#4137).
    @Test fun tokensCountInTenThousandsInChinese() {
        assertEquals("950", formatTokens(950, res))
        assertEquals("9500", formatTokens(9_500, res))
        assertEquals("1.2 万", formatTokens(12_345, res))
        assertEquals("120 万", formatTokens(1_200_000, res))
    }

    @Config(qualifiers = "ja")
    @Test fun tokensCountInTenThousandsInJapanese() {
        assertEquals("950", formatTokens(950, res))
        assertEquals("4.8万", formatTokens(48_200, res))
        assertEquals("120万", formatTokens(1_200_000, res))
    }

    @Config(qualifiers = "en")
    @Test fun tokensCountInThousandsAndMillionsInEnglish() {
        assertEquals("950", formatTokens(950, res))
        assertEquals("12.3K", formatTokens(12_345, res))
        assertEquals("500K", formatTokens(500_000, res))
        assertEquals("999.9K", formatTokens(999_949, res))
        assertEquals("1M", formatTokens(999_950, res))
        assertEquals("1.2M", formatTokens(1_200_000, res))
    }

    @Test fun claudesSessionShowsWhatItRunsHowFarAndWhere() {
        val session = Session(machineId = "m", sessionId = "s1", cwd = "/home/me/code/shop", launcher = "claude", model = "opus",
            permissionMode = "acceptEdits", effort = "xhigh")
        val info = SessionInfo(sessionId = "s1", cwd = "/home/me/code/shop", model = "claude-opus-5", version = "2.1.320",
            contextTokens = 48_200, outputTokens = 12_800, turns = 6, compactions = 0, gitBranch = "main",
            startedAt = "2026-11-01T02:00:00Z")
        val all = rows(session, info)
        assertEquals("claude-opus-5", all.getValue("运行")["模型"])
        assertEquals("2.1.320", all.getValue("运行")["Claude Code"])
        assertEquals("允许编辑", all.getValue("运行")["权限"])
        assertEquals("很高", all.getValue("运行")["思考强度"])
        assertEquals("4.8 万 tokens", all.getValue("用量")["上下文"])
        assertFalse("压缩" in all.getValue("用量"))
        assertEquals("6 轮", all.getValue("用量")["对话"])
        assertEquals("11月1日 10:00", all.getValue("用量")["开始"])
        assertEquals("main", all.getValue("位置")["分支"])
        assertEquals("cd -- '/home/me/code/shop' && claude --resume 's1'", all.getValue("拷贝")["恢复命令"])
        assertEquals("s1", all.getValue("拷贝")["session id"])
        // Unknown counts and fields read as nothing rather than inventing them.
        val sparse = rows(session.copy(launcher = null), SessionInfo(sessionId = "s1", compactions = 2))
        assertEquals("2 次", sparse.getValue("用量")["压缩"])
        assertNull(sparse.getValue("运行")["启动器"])
        assertFalse("开始" in sparse.getValue("用量"))
    }

    @Test fun aCodexThreadResumesByItsThreadAndARuntimeNotAtAll() {
        val codex = Session(machineId = "m", sessionId = "c0de-opaque", cwd = "/srv/deploy", agent = "codex",
            sourceKind = "codex_app_server", sourceThreadId = "019a-thread",
            capabilities = SessionCapabilities(read = true, send = true, terminal = true))
        val all = rows(codex, SessionInfo(sessionId = "c0de-opaque", version = "0.153.0"))
        assertEquals("0.153.0", all.getValue("运行")["Codex"])
        assertEquals("cd -- '/srv/deploy' && codex resume '019a-thread'", all.getValue("拷贝")["恢复命令"])
        val runtime = Session(machineId = "m", sessionId = "session-1", agent = "dsh", agentLabelHint = "DSH", sourceKind = "runtime",
            capabilities = SessionCapabilities(read = true, send = true))
        val copied = rows(runtime, SessionInfo(sessionId = "session-1")).getValue("拷贝")
        assertFalse("恢复命令" in copied)
        assertTrue("session id" in copied)
    }

    @Test fun readOnlySourcesAndBlockedApprovalsSayWhy() {
        val history = Session(sessionId = "h", sourceKind = "codex_history", readOnlyReason = "仅查看：外部进程正在使用此会话",
            capabilities = SessionCapabilities(read = true))
        assertEquals("仅查看：外部进程正在使用此会话", readOnlyNote(res, history))
        assertEquals("此会话仅可查看", readOnlyNote(res, history.copy(sourceKind = "codex_history", readOnlyReason = null,
            capabilities = SessionCapabilities(read = true, send = true))))
        val live = history.copy(sourceKind = "codex_app_server", capabilities = SessionCapabilities(read = true, send = true, attach = true))
        assertTrue("实时接入" in approvalBlockedReason(res, live))
        assertFalse("实时接入" in approvalBlockedReason(res, history))
    }

    /**
     * #4339 C9 (iOS SessionInfoSheet): How it runs while an assistant conversation is on the Agent SDK or fell back from it,
     * the Codex route with why it is the terminal, and the share of the window left under the tokens, from the session list.
     */
    @Config(qualifiers = "en")
    @Test fun howItRunsTheCodexRouteAndTheContextLeft() {
        val info = SessionInfo(sessionId = "s1", contextTokens = 172_000)
        fun run(s: Session) = rows(s, info).getValue("Run")
        fun usage(s: Session) = sessionInfoSections(res, s, info, zone).single { it.title == "Usage" }.rows
        val assistant = Session(machineId = "m", sessionId = "s1", assistant = true)
        assertNull(run(assistant)["How it runs"])
        assertNull(run(assistant.copy(runtime = "cli"))["How it runs"])
        assertEquals("Agent SDK", run(assistant.copy(runtime = "agentsdk"))["How it runs"])
        assertEquals("Fell back to CLI: Node.js 22+ not found",
            run(assistant.copy(runtime = "agentsdk", runtimeFallback = " Node.js 22+ not found "))["How it runs"])
        // Its place: after the launcher, before the agent's version.
        assertEquals(listOf("Model", "Launcher", "How it runs", "Claude Code"),
            sessionInfoSections(res, assistant.copy(runtime = "agentsdk"), info, zone).first().rows.map { it.label }.take(4))

        val codex = Session(machineId = "m", sessionId = "s1", agent = "codex", codexRoute = "app_server")
        val route = sessionInfoSections(res, codex, info, zone).first().rows.single { it.label == "Connect" }
        assertEquals("app-server (structured)", route.value)
        assertNull(route.note)
        val terminal = sessionInfoSections(res, codex.copy(codexRoute = "terminal", codexRouteNote = "Its app-server is not running"), info, zone)
            .first().rows.single { it.label == "Connect" }
        assertEquals("Terminal (screen reading)", terminal.value)
        assertEquals("Its app-server is not running", terminal.note)
        assertNull(run(codex.copy(codexRoute = null))["Connect"])

        val left = usage(Session(machineId = "m", sessionId = "s1", contextTokens = 172_000, contextWindow = 200_000))
        assertEquals(listOf("Context", "Context remaining"), left.map { it.label }.take(2))
        assertEquals("14% left", left[1].value)
        assertEquals(ContextUsage(86, 172_000, 200_000), left[1].usage)
        assertTrue(left[1].usage!!.warns)
        assertEquals("172K / 200K", left[1].usage!!.detail)
        // A window the node does not know draws nothing.
        assertFalse(usage(Session(machineId = "m", sessionId = "s1", contextTokens = 48_000)).any { it.usage != null })
    }

    /** iOS `ContextUsageTests`: the share, the warning from 80% used, and the short numbers. */
    @Test fun contextUsageShareAndShortNumbers() {
        assertEquals(16, ContextUsage.of(31_000, 200_000)!!.used)
        assertFalse(ContextUsage.of(31_000, 200_000)!!.warns)
        assertTrue(ContextUsage.of(160_000, 200_000)!!.warns)
        assertFalse(ContextUsage.of(158_000, 200_000)!!.warns)
        assertEquals(100, ContextUsage.of(300_000, 200_000)!!.used)
        assertNull(ContextUsage.of(48_000, null))
        assertNull(ContextUsage.of(0, 200_000))
        assertNull(ContextUsage.of(null, null))
        assertEquals(listOf("172K", "1M", "1.3M", "800", "999", "999K", "1M", "1M", "2M"),
            listOf<Long>(172_000, 1_000_000, 1_260_000, 800, 999, 999_499, 999_500, 999_600, 1_960_000).map(ContextUsage::short))
    }

    /** #582 (iOS `infoShown` / `stateWord`): the ultracode row says on, off or unknown, for the user's running Claude Code on a hub that has it. */
    @Test fun ultracodeRowSaysOnOffOrUnknown() {
        val claude = Session(machineId = "m", sessionId = "s1", agent = "claude", status = "idle")
        fun row(s: Session, features: Set<String> = setOf("ultracode")) = sessionInfoSections(res, s, SessionInfo(sessionId = "s1"), zone, features = features)
            .flatMap { it.rows }.firstOrNull { it.label == "ultracode" }?.value
        assertEquals("开", row(claude.copy(ultracode = true)))
        assertEquals("关", row(claude.copy(ultracode = false)))
        assertEquals("未知", row(claude))
        assertEquals(null, row(claude, emptySet()))
        assertEquals("not a Persona conversation", null, row(claude.copy(assistant = true)))
        assertEquals("not Codex", null, row(claude.copy(agent = "codex")))
        assertEquals("not offline", null, row(claude.copy(state = "offline")))
    }

    /**
     * A Paseo provider's session that lists its modes (#8261): its mode in its own words, no mode reported being its own
     * approval, never Ask First; and no counts the node says nobody gives (`usage_unknown`), where zeros stood.
     */
    @Config(qualifiers = "en")
    @Test fun aProvidersSessionSaysItsOwnModeAndLeavesOutCountsNobodyGives() {
        val grok = Session(machineId = "m", sessionId = "paseo-agent-1", agent = "paseo", agentLabelHint = "Grok", launcher = "grok",
            status = "idle", sourceKind = "runtime", capabilities = SessionCapabilities(read = true, send = true, modes = false,
                permissionModes = listOf("default", "bypassPermissions")))
        val info = SessionInfo(sessionId = "paseo-agent-1", model = "grok-5", usageUnknown = true, startedAt = "2026-11-01T02:00:00Z")
        val all = rows(grok, info)
        assertEquals("Follow launcher", all.getValue("Run")["Permissions"])
        assertEquals("Full access", rows(grok.copy(permissionMode = "bypassPermissions"), info).getValue("Run")["Permissions"])
        val usage = all.getValue("Usage")
        for (label in listOf("Context", "Compactions", "Chat", "Generated")) assertFalse(label, label in usage)
        assertTrue("Started" in usage)
        // Counts it has are shown as before, zeros too; a session without the list keeps Claude Code's words.
        val counted = rows(grok, info.copy(usageUnknown = false, turns = 0)).getValue("Usage")
        assertTrue("Chat" in counted && "Context" in counted)
        assertEquals("Default permissions", rows(grok.copy(capabilities = grok.capabilities?.copy(permissionModes = null), permissionMode = "default"), info)
            .getValue("Run")["Permissions"])
    }
}
