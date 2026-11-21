package persona.feature.chat

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.*
import androidx.compose.runtime.*
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.test.ext.junit.runners.AndroidJUnit4
import coil3.ImageLoader
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.serialization.json.JsonObject
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.designsystem.*
import persona.core.domain.tasks.*
import java.io.File

/**
 * #4339 C13: the open plan card in Persona's conversation, as iOS's `FeedLine` draws it (Glass): the plan's tasks, where each
 * would run, "Start these directly", Start, Edit and Don't start, then the dispatched line once the hub started it and
 * "Not started · N tasks" once it was dismissed. Brief and Board keep their plans behind the trays. Native JVM captures of the
 * real renderers, with synthetic data; the words in the app's three languages.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1200dp-xhdpi")
class PlanCardConversationTest {
    @get:Rule val rule = createComposeRule()
    private val session = SessionKey("computer", "main")
    private fun at(n: Int) = "2026-11-12T10:00:${n.toString().padStart(2, '0')}Z"
    private val notes = PlanTask("Write the release notes", prompt = "Draft them from this week's pull requests", machine = "mac",
        cwd = "/w/shop", permission = "acceptEdits", why = "It only touches the docs folder")
    private val crash = PlanTask("Look into the crash report", machine = "box", cwd = "/srv/app", model = "opus", readonly = true)
    private val plan = TaskPlan("plan", "main", listOf(notes, crash), "open", at(3))
    private val single = TaskPlan("single", "main", listOf(PlanTask("Tidy the changelog", machine = "mac", cwd = "/w/shop")), "open", at(4))
    private val transcript = listOf(
        Item("ask", 1, ts = at(0), role = "user", kind = "text", text = "Can you write the release notes and look into the crash?"),
        Item("answer", 2, ts = at(1), role = "assistant", kind = "text", text = "I made a plan; start it when you are ready."),
    )
    private val feed = listOf(FeedItem("plan-feed", "main", at(3), "plan", "plan"))
    private val machines = mapOf("mac" to "Work laptop", "box" to "Build server")

    /** What the hub is asked, and how it answers: Start comes back started with its to-dos made, Don't start dismissed. */
    private class Api(val plans: List<TaskPlan>) : TasksApi {
        var started: Pair<String, Boolean>? = null
        var dismissed: String? = null
        var made = emptyList<Todo>()
        override suspend fun todos() = made
        override suspend fun feed(conversation: String, since: String?) = FeedResponse(emptyList(), plans.associateBy { it.id })
        override suspend fun startPlan(id: String, remember: Boolean): TaskPlan {
            started = id to remember
            val p = plans.first { it.id == id }
            made = p.tasks.mapIndexed { n, t -> Todo("todo-$n", t.title, origin = "main", planId = id, createdAt = "2026-11-12T10:00:30Z") }
            return p.copy(state = "started", tasks = p.tasks.mapIndexed { n, t -> t.copy(todoId = "todo-$n") })
        }
        override suspend fun dismissPlan(id: String): TaskPlan { dismissed = id; return plans.first { it.id == id }.copy(state = "dismissed") }
        override suspend fun retryTask(todoId: String): Todo = error("unused")
        override suspend fun removeTask(todoId: String) = Unit
        override suspend fun route(conversation: String, todoId: String, text: String): FeedItem = error("unused")
        override suspend fun today(conversation: String): FeedItem = error("unused")
        override suspend fun mainConversation(): String? = null
        override suspend fun inboxReply(itemId: String, text: String, card: String?) = Unit
    }

    private class Connection : HubConnection {
        override val state = MutableStateFlow(ConnectionState.WaitingForNetwork)
        override val events = MutableSharedFlow<ServerEvent>()
        override fun reconnect() = Unit
        override fun send(message: JsonObject) = true
    }

    private lateinit var api: Api

    /** [alone]: that plan's card by itself, given as the tray's sheet captured it, not the conversation's lines. */
    private fun show(style: AppStyle, dark: MutableState<Boolean> = mutableStateOf(false), plans: List<TaskPlan> = listOf(plan),
        items: List<FeedItem> = feed, alone: TaskPlan? = null) {
        api = Api(plans)
        val scope = CoroutineScope(SupervisorJob() + Dispatchers.Unconfined)
        val store = TasksStore(api, Connection(), scope)
        store.load("main")
        rule.mainClock.autoAdvance = false
        rule.setContent {
            PersonaTheme(darkTheme = dark.value, reduceMotion = true, style = style) {
                val context = LocalContext.current
                val loader = remember { ImageLoader.Builder(context).build() }
                DisposableEffect(loader) { onDispose { loader.shutdown(); scope.cancel() } }
                val todos by store.todos.collectAsState()
                val current by store.plans.collectAsState()
                val host = AssistantTaskHost(store, "main", emptyList(), { machines[it] ?: it }, {}, { throw it }, {})
                val original = ChatTimeline().lines(transcript, false, emptySet()).lines
                val lines = AssistantTaskCards.lines(AssistantConversation.project(original, transcript).foreground, items, current, todos,
                    inline = style == AppStyle.Glass)
                CompositionLocalProvider(LocalAssistantTaskHost provides host, LocalHomeChrome provides HomeChrome(
                    listOf(NavItem("assistant", "Assistant", persona.core.designsystem.icons.PhIcons.House)), "assistant", onSelect = {}, leading = {})) {
                    Column(Modifier.fillMaxWidth().testTag("plan-shot").background(PersonaTheme.palette.background)
                        .padding(Tokens.Space.l), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                        if (alone != null) PlanCard(alone, host)
                        else lines.forEach { line -> key(line.contentKey) {
                            ChatLineContent(line, session, loader, null, onToggle = {}, onFold = {}, folding = emptySet(),
                                foldErrors = emptyMap(), onFullText = {}, style = ChatRenderStyle.Assistant)
                        } }
                    }
                }
            }
        }
        settle()
    }

    private fun settle() {
        Snapshot.sendApplyNotifications()
        repeat(4) { rule.mainClock.advanceTimeByFrame() }
        rule.mainClock.advanceTimeBy(500)
    }

    /** performScrollTo needs real choreographer frames; a frozen clock can otherwise fill the Robolectric heap. */
    private fun scrollIntoView(node: SemanticsNodeInteraction) {
        rule.mainClock.autoAdvance = true
        try {
            node.performScrollTo()
            rule.waitForIdle()
        } finally {
            rule.mainClock.autoAdvance = false
        }
        settle()
    }

    private fun capture(name: String): Int {
        val directory = File(System.getProperty("persona.shots") ?: "build/plan-card-shots").also { it.mkdirs() }
        val bitmap = rule.onNodeWithTag("plan-shot").captureToImage().asAndroidBitmap()
        File(directory, "$name.png").outputStream().use { assertTrue(bitmap.compress(Bitmap.CompressFormat.PNG, 100, it)) }
        return bitmap.getPixel(2, 2)
    }

    @Test fun glassDrawsTheOpenPlanAsIOSDoesLightAndDark() {
        val dark = mutableStateOf(false)
        show(AppStyle.Glass, dark)
        val colors = mutableListOf<Int>()
        for (night in listOf(false, true)) {
            dark.value = night; settle()
            // iOS `PlanCard.open`: what it does, then each task with where it would run and why
            rule.onNodeWithTag("plan-heading").assertTextEquals("Split into 2 tasks, started like this")
            rule.onNodeWithText("Write the release notes").assertIsDisplayed()
            rule.onNodeWithText("Look into the crash report").assertIsDisplayed()
            // the number disc only when there are several tasks
            rule.onNodeWithText("1").assertIsDisplayed()
            rule.onNodeWithText("2").assertIsDisplayed()
            for (chip in listOf("Work laptop", "shop", "Default model", "Auto accept edits", "Build server", "app", "Opus", "Read-only requested"))
                rule.onNodeWithText(chip).assertIsDisplayed()
            rule.onNodeWithText("It only touches the docs folder").assertIsDisplayed()
            // the choice, then the three actions in iOS's order
            rule.onNodeWithTag("plan-directly").assertIsDisplayed().assertIsOff()
            rule.onNodeWithText("Start directly here and in subfolders").assertIsDisplayed()
            rule.onNodeWithTag("plan-start-plan").assertTextEquals("Start 2 tasks").assertIsEnabled()
            rule.onNodeWithTag("plan-edit-plan").assertTextEquals("Edit")
            rule.onNodeWithTag("plan-dismiss-plan").assertTextEquals("Don't start")
            colors += capture("plan-glass-${if (night) "dark" else "light"}")
        }
        assertNotEquals("dark capture must differ from light", colors[0], colors[1])
    }

    @Test fun glassDrawsASingleTaskPlanWithoutNumbersAndAPlanThatWasNotStarted() {
        val dismissed = TaskPlan("off", "main", listOf(PlanTask("Rename the old branches", machine = "mac", cwd = "/w/shop"),
            PlanTask("Delete the stale tags", machine = "mac", cwd = "/w/shop")), "dismissed", at(2))
        show(AppStyle.Glass, plans = listOf(single, dismissed), items = listOf(FeedItem("off-feed", "main", at(2), "plan", "off"),
            FeedItem("single-feed", "main", at(4), "plan", "single")))
        rule.onNodeWithTag("plan-heading").assertTextEquals("Start like this")
        rule.onNodeWithTag("plan-start-single").assertTextEquals("Start")
        // no number disc for one task
        rule.onAllNodesWithText("1").assertCountEquals(0)
        // iOS's `settled`: a cross and "Not started · N tasks", no buttons for it
        rule.onNodeWithTag("plan-not-started").assertIsDisplayed()
        rule.onNodeWithText("Not started · 2 tasks").assertIsDisplayed()
        rule.onAllNodesWithTag("plan-start-off").assertCountEquals(0)
        capture("plan-glass-single-and-not-started")
    }

    @Test fun startAsksTheHubOnceAndTheDispatchedLineTakesTheCardsPlace() {
        show(AppStyle.Glass)
        rule.onNodeWithTag("plan-start-plan").performClick(); settle()
        assertEquals("plan" to false, api.started)
        rule.onAllNodesWithTag("plan-card-plan").assertCountEquals(0)
        rule.onNodeWithTag("assistant-dispatch-hint").assertIsDisplayed()
        rule.onNodeWithText("Write the release notes").assertIsDisplayed()
        rule.onNodeWithText("Look into the crash report").assertIsDisplayed()
        capture("plan-glass-dispatched")
    }

    @Test fun aCardLeftOpenFollowsItsPlanOnceStartedInsteadOfOfferingStartAgain() {
        // The tray's sheet keeps the plan it was opened with; the card reads the store's latest of it.
        show(AppStyle.Glass, alone = plan)
        rule.onNodeWithTag("plan-start-plan").performClick(); settle()
        assertEquals("plan" to false, api.started)
        rule.onAllNodesWithTag("plan-start-plan").assertCountEquals(0)
        rule.onNodeWithText("✓ Started 2 tasks").assertIsDisplayed()
    }

    private val named = TaskPlan("named", "main", listOf(
        PlanTask("Look at the raw id", machine = "mac", cwd = "/w/a", model = "claude-opus-4-8[1m]"),
        PlanTask("Start on a launcher", machine = "mac", cwd = "/w/b", launcher = "claude-kimi"),
        PlanTask("Start on the default", machine = "mac", cwd = "/w/c"),
    ), "open", at(5))

    private val tiered = JsonCodec.decodeFromString(TaskPlan.serializer(), """{
        "id":"tiered","conversation":"main","state":"open","created_at":"2026-11-12T10:00:06Z",
        "tasks":[
            {"title":"Use a preset","machine":"mac","cwd":"/w/a","tier":"review"},
            {"title":"Use a preset on a launcher","machine":"mac","cwd":"/w/b","launcher":"claude-kimi","tier":"review"},
            {"title":"An explicit model wins","machine":"mac","cwd":"/w/c","launcher":"claude-kimi","tier":"review","model":"claude-opus-4-8[1m]"},
            {"title":"Keep the default","machine":"mac","cwd":"/w/d"}
        ]
    }""")

    private fun tierNamesTheModels() {
        rule.onNodeWithText("Preset review").assertIsDisplayed()
        rule.onNodeWithText("Claude Kimi · Preset review").assertIsDisplayed()
        rule.onNodeWithText("Opus 4.8").assertIsDisplayed()
        rule.onNodeWithText("Default model").assertIsDisplayed()
        // Only the two tasks without an explicit model show their preset; the launcher does not replace it.
        rule.onAllNodesWithText("Preset review", substring = true).assertCountEquals(2)
    }

    private fun everyStyleNamesTiers(style: AppStyle, largeText: Boolean = false) {
        val dark = mutableStateOf(false)
        if (style == AppStyle.Glass) show(style, dark, plans = listOf(tiered),
            items = listOf(FeedItem("tiered-feed", "main", at(6), "plan", "tiered")))
        else show(style, dark, plans = listOf(tiered), alone = tiered)
        val colors = mutableListOf<Int>()
        for (night in listOf(false, true)) {
            dark.value = night; settle()
            tierNamesTheModels()
            val size = if (largeText) "-large" else ""
            colors += capture("plan-tiers-${style.name.lowercase()}-${if (night) "dark" else "light"}$size")
        }
        assertNotEquals("dark capture must differ from light", colors[0], colors[1])
    }

    @Test fun glassNamesTiersWithLaunchersAndKeepsModelPrecedence() = everyStyleNamesTiers(AppStyle.Glass)
    @Test fun briefNamesTiersWithLaunchersAndKeepsModelPrecedence() = everyStyleNamesTiers(AppStyle.Brief)
    @Test fun boardNamesTiersWithLaunchersAndKeepsModelPrecedence() = everyStyleNamesTiers(AppStyle.Board)

    @Config(sdk = [35], qualifiers = "en-w400dp-h2400dp-xhdpi", fontScale = 2f)
    @Test fun glassNamesTiersAtLargestText() = everyStyleNamesTiers(AppStyle.Glass, largeText = true)

    @Config(sdk = [35], qualifiers = "en-w400dp-h2400dp-xhdpi", fontScale = 2f)
    @Test fun briefNamesTiersAtLargestText() = everyStyleNamesTiers(AppStyle.Brief, largeText = true)

    @Config(sdk = [35], qualifiers = "en-w400dp-h2400dp-xhdpi", fontScale = 2f)
    @Test fun boardNamesTiersAtLargestText() = everyStyleNamesTiers(AppStyle.Board, largeText = true)

    private fun namesTheModels() {
        rule.onNodeWithText("Opus 4.8").assertIsDisplayed()
        rule.onNodeWithText("Claude Kimi").assertIsDisplayed()
        rule.onNodeWithText("Default model").assertIsDisplayed()
        // never the raw id on the card (DESIGN_SYSTEM.md 5.6); iOS writes it as it is
        rule.onAllNodesWithText("claude-opus-4-8[1m]").assertCountEquals(0)
        rule.onAllNodesWithText("claude-kimi").assertCountEquals(0)
    }

    @Test fun glassNamesAModelByItsShortNameAndTheEditFieldKeepsTheRawValue() {
        show(AppStyle.Glass, plans = listOf(named), items = listOf(FeedItem("named-feed", "main", at(5), "plan", "named")))
        namesTheModels()
        // what the task starts with stays as it is: the field holds the raw id, which is what Start sends
        rule.onNodeWithTag("plan-edit-named").performClick(); settle()
        rule.onNode(hasText("claude-opus-4-8[1m]") and hasSetTextAction()).assertExists()
    }

    private fun aTraySheetsCardNamesTheModelsToo(style: AppStyle) {
        show(style, plans = listOf(named), alone = named)
        namesTheModels()
    }

    @Test fun briefsCardNamesTheModelsToo() = aTraySheetsCardNamesTheModelsToo(AppStyle.Brief)
    @Test fun boardsCardNamesTheModelsToo() = aTraySheetsCardNamesTheModelsToo(AppStyle.Board)

    @Test fun startTheseDirectlyGoesWithStart() {
        show(AppStyle.Glass)
        rule.onNodeWithTag("plan-directly").performClick(); settle()
        rule.onNodeWithTag("plan-directly").assertIsOn()
        rule.onNodeWithTag("plan-start-plan").performClick(); settle()
        assertEquals("plan" to true, api.started)
    }

    @Test fun dontStartLeavesTheNotStartedLine() {
        show(AppStyle.Glass)
        rule.onNodeWithTag("plan-dismiss-plan").performClick(); settle()
        assertEquals("plan", api.dismissed)
        rule.onAllNodesWithTag("plan-card-plan").assertCountEquals(0)
        rule.onNodeWithText("Not started · 2 tasks").assertIsDisplayed()
    }

    @Test fun editOpensTheEditSheetBeforeAnythingStarts() {
        show(AppStyle.Glass)
        rule.onNodeWithTag("plan-edit-plan").performClick(); settle()
        rule.onNode(hasText("Edit plan")).assertExists()
        assertNull(api.started)
    }

    private fun bounds(tag: String) = rule.onNodeWithTag(tag).getBoundsInRoot()

    @Test fun theThreeActionsShareARowWhileStartsWholeLabelFits() {
        show(AppStyle.Glass)
        val start = bounds("plan-start-plan"); val edit = bounds("plan-edit-plan"); val dismiss = bounds("plan-dismiss-plan")
        assertTrue("in iOS's order, side by side", start.right <= edit.left && edit.right <= dismiss.left)
        assertEquals("one row", start.top.value, edit.top.value, 1f)
        assertEquals("one row", start.top.value, dismiss.top.value, 1f)
    }

    @Config(qualifiers = "ja-w360dp-h1200dp-xhdpi")
    @Test fun startHasARowOfItsOwnWhenItsLabelWouldBeCutBesideTheOthers() {
        // At 14sp the three Japanese labels fit in a 400 dp card; 360 dp leaves too little room, so Start stacks instead of being cut.
        show(AppStyle.Glass)
        val start = bounds("plan-start-plan"); val edit = bounds("plan-edit-plan"); val dismiss = bounds("plan-dismiss-plan")
        assertTrue("Start above the other two", edit.top >= start.bottom && dismiss.top >= start.bottom)
        assertEquals("the other two share a row", edit.top.value, dismiss.top.value, 1f)
        assertTrue("in order", edit.right <= dismiss.left)
        assertEquals("Start is the card's whole width", (start.right - start.left).value, (dismiss.right - edit.left).value, 1f)
        capture("plan-glass-ja-stacked")
    }

    @Config(qualifiers = "en-w320dp-h1200dp-xhdpi")
    @Test fun aNarrowPhoneKeepsEveryLabelWhole() {
        show(AppStyle.Glass)
        val start = bounds("plan-start-plan"); val edit = bounds("plan-edit-plan"); val dismiss = bounds("plan-dismiss-plan")
        // wherever they sit, none overlaps another and all stay inside the card
        assertTrue("Start and Edit do not overlap", start.right <= edit.left || start.bottom <= edit.top)
        assertTrue("Edit and Don't start do not overlap", edit.right <= dismiss.left || edit.bottom <= dismiss.top)
        assertTrue(dismiss.right.value <= 320f)
        capture("plan-glass-320")
    }

    private fun keepsThePlanBehindTheTrays(style: AppStyle) {
        show(style)
        rule.onAllNodesWithTag("plan-card-plan").assertCountEquals(0)
        rule.onNodeWithText("Split into 2 tasks, started like this").assertDoesNotExist()
        rule.onNodeWithText("Start 2 tasks").assertDoesNotExist()
        // the conversation is its two messages alone
        rule.onNodeWithText("I made a plan; start it when you are ready.").assertIsDisplayed()
        capture("plan-${style.name.lowercase()}-light")
    }

    @Test fun briefKeepsThePlanBehindTheTrays() = keepsThePlanBehindTheTrays(AppStyle.Brief)
    @Test fun boardKeepsThePlanBehindTheTrays() = keepsThePlanBehindTheTrays(AppStyle.Board)

    private fun words(language: String, heading: String, start: String, edit: String, dismiss: String, directly: String) {
        show(AppStyle.Glass)
        rule.onNodeWithTag("plan-heading").assertTextEquals(heading)
        rule.onNodeWithTag("plan-start-plan").assertTextEquals(start)
        rule.onNodeWithTag("plan-edit-plan").assertTextEquals(edit)
        rule.onNodeWithTag("plan-dismiss-plan").assertTextEquals(dismiss)
        rule.onNodeWithText(directly).assertIsDisplayed()
        capture("plan-glass-$language")
    }

    @Config(qualifiers = "zh-w400dp-h1200dp-xhdpi")
    @Test fun simplifiedChinese() = words("zh", "拆成 2 件事，这样开", "开始 2 个任务", "改一下", "不开", "这些目录及子目录以后直接开")

    @Config(qualifiers = "ja-w400dp-h1200dp-xhdpi")
    @Test fun japanese() = words("ja", "2件に分けて、この内容で開始", "2件のタスクを開始", "編集", "開始しない", "今後このフォルダ以下はすぐに開始")

    private val ownerRequest = "Please fix the sign-in crash before Friday, keep the existing session behavior, and add a landscape regression test."
    private val reconstructedHandover = "Take over the existing branch, review the earlier agent's notes, and finish the repair without starting the work again."

    /** One real open plan carries every brief provenance and one older task with none. All effective-scope values are distinct. */
    private val briefMatrix = JsonCodec.decodeFromString(TaskPlan.serializer(), """{
        "id":"brief-matrix","conversation":"main","state":"open","created_at":"2026-11-12T10:00:07Z",
        "tasks":[
            {"title":"Fix the sign-in crash","machine":"box","cwd":"/srv/owner","brief":{
                "created_at":"2026-11-03T12:01:00Z","started_by":"owner",
                "request":{"text":"$ownerRequest","at":"2026-11-03T12:00:00Z","conversation":"main","message_id":"message-owner"},
                "goal":"Landscape sign-in works without crashing",
                "done_when":["Original owner criterion"],"criteria":["Current landscape regression passes"],
                "expect_minutes":30,"expect_minutes_now":150,"out_of_bounds":["Do not change payments"]}},
            {"title":"Resume the worker repair","machine":"box","cwd":"/srv/worker","brief":{
                "created_at":"2026-11-04T09:30:00Z","started_by":"assistant","reason":"The earlier repair stopped",
                "goal":"The worker completes its repair","done_when":["The worker regression passes"],
                "expect_minutes":45,"out_of_bounds":["Do not deploy the worker"]}},
            {"title":"Finish the older repair","machine":"box","cwd":"/srv/older","brief":{
                "created_at":"2026-11-05T08:00:00Z","started_by":"unknown","reconstructed":true,
                "handover":"$reconstructedHandover","goal":"The older branch is finished",
                "done_when":["The older branch passes its checks"],"expect_minutes":20,
                "out_of_bounds":["Do not rewrite history"]}},
            {"title":"Keep the existing card","machine":"box","cwd":"/srv/legacy","readonly":true,
                "why":"Existing plan details stay visible"}
        ]
    }""")

    private fun showBriefMatrix(style: AppStyle, dark: MutableState<Boolean>) {
        if (style == AppStyle.Glass) show(style, dark, plans = listOf(briefMatrix),
            items = listOf(FeedItem("brief-matrix-feed", "main", at(7), "plan", "brief-matrix")))
        else show(style, dark, plans = listOf(briefMatrix), alone = briefMatrix)
    }

    /** Folded: provenance and a summary only. The fourth task proves that no brief leaves the existing task/card intact. */
    private fun briefMatrixIsFolded(style: AppStyle) {
        rule.onAllNodesWithTag("plan-brief").assertCountEquals(3)
        rule.onAllNodesWithTag("plan-brief-toggle").assertCountEquals(3)
        rule.onAllNodesWithText("Show All").assertCountEquals(3)
        rule.onNode(hasText("You asked on", substring = true) and hasText("2026", substring = true)).assertIsDisplayed()
        rule.onNodeWithText(ownerRequest, substring = true).assertIsDisplayed()
        rule.onNode(hasText("Started by the assistant on", substring = true) and hasText("2026", substring = true)).assertIsDisplayed()
        rule.onNodeWithText("The earlier repair stopped", substring = true).assertIsDisplayed()
        rule.onNodeWithText("Reconstructed: the original request was not recorded").assertIsDisplayed()
        rule.onNodeWithText("Hand-over Prompt").assertIsDisplayed()
        rule.onNodeWithText(reconstructedHandover, substring = true).assertIsDisplayed()
        // Current values win for the owner; the assistant's original criteria/time are its fallback.
        rule.onNodeWithText("1 criterion · about 2.5 h · 1 out of bounds").assertIsDisplayed()
        rule.onNodeWithText("1 criterion · about 45 min · 1 out of bounds").assertIsDisplayed()
        rule.onNodeWithText("1 criterion · about 20 min · 1 out of bounds").assertIsDisplayed()
        rule.onAllNodesWithText("Goal").assertCountEquals(0)
        rule.onAllNodesWithText("Done When").assertCountEquals(0)
        rule.onAllNodesWithText("Expected Time").assertCountEquals(0)
        rule.onAllNodesWithText("Out of Bounds").assertCountEquals(0)
        rule.onAllNodesWithText("Current landscape regression passes").assertCountEquals(0)
        rule.onAllNodesWithText("The worker regression passes").assertCountEquals(0)
        rule.onAllNodesWithText("Do not change payments").assertCountEquals(0)
        rule.onAllNodesWithText("Original owner criterion").assertCountEquals(0)
        // The task without a brief keeps its old content and the plan keeps all of its controls; there is no fourth disclosure.
        rule.onNodeWithText("Keep the existing card", substring = true).assertIsDisplayed()
        rule.onNodeWithText("Existing plan details stay visible").assertIsDisplayed()
        rule.onNodeWithText("legacy").assertIsDisplayed()
        rule.onNodeWithText("Read-only requested").let { chip ->
            if (style == AppStyle.Glass) chip.assertIsDisplayed()
            else {
                // Brief/Board keep their old horizontal chip row; exercise the off-screen chip without leaving it scrolled.
                scrollIntoView(chip)
                chip.assertIsDisplayed()
                scrollIntoView(rule.onNodeWithText("legacy"))
                rule.onNodeWithText("legacy").assertIsDisplayed()
            }
        }
        rule.onNodeWithText("Start directly here and in subfolders").assertIsDisplayed()
        rule.onNodeWithText("Start 4 tasks").assertIsEnabled()
        rule.onNodeWithText("Edit").assertIsEnabled()
        rule.onNodeWithText("Don't start").assertIsEnabled()
    }

    private fun briefMatrixIsExpanded() {
        rule.onAllNodesWithTag("plan-brief-toggle").assertCountEquals(3)
        rule.onAllNodesWithText("Show less").assertCountEquals(3)
        for (label in listOf("Goal", "Done When", "Expected Time", "Out of Bounds"))
            rule.onAllNodesWithText(label).assertCountEquals(3)
        for (text in listOf(
            "Landscape sign-in works without crashing", "Current landscape regression passes", "About 2.5 h", "Do not change payments",
            "The worker completes its repair", "The worker regression passes", "About 45 min", "Do not deploy the worker",
            "The older branch is finished", "The older branch passes its checks", "About 20 min", "Do not rewrite history"))
            rule.onNodeWithText(text).assertIsDisplayed()
        // Scope changes never resurrect the owner's original criterion or original clock in the effective fields.
        rule.onAllNodesWithText("Original owner criterion").assertCountEquals(0)
        rule.onAllNodesWithText("About 30 min").assertCountEquals(0)
        rule.onNodeWithText("Hand-over Prompt").assertIsDisplayed()
        rule.onNodeWithText(reconstructedHandover, substring = true).assertIsDisplayed()
    }

    private fun briefTextHeight(text: String): Float {
        val bounds = rule.onNodeWithText(text, substring = true).getBoundsInRoot()
        return (bounds.bottom - bounds.top).value
    }

    /** Leave the legacy task's horizontal chip row at its natural leading position for review captures. */
    private fun captureBriefMatrix(style: AppStyle, name: String): Int {
        if (style != AppStyle.Glass) scrollIntoView(rule.onAllNodesWithText("Build server")[3])
        return capture(name)
    }

    /** The real PlanCard in every style and theme, with one disclosure per brief; captures include folded and expanded forms. */
    private fun everyStyleBriefMatrix(style: AppStyle, largeText: Boolean = false) {
        val dark = mutableStateOf(false)
        showBriefMatrix(style, dark)
        val foldedColors = mutableListOf<Int>()
        for (night in listOf(false, true)) {
            dark.value = night; settle()
            briefMatrixIsFolded(style)
            val theme = if (night) "dark" else "light"
            val size = if (largeText) "-large" else ""
            if (!largeText) {
                val ownerFolded = briefTextHeight(ownerRequest)
                val handoverFolded = briefTextHeight(reconstructedHandover)
                foldedColors += captureBriefMatrix(style, "plan-briefs-${style.name.lowercase()}-$theme-folded$size")
                for (n in 0 until 3) {
                    rule.onAllNodesWithTag("plan-brief-toggle")[n].assertTextEquals("Show All").performClick()
                    settle()
                }
                briefMatrixIsExpanded()
                assertTrue("Show All reveals the whole owner request", briefTextHeight(ownerRequest) > ownerFolded)
                assertTrue("Show All reveals the whole hand-over", briefTextHeight(reconstructedHandover) > handoverFolded)
                captureBriefMatrix(style, "plan-briefs-${style.name.lowercase()}-$theme-open$size")
                for (n in 0 until 3) {
                    rule.onAllNodesWithTag("plan-brief-toggle")[n].assertTextEquals("Show less").performClick()
                    settle()
                }
                briefMatrixIsFolded(style)
                assertEquals("Show less folds the request again", ownerFolded, briefTextHeight(ownerRequest), 1f)
                assertEquals("Show less folds the hand-over again", handoverFolded, briefTextHeight(reconstructedHandover), 1f)
            } else foldedColors += captureBriefMatrix(style, "plan-briefs-${style.name.lowercase()}-$theme-folded$size")
        }
        assertNotEquals("dark brief capture must differ from light", foldedColors[0], foldedColors[1])
    }

    @Config(sdk = [35], qualifiers = "en-w400dp-h2400dp-xhdpi")
    @Test fun glassBriefMatrixFoldsExpandsAndCapturesLightAndDark() = everyStyleBriefMatrix(AppStyle.Glass)

    @Config(sdk = [35], qualifiers = "en-w400dp-h2400dp-xhdpi")
    @Test fun briefStyleBriefMatrixFoldsExpandsAndCapturesLightAndDark() = everyStyleBriefMatrix(AppStyle.Brief)

    @Config(sdk = [35], qualifiers = "en-w400dp-h2400dp-xhdpi")
    @Test fun boardBriefMatrixFoldsExpandsAndCapturesLightAndDark() = everyStyleBriefMatrix(AppStyle.Board)

    @Config(sdk = [35], qualifiers = "en-w400dp-h3200dp-xhdpi", fontScale = 2f)
    @Test fun glassBriefMatrixAtLargestText() = everyStyleBriefMatrix(AppStyle.Glass, largeText = true)

    @Config(sdk = [35], qualifiers = "en-w400dp-h3200dp-xhdpi", fontScale = 2f)
    @Test fun briefStyleBriefMatrixAtLargestText() = everyStyleBriefMatrix(AppStyle.Brief, largeText = true)

    @Config(sdk = [35], qualifiers = "en-w400dp-h3200dp-xhdpi", fontScale = 2f)
    @Test fun boardBriefMatrixAtLargestText() = everyStyleBriefMatrix(AppStyle.Board, largeText = true)
}
