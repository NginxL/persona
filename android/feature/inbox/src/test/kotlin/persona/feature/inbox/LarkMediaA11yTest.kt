package persona.feature.inbox

import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.semantics.getOrNull
import androidx.compose.ui.test.SemanticsMatcher
import androidx.compose.ui.test.assert
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.designsystem.PersonaTheme
import persona.core.inbox.*

/**
 * TalkBack names Lark voice and video messages, reads their duration and says a tap plays them (#3904). The name sits
 * on the play / pause icon: on the merging container it would replace the merged duration text (#4085).
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class LarkMediaA11yTest {
    @get:Rule val rule = createComposeRule()

    private fun clickLabel(label: String) = SemanticsMatcher("click label is $label") {
        it.config.getOrNull(SemanticsActions.OnClick)?.label == label
    }

    private fun assertContainerHasNoOwnDescription() {
        rule.onNode(hasClickAction(), useUnmergedTree = true)
            .assert(SemanticsMatcher.keyNotDefined(SemanticsProperties.ContentDescription))
    }

    @Test fun aVideoIsNamedReadsItsDurationAndSaysATapPlaysIt() {
        rule.setContent { PersonaTheme(reduceMotion = true) { LarkVideoView(LarkPart.Video("video", 12_000), onPlay = {}) } }
        rule.onNodeWithContentDescription("Video").assert(clickLabel("Play")).assert(hasText("00:12"))
        assertContainerHasNoOwnDescription()
    }

    @Test fun aVoiceMessageIsNamedReadsItsDurationAndSaysATapPlaysIt() {
        rule.setContent { PersonaTheme(reduceMotion = true) { LarkVoiceView(LarkPart.Audio("audio", 5_000)) } }
        rule.onNodeWithContentDescription("Voice").assert(clickLabel("Play")).assert(hasText("5\""))
        assertContainerHasNoOwnDescription()
    }
}
