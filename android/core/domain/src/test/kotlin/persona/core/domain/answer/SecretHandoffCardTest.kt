package persona.core.domain.answer

import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.Interaction
import persona.core.contracts.JsonCodec
import persona.core.contracts.Resolution
import persona.core.contracts.SecretHandoffRequest
import persona.core.contracts.SecretHandoffVariable

/** The reserved one-run hand-off: only its complete card can approve, and only the allowlisted context is read. */
class SecretHandoffCardTest {
    private fun card(
        input: String = INPUT,
        kind: String = "permission",
        tool: String = "secret_handoff",
        always: String? = "this launcher on this computer",
    ) = JsonCodec.decodeFromString<Interaction>(
        """{"id":"ix","machine_id":"hub","session_id":"chat","kind":"$kind","tool_name":"$tool","tool_input":$input,"always_label":${always?.let { JsonPrimitive(it).toString() ?: "null" } ?: "null"}}""",
    )

    @Test fun readsOnlyTheAllowlistedContext() {
        val parsed = card().secretHandoff
        assertEquals(
            SecretHandoffRequest(
                listOf(
                    SecretHandoffVariable("TOKEN", "Example Vault", "Production token"),
                    SecretHandoffVariable("BACKUP", "example-vault", "Backup token"),
                ),
                "Example Agent", "Workstation", "/srv/example", "Run the deployment checks",
            ),
            parsed,
        )
        assertNull(card(kind = "question").secretHandoff)
        assertNull(card(tool = "Bash").secretHandoff)
        assertFalse(card(kind = "question").isSecretHandoff)
        assertTrue(card().needsFieldValues)
        assertFalse(card().hasFieldsForm)
        assertTrue(card().offersAlways)
    }

    @Test fun malformedAndBlankVariablesFailClosed() {
        val sparse = card("""{"variables":[{"name":"  ","provider":"ignored"},null,1,{"name":3},{"name":" OK ","provider_name":4,"title":null}],"agent":7,"cwd":false}""")
        assertNull("one malformed entry invalidates every approval, including the readable variables", sparse.secretHandoff)
        assertFalse(AnswerBuilder.build(sparse, AnswerAction.Allow).allowed)
        assertFalse(AnswerBuilder.build(sparse, AnswerAction.Always).allowed)
        for (input in listOf("{}", """{"variables":[]}""", """{"variables":[null,{"name":" "}]}""", "null")) {
            val request = card(input)
            assertFalse(input, AnswerBuilder.build(request, AnswerAction.Allow).allowed)
            assertFalse(input, AnswerBuilder.build(request, AnswerAction.Always).allowed)
            assertFalse(input, request.offersAlways)
            assertTrue(input, request.needsFieldValues)
        }
    }

    @Test fun exactAllowAlwaysAndDenyBodies() {
        val request = card()
        assertEquals(Resolution(allow = true), AnswerBuilder.build(request, AnswerAction.Allow).resolution)
        assertEquals(Resolution(allow = true, always = true), AnswerBuilder.build(request, AnswerAction.Always).resolution)
        assertEquals(Resolution(allow = false), AnswerBuilder.build(request, AnswerAction.Deny).resolution)
        assertEquals("""{"allow":true}""", JsonCodec.encodeToString(Resolution.serializer(), Resolution(allow = true)))
        assertEquals("""{"allow":true,"always":true}""", JsonCodec.encodeToString(Resolution.serializer(), Resolution(allow = true, always = true)))
        assertEquals("""{"allow":false}""", JsonCodec.encodeToString(Resolution.serializer(), Resolution(allow = false)))
        assertFalse(AnswerBuilder.build(card(always = null), AnswerAction.Always).allowed)
        assertFalse(AnswerBuilder.build(card(always = "  "), AnswerAction.Always).allowed)
        assertEquals(Resolution(allow = true), AnswerBuilder.build(card(always = null), AnswerAction.Allow).resolution)
    }

    @Test fun approvalsTakeNoDraftAndOnlyTheConversation() {
        val request = card()
        val drafts = listOf(
            AnswerDraft(message = "why"), AnswerDraft(feedback = "why"), AnswerDraft(text = "text"),
            AnswerDraft(password = "not-a-real-secret"), AnswerDraft(values = mapOf("x" to "y")),
            AnswerDraft(other = mapOf("x" to "y")), AnswerDraft(fields = buildJsonObject { put("x", "y") }),
        )
        for (action in listOf(AnswerAction.Allow, AnswerAction.Always)) {
            for (draft in drafts) assertFalse("$action $draft", AnswerBuilder.build(request, action, draft).allowed)
            assertFalse(AnswerBuilder.build(request, action, context = AnswerContext(locked = true)).allowed)
            assertFalse(AnswerBuilder.build(request, action, context = AnswerContext(sending = true)).allowed)
            assertFalse(AnswerBuilder.build(request, action, context = AnswerContext(state = "resolved")).allowed)
        }
        for (entry in AnswerEntry.entries - AnswerEntry.Conversation) {
            for (action in AnswerAction.entries - AnswerAction.Deny) {
                assertFalse("${entry.wire} ${action.wire}",
                    AnswerBuilder.build(request, action, context = AnswerContext(entry = entry)).allowed)
            }
        }
        for (action in AnswerAction.entries - AnswerAction.Allow - AnswerAction.Always - AnswerAction.Deny) {
            assertFalse(action.wire, AnswerBuilder.build(request, action).allowed)
        }
        assertEquals(Resolution(allow = true),
            AnswerBuilder.build(card(tool = "Bash", input = """{"command":"ls"}""", always = null), AnswerAction.Allow).resolution)
    }

    @Test fun denyAllowsOnlyItsOptionalReason() {
        val request = card()
        assertEquals(Resolution(allow = false, message = "not now"),
            AnswerBuilder.build(request, AnswerAction.Deny, AnswerDraft(message = "not now")).resolution)
        for (draft in listOf(AnswerDraft(message = "x", password = "y"), AnswerDraft(message = "x", values = mapOf("a" to "b")),
            AnswerDraft(message = "x".repeat(4001)))) {
            assertFalse(AnswerBuilder.build(request, AnswerAction.Deny, draft).allowed)
        }
    }

    private companion object {
        const val INPUT = """{"variables":[{"name":" TOKEN ","provider":"example-vault","provider_name":" Example Vault ","title":" Production token ","reference":"op://Test/Vault/token","value":"sentinel-never-show"},{"name":"BACKUP","provider":"example-vault","provider_name":"","title":"Backup token"}],"agent":" Example Agent ","machine_name":" Workstation ","cwd":" /srv/example ","prompt":" Run the deployment checks ","reference":"op://Test/Vault/other","value":"sentinel-never-show"}"""
    }
}
