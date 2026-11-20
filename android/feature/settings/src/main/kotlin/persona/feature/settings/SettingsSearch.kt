package persona.feature.settings

import android.content.res.Resources
import java.text.Normalizer
import persona.core.navigation.SettingsPage

/** Keep full former labels searchable in every locale without maintaining translated copies of them. */
internal fun settingsFormerEntryAliases(resources: Resources, page: SettingsPage): String {
    val names = when (page) {
        SettingsPage.ModelConfig -> listOf(R.string.settings_providers_title, R.string.settings_row_models, R.string.settings_src_model_title)
        SettingsPage.AgentSettings -> listOf(R.string.settings_row_claude, R.string.settings_row_codex, R.string.settings_row_agy, R.string.settings_triage_title)
        SettingsPage.NotificationBackground -> listOf(R.string.settings_ready_notifications, R.string.settings_capsule_title,
            R.string.settings_row_background, R.string.settings_desktops_banner, R.string.settings_desktops_mute)
        SettingsPage.About -> listOf(R.string.settings_section_about, R.string.settings_about_persona, R.string.settings_update_check,
            R.string.settings_developing_title, R.string.settings_developing_export)
        SettingsPage.Diagnostics -> listOf(R.string.settings_diag_title, R.string.settings_hub_address, R.string.settings_row_pairing)
        SettingsPage.Connectors -> listOf(R.string.settings_row_mail, R.string.settings_phone_data_title, R.string.settings_sent_title, R.string.settings_row_lark)
        else -> emptyList()
    }
    return names.joinToString(" ") { resources.getString(it) }
}

/** Entry names and aliases only: never searches account values, addresses or credentials. */
internal fun settingsSearchMatches(query: String, title: String, subtitle: String? = null, aliases: String = ""): Boolean {
    fun normalize(value: String) = Normalizer.normalize(value, Normalizer.Form.NFKC).lowercase(java.util.Locale.ROOT)
    val terms = normalize(query).trim().split(Regex("\\s+")).filter(String::isNotBlank)
    val text = normalize(listOfNotNull(title, subtitle, aliases).joinToString(" "))
    return terms.all(text::contains)
}
