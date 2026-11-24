package persona.feature.inbox

import android.app.Application
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.designsystem.AssistantStackProbe
import persona.core.designsystem.AssistantStackProbeBackend
import persona.core.inbox.*

@RunWith(AndroidJUnit4::class)
@Config(application = Application::class, sdk = [35])
class ReplyComposerProbeTest {
    @get:Rule val rule = createComposeRule()

    private class Backend : AssistantStackProbeBackend {
        var draft: String? = null
        override fun publishNavigation(route: String, selectedKey: String?) = Unit
        override fun publishDraft(page: String, draft: String, focused: Boolean) { this.draft = draft }
        override fun removeDraft(page: String) { draft = null }
        override fun touch() = Unit
    }

    @Test
    fun replySheetPublishesItsCurrentDraft() {
        val backend = Backend()
        AssistantStackProbe.install(backend)
        try {
            rule.setContent {
                ReplyComposerField(
                    ReplyComposerState(draft = ReplyDraft("reply"), loading = false),
                    onUpdate = {}, onSend = {}, placeholder = "", probePage = "Mail",
                )
            }
            rule.runOnIdle { assertEquals("reply", backend.draft) }
        } finally {
            AssistantStackProbe.uninstall(backend)
        }
    }
}
