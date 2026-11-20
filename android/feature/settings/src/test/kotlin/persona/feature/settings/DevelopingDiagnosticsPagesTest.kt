package persona.feature.settings

import android.content.Context
import androidx.activity.ComponentActivity
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.assertIsNotEnabled
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.io.File
import java.time.Instant
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.data.dev.DevCategory
import persona.core.designsystem.PersonaTheme

@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w420dp-h900dp-xhdpi")
class DevelopingDiagnosticsPagesTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    private fun back() = rule.runOnIdle { rule.activity.onBackPressedDispatcher.onBackPressed() }

    @Test fun theSwitchStartsRecordingAndTheEnabledGroupOpensExport() {
        val fake = FakeDevelopingDiagnostics()
        var exports = 0
        rule.setContent { PersonaTheme { DevelopingPersonaAbout(fake) { exports++ } } }
        rule.onNodeWithText("Export Diagnostics…").assertDoesNotExist()
        rule.onNodeWithText("Developing Persona").performClick()
        rule.waitForIdle()
        assertEquals(listOf(true), fake.enabledChanges)

        fake.state.value = DevelopingDiagnosticsSnapshot(true, Instant.parse("2026-11-17T04:00:00Z"), 2048)
        rule.onNodeWithText("Recorded").assertIsDisplayed()
        rule.onNodeWithText("Export Diagnostics…").performClick()
        assertEquals(1, exports)
    }

    @Test fun turningOffRefreshesAnUnknownSizeBeforeChoosingDeletion() {
        val fake = FakeDevelopingDiagnostics(DevelopingDiagnosticsSnapshot(true, Instant.parse("2026-11-17T04:00:00Z"), null))
        rule.setContent { PersonaTheme { DevelopingPersonaAbout(fake) {} } }
        rule.waitForIdle()
        fake.persistedBytes = 4096
        rule.onNodeWithText("Developing Persona").performClick()
        rule.onNodeWithText("Turn off Developing Persona?").assertIsDisplayed()
        rule.onNodeWithText("Export First…").assertIsDisplayed()
        assertTrue(fake.enabledChanges.isEmpty())
    }

    @Test fun turningOffWithRecordsOffersExportBeforeDeletion() {
        val fake = FakeDevelopingDiagnostics(DevelopingDiagnosticsSnapshot(true, Instant.parse("2026-11-17T04:00:00Z"), 2048))
        var exports = 0
        rule.setContent { PersonaTheme { DevelopingPersonaAbout(fake) { exports++ } } }
        rule.onNodeWithText("Developing Persona").performClick()
        rule.onNodeWithText("Turn off Developing Persona?").assertIsDisplayed()
        assertTrue(fake.enabledChanges.isEmpty())
        rule.onNodeWithText("Export First…").performClick()
        assertEquals(1, exports)
        assertTrue(fake.enabledChanges.isEmpty())

        rule.onNodeWithText("Developing Persona").performClick()
        rule.onNodeWithText("Turn Off and Delete").performClick()
        rule.waitForIdle()
        assertEquals(listOf(false), fake.enabledChanges)
    }

    @Test fun exportHonorsCategoriesAndOnlySharesTheCreatedArchive() {
        val fake = FakeDevelopingDiagnostics(DevelopingDiagnosticsSnapshot(true, recordedBytes = 2048))
        rule.setContent { PersonaTheme { DevelopingDiagnosticsExportPage(fake) {} } }
        rule.onNodeWithText("Errors and Crashes").performClick()
        rule.onNodeWithText("Create Archive").performClick()
        rule.waitForIdle()
        assertEquals(listOf(setOf(DevCategory.Logs, DevCategory.Energy)), fake.exportedCategories)
        rule.onNodeWithText("Share File…").performClick()
        rule.onNodeWithText("Send to Agent…").performClick()
        assertEquals(listOf(fake.archive), fake.shared)
        assertEquals(listOf(fake.archive), fake.sent)
    }

    @Test fun exportBackReturnsToAboutBeforeLeavingSettings() {
        val fake = FakeDevelopingDiagnostics(DevelopingDiagnosticsSnapshot(true, recordedBytes = 2048))
        val services = demoSettingsServices(rule.activity.resources).copy(developing = fake)
        rule.setContent { PersonaTheme { SettingsScreen(services, initialPage = "about") } }
        rule.onNodeWithText("Export Diagnostics…").performClick()
        rule.onNodeWithText("Create Archive").assertIsDisplayed()

        back()
        rule.onNodeWithText("Export Diagnostics…").assertIsDisplayed()
        back()
        rule.onNodeWithText("Export Diagnostics…").assertDoesNotExist()
        rule.onNodeWithText("Settings").assertIsDisplayed()
    }

    @Test fun routedExportBackClosesOnlyTheExportOverlay() {
        val fake = FakeDevelopingDiagnostics(DevelopingDiagnosticsSnapshot(true, recordedBytes = 2048))
        val services = demoSettingsServices(rule.activity.resources).copy(developing = fake)
        var exits = 0
        rule.setContent {
            PersonaTheme {
                SettingsScreen(services, initialPage = "about", onBack = { exits++ })
            }
        }
        rule.onNodeWithText("Export Diagnostics…").performClick()
        rule.onNodeWithText("Create Archive").assertIsDisplayed()

        back()
        rule.onNodeWithText("Export Diagnostics…").assertIsDisplayed()
        assertEquals(0, exits)
        back()
        assertEquals(1, exits)
    }

    @Test fun aFailedExportCanBeRetriedAndBusyCannotStartAnotherExport() {
        val gate = CompletableDeferred<File>()
        val fake = FakeDevelopingDiagnostics(DevelopingDiagnosticsSnapshot(true, recordedBytes = 2048)).apply { exportGate = gate }
        rule.setContent { PersonaTheme { DevelopingDiagnosticsExportPage(fake) {} } }
        rule.onNodeWithText("Create Archive").performClick()
        rule.onNodeWithText("Creating archive…").assertIsNotEnabled()
        assertEquals(1, fake.exportedCategories.size)
        gate.completeExceptionally(IllegalStateException("full"))
        rule.waitForIdle()
        rule.onNodeWithText("Couldn’t create the diagnostics archive. Nothing was shared; try again.").assertIsDisplayed()
        rule.onNodeWithText("Share File…").assertDoesNotExist()

        fake.exportGate = null
        rule.onNodeWithText("Retry").performClick()
        rule.waitForIdle()
        assertEquals(2, fake.exportedCategories.size)
        rule.onNodeWithText("Share File…").assertIsDisplayed()
    }

    private class FakeDevelopingDiagnostics(initial: DevelopingDiagnosticsSnapshot = DevelopingDiagnosticsSnapshot()) : DevelopingDiagnostics {
        override val state = MutableStateFlow(initial)
        val enabledChanges = mutableListOf<Boolean>()
        val exportedCategories = mutableListOf<Set<DevCategory>>()
        val archive = File.createTempFile("developing-diagnostics", ".zip")
        val shared = mutableListOf<File>()
        val sent = mutableListOf<File>()
        var exportGate: CompletableDeferred<File>? = null
        var persistedBytes: Long? = null

        override suspend fun setEnabled(enabled: Boolean) {
            enabledChanges += enabled
            state.value = if (enabled) DevelopingDiagnosticsSnapshot(true, Instant.now(), 0) else DevelopingDiagnosticsSnapshot()
        }
        override suspend fun refresh() {
            persistedBytes?.let { state.value = state.value.copy(recordedBytes = it) }
        }
        override suspend fun export(categories: Set<DevCategory>): File {
            exportedCategories += categories
            exportGate?.await()
            return archive
        }
        override fun share(context: Context, file: File) { shared += file }
        override fun sendToAgent(context: Context, file: File) { sent += file }
    }
}
