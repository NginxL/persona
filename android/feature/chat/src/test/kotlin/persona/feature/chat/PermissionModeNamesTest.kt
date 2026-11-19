package persona.feature.chat

import java.io.File
import javax.xml.parsers.DocumentBuilderFactory
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import org.w3c.dom.Element
import persona.core.contracts.JsonCodec

/** The Android header, composer, approvals and confirmations use the shared mode names (#10363). */
class PermissionModeNamesTest {
    private val modes = listOf("default", "acceptEdits", "plan", "auto", "bypassPermissions")
    private val languages = mapOf("en" to "values", "zh-Hans" to "values-zh", "ja" to "values-ja")
    // Standalone aliases only. Launcher-default, raw-ID handoff and unset routine behavior are
    // deliberately excluded; mode-switch confirmations are checked separately below.
    private val exact = mapOf(
        "default" to listOf(
            "chat_modes_default", "chat_answered_mode_default", "chat_tasks_permission_default", "agents_mode_default",
            "assistant_proactive_mode_default", "assistant_settings_permission_default",
        ),
        "acceptEdits" to listOf(
            "chat_modes_accept_edits", "chat_answered_mode_accept_edits", "chat_tasks_permission_accept_edits",
            "agents_mode_accept_edits", "assistant_proactive_mode_accept_edits", "assistant_settings_permission_edits",
        ),
        "plan" to listOf(
            "chat_modes_plan", "chat_tasks_permission_plan", "agents_mode_plan",
            "assistant_proactive_mode_plan", "assistant_settings_permission_plan",
        ),
        "auto" to listOf(
            "chat_modes_auto", "chat_tasks_permission_auto", "agents_mode_auto",
            "assistant_proactive_mode_auto", "assistant_settings_permission_auto",
        ),
        "bypassPermissions" to listOf(
            "chat_modes_bypass", "chat_answered_mode_bypass", "chat_tasks_permission_bypass", "agents_mode_bypass",
            "assistant_proactive_mode_bypass", "assistant_settings_permission_full",
        ),
    )
    private val contains = mapOf(
        "auto" to listOf("assistant_proactive_runs_with_note"),
        "bypassPermissions" to listOf(
            "chat_modes_confirm_bypass_title",
            "chat_modes_confirm_leave_title",
            "chat_modes_confirm_leave_text",
            "chat_modes_button_bypass",
            "agents_new_bypass_warning",
            "agents_new_provider_mode_hint",
            "assistant_proactive_bypass_note",
            "assistant_proactive_bypass_title",
        ),
    )

    @Test fun stringsMatchSharedTable() {
        val root = File(checkNotNull(System.getProperty("persona.repo.root")))
        val fixture = JsonCodec.parseToJsonElement(
            File(root, "spec/fixtures/permission-modes/names.json").readText(),
        ).jsonObject
        val names = fixture.getValue("names").jsonObject
        assertEquals(modes.toSet(), names.keys)
        assertRoutineDefaultAndUnsetAreDistinct(root)

        for ((language, directory) in languages) {
            val strings = readStrings(root, directory)
            for (mode in modes) {
                val expected = names.getValue(mode).jsonObject.getValue(language).jsonPrimitive.content
                for (key in exact.getValue(mode)) {
                    assertEquals("$language $key", expected, strings.getValue(key))
                }
                for (key in contains[mode].orEmpty()) {
                    assertTrue("$language $key must contain $expected", strings.getValue(key).contains(expected))
                }
            }
        }
    }

    private fun assertRoutineDefaultAndUnsetAreDistinct(root: File) {
        val source = File(root, "android/feature/assistant/src/main/kotlin/persona/feature/assistant/proactive/ProactiveText.kt").readText()
        val title = source.substringAfter("internal fun permissionModeTitle").substringBefore("internal fun permissionModeDetail")
        val actual = requireNotNull(Regex("(?:\"default\"|else)\\s*->\\s*R\\.string\\.([A-Za-z0-9_]+)").find(title)) {
            "permissionModeTitle must name the actual default mode"
        }
        val unset = requireNotNull(Regex("\"\"\\s*->\\s*R\\.string\\.([A-Za-z0-9_]+)").find(title)) {
            "permissionModeTitle must give an empty/unset routine its own label"
        }
        assertEquals("assistant_proactive_mode_default", actual.groupValues[1])
        assertTrue("an unset routine must not reuse the actual default mode's key", unset.groupValues[1] != actual.groupValues[1])
    }

    private fun readStrings(root: File, directory: String): Map<String, String> {
        val result = mutableMapOf<String, String>()
        val resources = mapOf(
            "feature/chat" to listOf("strings_a.xml", "strings_b.xml"),
            "feature/agents" to listOf("strings.xml"),
            "feature/assistant" to listOf("strings.xml", "strings_proactive_tasks.xml"),
        )
        for ((module, files) in resources) {
            for (file in files) {
                val path = File(root, "android/$module/src/main/res/$directory/$file")
                val nodes = DocumentBuilderFactory.newInstance().newDocumentBuilder().parse(path).getElementsByTagName("string")
                for (index in 0 until nodes.length) {
                    val element = nodes.item(index) as Element
                    result[element.getAttribute("name")] = element.textContent
                }
            }
        }
        return result
    }
}
