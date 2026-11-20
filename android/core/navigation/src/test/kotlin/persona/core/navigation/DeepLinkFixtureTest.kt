package persona.core.navigation

import androidx.navigation3.runtime.NavKey
import java.io.File
import kotlinx.serialization.json.JsonNull
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.JsonCodec

/** Every shared parse case (spec/fixtures/deep-links), including both paired-hub profiles. */
class DeepLinkFixtureTest {
    @Test fun sharedParseCasesMatchTheAndroidResult() {
        val file = File(System.getProperty("persona.repo.root"), "spec/fixtures/deep-links/cases.json")
        val document = JsonCodec.parseToJsonElement(file.readText()).jsonObject
        assertTrue(document.getValue("version").jsonPrimitive.content == "1")
        val hubs = document.getValue("hubs").jsonObject
        val cases = document.getValue("parse").jsonArray
        assertTrue("The shared fixture has parse cases", cases.isNotEmpty())

        val failures = mutableListOf<String>()
        for (element in cases) {
            val case = element.jsonObject
            val name = case.getValue("name").jsonPrimitive.content
            val url = case.getValue("url").jsonPrimitive.content
            val profiles = case.getValue("hubs").jsonArray.map {
                val hub = hubs.getValue(it.jsonPrimitive.content).jsonObject
                HubLinks.Hub(
                    id = hub.getValue("id").jsonPrimitive.content,
                    addresses = hub.getValue("addresses").jsonArray.map { address -> address.jsonPrimitive.content },
                )
            }
            val expectedPage = case.getValue("page").let { page ->
                if (page == JsonNull) null else page.jsonObject.mapValues { (_, value) -> value.jsonPrimitive.content }
            }
            val expectedReason = case["reason"]?.jsonPrimitive?.content
            when (val result = HubLinks.parse(url, profiles)) {
                is HubLinks.Result.Page -> {
                    val actual = fields(result.page) + listOfNotNull(result.hub?.let { "hub" to it })
                    if (expectedPage != actual || expectedReason != null) {
                        failures += "$name: expected page $expectedPage, got $actual (reason $expectedReason)"
                    }
                }
                is HubLinks.Result.Rejected -> {
                    val actual = when (result.reason) {
                        HubLinks.Reason.External -> "external"
                        HubLinks.Reason.NotAPage -> "not-a-page"
                        HubLinks.Reason.Unsafe -> "unsafe"
                    }
                    if (expectedPage != null || expectedReason != actual) {
                        failures += "$name: expected ${expectedPage ?: expectedReason}, got rejection $actual"
                    }
                }
            }
        }
        assertTrue("${failures.size} of ${cases.size} shared parse cases failed:\n${failures.joinToString("\n")}", failures.isEmpty())
    }

    private fun fields(page: HubPage): Map<String, String> = when (page) {
        is HubPage.Agent -> values("session", "machine" to page.machineId, "session" to page.sessionId,
            "at" to page.itemId, "view" to page.view)
        is HubPage.Conversation -> if (page.sessionId == null) values("persona")
            else values("conversation", "id" to page.sessionId, "machine" to page.machineId,
                "manager" to page.managerId, "at" to page.itemId)
        HubPage.NewConversation -> values("persona-new")
        is HubPage.Knowledge -> values("knowledge", "kind" to page.kind)
        is HubPage.KnowledgeEntry -> values("knowledge-entry", "kind" to page.kind, "id" to page.id)
        is HubPage.Screen -> screenFields(page.key)
    }

    private fun screenFields(key: NavKey): Map<String, String> = when (key) {
        AssistantRoute -> values("persona")
        AgentsRoute -> values("agents")
        InboxRoute -> values("inbox")
        is InboxItemRoute -> values("inbox-item", "id" to key.itemId, "manager" to key.manager)
        TodosRoute -> values("todos")
        is TodoDetailRoute -> values("todo", "id" to key.todoId)
        MachinesRoute -> values("machines")
        is MachineDetailRoute -> values("machine", "id" to key.machineId, "range" to key.range)
        SettingsRoute -> values("settings")
        is ModelPurposeRoute -> values("settings", "tab" to "models", "item" to key.purposeId, "manager" to key.manager)
        is ExtensionRoute -> values("settings", "tab" to "extensions", "item" to key.extensionId, "manager" to key.manager)
        is ExtensionUiRoute -> values("extension", "extension" to key.extensionId, "ui" to key.uiId, "state" to key.state)
        is SettingsPageRoute -> when (key.page) {
            SettingsPage.Sent -> values("settings", "tab" to "connectors", "item" to "-sent")
            SettingsPage.Connectors -> values("settings", "tab" to "connectors")
            SettingsPage.Hubs -> values("settings", "tab" to "hub")
            SettingsPage.ModelConfig -> values("settings", "tab" to "models")
            else -> error("No shared deep-link fields for $key")
        }
        else -> error("No shared deep-link fields for $key")
    }

    private fun values(page: String, vararg entries: Pair<String, String?>): Map<String, String> =
        entries.mapNotNull { (key, value) -> value?.let { key to it } }.toMap() + ("page" to page)
}
