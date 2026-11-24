package persona.feature.chat

import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.test.SemanticsMatcher
import androidx.compose.ui.test.assert
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.getUnclippedBoundsInRoot
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.test.swipeUp
import androidx.compose.ui.unit.dp
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.Interaction
import persona.core.contracts.Question
import persona.core.contracts.QuestionOption
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h800dp-xhdpi", application = QuestionTestApplication::class)
class CardHeightTest {
    @get:Rule val rule = createComposeRule()

    @Test fun storeCapsDragsTogglesStepsAndPersists() {
        var persisted: Float? = null
        val store = CardHeightStore(readShare = { persisted }, writeShare = { persisted = it })
        val range = 150.dp..550.dp

        assertNull(store.share)
        assertEquals(300.dp, store.cap(default = 300.dp, range = range, id = "q1"))

        store.drag("q1", 450.dp)
        assertEquals(450.dp, store.cap(default = 300.dp, range = range, id = "q1"))
        // Other card does not follow q1's live drag
        assertEquals(300.dp, store.cap(default = 300.dp, range = range, id = "q2"))

        // Clamped past upper bound
        store.drag("q1", 900.dp)
        assertEquals(550.dp, store.cap(default = 300.dp, range = range, id = "q1"))

        store.drag("q1", 350.dp)
        store.end("q1", range)
        assertNull(store.liveId)
        assertEquals(0.5f, store.share!!, 0.001f)
        assertEquals(0.5f, persisted!!, 0.001f)
        assertEquals(350.dp, store.cap(default = 300.dp, range = range, id = "q1"))

        // Toggle switches to tallest (1.0f), then back to default (null)
        store.toggle()
        assertEquals(1f, store.share!!, 0.001f)
        assertEquals(550.dp, store.cap(default = 300.dp, range = range, id = "q1"))
        store.toggle()
        assertNull(store.share)
        assertNull(persisted)
        assertEquals(300.dp, store.cap(default = 300.dp, range = range, id = "q1"))

        // Accessibility quarter-steps from current height
        store.step(0.25f, from = 250.dp, range = range)
        assertEquals(0.5f, store.share!!, 0.001f)
        store.step(-0.25f, from = 350.dp, range = range)
        assertEquals(0.25f, store.share!!, 0.001f)
    }

    @Test fun persistentStoreReadsBackSavedShare() {
        val context = ApplicationProvider.getApplicationContext<android.content.Context>()
        val first = CardHeightStore.persistent(context)
        first.toggle()
        assertEquals(1f, first.share!!, 0.001f)
        val reloaded = CardHeightStore.persistent(context)
        assertEquals(1f, reloaded.share!!, 0.001f)
        reloaded.toggle()
        assertNull(CardHeightStore.persistent(context).share)
    }

    @Test fun pendingStackResizeRangeLeavesConversationSliverAndSiblingRoom() {
        assertNull(PendingStack.resizeRange(760.dp, rest = 0.dp, header = 100.dp, floor = 24.dp))
        // 760 - 100 - 120 - 24 = 516dp tallest, 150dp shortest
        val lone = PendingStack.resizeRange(760.dp, rest = 120.dp, header = 100.dp, floor = 24.dp)
        assertEquals(Tokens.Size.box150..516.dp, lone)
        // Sibling folded at 64dp leaves 452dp to the task question
        val withFoldedSibling = PendingStack.resizeRange(760.dp, rest = 120.dp, header = 100.dp, floor = 24.dp, other = 64.dp)
        assertEquals(Tokens.Size.box150..452.dp, withFoldedSibling)
        // Very short screen clamps bottom to top
        val shortScreen = PendingStack.resizeRange(320.dp, rest = 120.dp, header = 80.dp, floor = 24.dp)
        assertEquals(96.dp..96.dp, shortScreen)
    }

    @Test fun grabberShowsOnlyOnLoneUnfoldedCardAndResizesOnTapAndDrag() {
        val longQuestion = Interaction(
            id = "q-long",
            kind = "question",
            questions = listOf(
                Question(
                    question = "Which architecture approach should we take for the shared storage layer?",
                    header = "Architecture",
                    options = (1..8).map { i ->
                        QuestionOption(
                            label = "Option $i: Keep layer $i isolated",
                            description = "Detailed rationale for option $i covering migration safety and rollback behavior.",
                        )
                    },
                ),
            ),
        )
        val store = CardHeightStore()
        val range = 150.dp..620.dp
        val folds = RequestFolds()
        var currentHeight by mutableStateOf(300.dp)
        var keyboard by mutableStateOf(false)
        rule.setContent {
            val resize = if (keyboard) null else CardResize("q-long", height = { currentHeight }, range = range, store = store)
            val cap = if (resize != null && !folds.isFolded("q-long")) store.cap(300.dp, range, "q-long") else 300.dp
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                Box(Modifier.fillMaxSize()) {
                    InteractionPanel(
                        requests = listOf(longQuestion),
                        machine = null,
                        maxHeight = cap,
                        busyId = null,
                        uncertainIds = emptySet(),
                        onResolve = { _, _ -> },
                        onTerminal = null,
                        onOverlay = {},
                        folds = folds,
                        keyboard = keyboard,
                        resize = resize,
                    )
                }
            }
        }
        rule.waitForIdle()
        val grabber = rule.onNodeWithTag("interaction-grabber")
        grabber.assertIsDisplayed()
            .assert(SemanticsMatcher.expectValue(SemanticsProperties.Role, Role.Button))
            .assert(SemanticsMatcher.keyIsDefined(SemanticsActions.CustomActions))

        val initialBounds = rule.onNodeWithTag("pending-card-q-long").getUnclippedBoundsInRoot()
        val initialHeight = initialBounds.bottom - initialBounds.top

        // Tap toggles to tallest room
        grabber.performClick()
        rule.waitForIdle()
        assertEquals(1f, store.share!!, 0.001f)
        val tallestBounds = rule.onNodeWithTag("pending-card-q-long").getUnclippedBoundsInRoot()
        val tallestHeight = tallestBounds.bottom - tallestBounds.top
        assertTrue("Tapping grabber expands long question card ($tallestHeight > $initialHeight)", tallestHeight > initialHeight)

        // Tap again restores default cap
        grabber.performClick()
        rule.waitForIdle()
        assertNull(store.share)

        // Vertical drag up makes card taller and persists share
        currentHeight = initialHeight
        grabber.performTouchInput {
            down(center)
            moveBy(Offset(0f, -180f))
            up()
        }
        rule.waitForIdle()
        assertNotNull("Dragging persists user share", store.share)
        assertTrue("Dragging upward increases share", store.share!! > 0.3f)

        // Folding hides the grabber
        folds.set("q-long", true)
        rule.waitForIdle()
        rule.onNodeWithTag("interaction-grabber").assertDoesNotExist()
        folds.set("q-long", false)
        rule.waitForIdle()
        rule.onNodeWithTag("interaction-grabber").assertIsDisplayed()

        // Showing keyboard hides the grabber
        keyboard = true
        rule.waitForIdle()
        rule.onNodeWithTag("interaction-grabber").assertDoesNotExist()
    }
}
