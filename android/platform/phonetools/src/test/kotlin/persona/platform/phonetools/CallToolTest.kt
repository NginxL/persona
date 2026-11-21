package persona.platform.phonetools

import android.Manifest
import android.app.Application
import android.content.ActivityNotFoundException
import android.content.Context
import android.content.ContextWrapper
import android.content.Intent
import android.content.pm.ActivityInfo
import android.content.pm.PackageManager
import android.content.pm.ResolveInfo
import android.net.Uri
import android.os.Bundle
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.runBlocking
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.boolean
import kotlinx.serialization.json.booleanOrNull
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.put
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config
import persona.core.contracts.api.PhoneToolCall
import persona.core.contracts.api.PhoneToolResult

/** Android may open the user's dialer after the usual approval, but can neither place nor observe a call itself. */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class CallToolTest {
    private val context: Context get() = ApplicationProvider.getApplicationContext()
    private val application: Application get() = ApplicationProvider.getApplicationContext()

    private fun text(result: PhoneToolResult) =
        result.content.single().jsonObject.getValue("text").jsonPrimitive.content

    private fun body(result: PhoneToolResult): JsonObject = Json.parseToJsonElement(text(result)).jsonObject

    private fun args(number: String, kind: String? = null) = buildJsonObject {
        put("number", number)
        if (kind != null) put("kind", kind)
        put("name", "Sam")
    }

    private fun registry(
        context: Context = this.context,
        foreground: () -> Boolean = { true },
        confirmations: MutableList<String> = mutableListOf(),
    ): PhoneToolRegistry {
        val tools = AndroidPhoneTools(context, foreground)
        return PhoneToolRegistry(
            { PhoneSource.entries.filter { it.key == "calls" }.toSet() }, { true }, foreground, { _, _ -> true },
            { render ->
                confirmations += render(context.resources)
                PhoneConfirmation.Allowed
            },
            tools::execute,
        )
    }

    private fun installDialer() {
        val dialer = ResolveInfo().apply {
            activityInfo = ActivityInfo().apply { packageName = "com.example.dialer"; name = "Dialer" }
        }
        shadowOf(context.packageManager).apply {
            addResolveInfoForIntent(Intent(Intent.ACTION_DIAL), dialer)
            addResolveInfoForIntent(Intent(Intent.ACTION_DIAL, Uri.parse(DIAL_URI)), dialer)
        }
    }

    private fun assertNoIntent() {
        assertNull("no dialer or call intent may be launched", shadowOf(application).nextStartedActivity)
    }

    private fun assertCleanHandoffFailure(result: PhoneToolResult) {
        val message = text(result)
        assertTrue(message, result.isError)
        assertTrue(message, message.contains("dial", ignoreCase = true))
        assertFalse(message, message.contains("unknown tool", ignoreCase = true))
        assertFalse(message, message.contains("handed_to_android"))
        assertFalse(message, message.contains("uncertain", ignoreCase = true) || message.contains("不确定"))
    }

    @Test fun validDefaultAndPhoneKindsOnlyHandTheSanitizedNumberToTheDialer() = runBlocking {
        installDialer()
        val confirmations = mutableListOf<String>()
        val registry = registry(confirmations = confirmations)
        for ((id, kind) in listOf("default-kind" to null, "phone-kind" to "phone")) {
            val result = registry.call(PhoneToolCall(id, "phone_call_start", args(RAW_NUMBER, kind)))
            assertFalse(text(result), result.isError)
            val body = body(result)
            assertEquals(true, body.getValue("handed_to_android").jsonPrimitive.boolean)
            assertEquals("phone", body.getValue("kind").jsonPrimitive.content)
            assertEquals(SANITIZED_NUMBER, body.getValue("number").jsonPrimitive.content)
            val note = body.getValue("note").jsonPrimitive.content
            assertTrue(note, note.contains("cannot tell whether it connected"))
            assertNotEquals(true, body["connected"]?.jsonPrimitive?.booleanOrNull)
        }
        assertEquals(2, confirmations.size)
        assertTrue(confirmations.all { it.contains("Sam") && it.contains(SANITIZED_NUMBER) })
        repeat(2) {
            val intent = requireNotNull(shadowOf(application).nextStartedActivity)
            assertEquals(Intent.ACTION_DIAL, intent.action)
            assertNotEquals(Intent.ACTION_CALL, intent.action)
            assertEquals(Uri.parse(DIAL_URI), intent.data)
            assertTrue("an application-context handoff needs NEW_TASK", intent.flags and Intent.FLAG_ACTIVITY_NEW_TASK != 0)
        }
        assertNoIntent()
    }

    @Test fun blankAlphabeticShortAndMultipleNumbersFailBeforeConfirmationOrIntent() = runBlocking {
        installDialer()
        val confirmations = mutableListOf<String>()
        val registry = registry(confirmations = confirmations)
        val invalid = listOf(
            "   " to "number is required",
            "call me" to "not one phone number",
            "12" to "not one phone number",
            "555-0100 or 555-0199" to "not one phone number",
        )
        for ((index, input) in invalid.withIndex()) {
            val (number, expected) = input
            val result = registry.call(PhoneToolCall("invalid-$index", "phone_call_start", args(number)))
            assertTrue(text(result), result.isError)
            assertTrue(text(result), text(result).contains(expected))
        }
        assertTrue("invalid calls must not show an approval card", confirmations.isEmpty())
        assertNoIntent()
    }

    @Test fun faceTimeKindsFailAsUnsupportedWithoutAnIntent() = runBlocking {
        val registry = registry()
        for (kind in listOf("facetime", "facetime_audio")) {
            val result = registry.call(PhoneToolCall(kind, "phone_call_start", args("me@example.com", kind)))
            assertTrue(text(result), result.isError)
            assertTrue(text(result), text(result).contains("facetime", ignoreCase = true))
            assertTrue(text(result), text(result).contains("unsupported", ignoreCase = true) ||
                text(result).contains("not supported", ignoreCase = true))
            assertFalse(text(result), text(result).contains("handed_to_android"))
        }
        assertNoIntent()
    }

    @Test fun missingDialerFailsCleanlyWithoutSuccess() = runBlocking {
        val result = registry().call(PhoneToolCall("no-dialer", "phone_call_start", args(RAW_NUMBER)))
        assertCleanHandoffFailure(result)
        assertNoIntent()
    }

    @Test fun dialerLaunchFailureFailsCleanlyWithoutSuccess() = runBlocking {
        installDialer() // Resolution succeeds; the subsequent launch itself fails.
        val failingContext = object : ContextWrapper(context) {
            override fun getApplicationContext(): Context = this
            override fun startActivity(intent: Intent) {
                if (intent.action == Intent.ACTION_DIAL) throw ActivityNotFoundException("dialer launch failed")
                super.startActivity(intent)
            }
            override fun startActivity(intent: Intent, options: Bundle?) {
                if (intent.action == Intent.ACTION_DIAL) throw ActivityNotFoundException("dialer launch failed")
                super.startActivity(intent, options)
            }
        }
        val result = registry(failingContext).call(PhoneToolCall("launch-failure", "phone_call_start", args(RAW_NUMBER)))
        assertCleanHandoffFailure(result)
        assertNoIntent()
    }

    @Test fun backgroundCallIsRejectedBeforeConfirmationOrIntent() = runBlocking {
        installDialer()
        val confirmations = mutableListOf<String>()
        val registry = registry(foreground = { false }, confirmations = confirmations)
        val result = registry.call(PhoneToolCall("background", "phone_call_start", args(RAW_NUMBER)))
        assertTrue(text(result), result.isError)
        assertTrue(text(result), text(result).contains("open on screen"))
        assertTrue(confirmations.isEmpty())
        assertNoIntent()
    }

    @Test fun callsDoNotUseCallPhonePermission() {
        val source = requireNotNull(PhoneSource.entries.singleOrNull { it.key == "calls" }) {
            "Android must declare an opt-in Calls source"
        }
        assertTrue(PhonePermissions(context).permissions(source).isEmpty())
        val requested = context.packageManager.getPackageInfo(context.packageName, PackageManager.GET_PERMISSIONS)
            .requestedPermissions?.toList().orEmpty()
        assertFalse(Manifest.permission.CALL_PHONE in requested)
        assertNotNull(PhoneToolCatalog.all.singleOrNull { it.spec.name == "phone_call_start" })
    }

    private companion object {
        const val RAW_NUMBER = "+1 (555) 010-0199"
        const val SANITIZED_NUMBER = "+15550100199"
        const val DIAL_URI = "tel:+15550100199"
    }
}
