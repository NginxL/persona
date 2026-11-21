package persona.platform.phonetools

import android.content.ActivityNotFoundException
import android.content.Context
import android.content.Intent
import android.net.Uri
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import persona.core.contracts.api.PhoneToolResult

/** Opens the system dialer after Persona's confirmation; Android never places the call on the user's behalf. */
internal object CallTool {
    fun confirmation(args: JsonObject): PhoneConfirmationText {
        val request = CallRequest.parse(args)
        return { res -> res.getString(R.string.phonetools_call_start, request.who) }
    }

    fun start(context: Context, args: JsonObject): PhoneToolResult {
        val request = try {
            CallRequest.parse(args)
        } catch (failure: PhoneToolFailure) {
            return PhoneToolText.failure(failure, PhoneSource.Calls)
        }
        // Keep iOS's dialing punctuation, but encode '#' so it cannot become a URI fragment.
        val target = Uri.parse("tel:${Uri.encode(request.number, "+*,;")}")
        val intent = Intent(Intent.ACTION_DIAL, target).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        if (intent.resolveActivity(context.packageManager) == null) return dialerFailure()
        return try {
            context.startActivity(intent)
            PhoneToolResult.text(buildJsonObject {
                put("handed_to_android", true)
                put("kind", "phone")
                put("number", request.number)
                put("note", "Android shows the dialer; the call starts only if the user taps Call. " +
                    "Persona cannot tell whether it connected.")
            }.toString())
        } catch (_: ActivityNotFoundException) {
            dialerFailure()
        } catch (_: SecurityException) {
            dialerFailure()
        }
    }

    private fun dialerFailure() = PhoneToolText.unavailable(
        "安卓未能打开拨号器，未开始通话 / Android could not open a dialer. Install or enable a phone app and try again; no call was started.",  // i18n-ignore: tool result for the model
    )
}

private data class CallRequest(val number: String, val name: String?) {
    val who: String get() = name?.let { "$it ($number)" } ?: number

    companion object {
        private const val ALLOWED = " +-().*#,;"
        private const val DIALABLE = "+*#,;"

        fun parse(args: JsonObject): CallRequest {
            val raw = args.text("number") ?: invalid("number is required")
            when (args.text("kind") ?: "phone") {
                "phone" -> Unit
                "facetime", "facetime_audio" -> unavailable(
                    "安卓不支持 FaceTime / FaceTime is unsupported on Android. Use kind phone with one phone number.",  // i18n-ignore: tool result for the model
                )
                else -> invalid("kind must be phone, facetime or facetime_audio")
            }
            var digits = 0
            var offset = 0
            val normalized = StringBuilder(raw.length)
            while (offset < raw.length) {
                val point = raw.codePointAt(offset)
                when {
                    isNumber(point) -> {
                        digits++
                        normalized.appendCodePoint(point)
                    }
                    point < 128 && point.toChar() in ALLOWED -> {
                        if (point.toChar() in DIALABLE) normalized.append(point.toChar())
                    }
                    else -> invalid("\"$raw\" is not one phone number")
                }
                offset += Character.charCount(point)
            }
            if (digits < 3) invalid("\"$raw\" is not one phone number")
            return CallRequest(normalized.toString(), args.text("name"))
        }

        /** Swift Character.isNumber includes decimal digits, letter numbers and other Unicode numbers. */
        private fun isNumber(point: Int) = when (Character.getType(point)) {
            Character.DECIMAL_DIGIT_NUMBER.toInt(), Character.LETTER_NUMBER.toInt(), Character.OTHER_NUMBER.toInt() -> true
            else -> false
        }
    }
}
