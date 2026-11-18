package persona.feature.settings

import android.content.Context
import android.content.res.Configuration
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.navigation.SettingsPage
import java.util.Locale

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class SettingsSearchAliasesTest {
    @Test fun completeFormerEntryNamesFindTheirNewHomesInAllThreeLanguages() {
        val context = ApplicationProvider.getApplicationContext<Context>()
        val entries = listOf(
            Triple(SettingsPage.ModelConfig, R.string.settings_home_models, listOf(R.string.settings_providers_title, R.string.settings_src_model_title)),
            Triple(SettingsPage.AgentSettings, R.string.settings_home_agents, listOf(R.string.settings_row_claude, R.string.settings_row_codex, R.string.settings_row_agy, R.string.settings_triage_title)),
            Triple(SettingsPage.NotificationBackground, R.string.settings_notification_background_title,
                listOf(R.string.settings_ready_notifications, R.string.settings_capsule_title, R.string.settings_row_background, R.string.settings_desktops_mute)),
            Triple(SettingsPage.Diagnostics, R.string.settings_home_connection, listOf(R.string.settings_diag_title, R.string.settings_hub_address, R.string.settings_row_pairing)),
            Triple(SettingsPage.About, R.string.settings_home_about, listOf(R.string.settings_about_persona, R.string.settings_update_check)),
            Triple(SettingsPage.Connectors, R.string.settings_section_connectors, listOf(R.string.settings_sent_title, R.string.settings_phone_data_title)),
        )
        for (language in listOf("en", "zh-CN", "ja")) {
            val config = Configuration(context.resources.configuration).apply { setLocale(Locale.forLanguageTag(language)) }
            val resources = context.createConfigurationContext(config).resources
            for ((page, title, formerNames) in entries) {
                val aliases = settingsFormerEntryAliases(resources, page)
                for (name in formerNames) {
                    val query = resources.getString(name)
                    assertTrue("$language: $query must find $page", settingsSearchMatches(query, resources.getString(title), aliases = aliases))
                }
            }
        }
    }
}
