package persona.platform.phonetools

import android.content.*
import android.os.*
import android.net.ConnectivityManager
import android.net.NetworkCapabilities
import persona.core.contracts.api.PhoneToolResult
import kotlinx.coroutines.*
import kotlinx.serialization.json.*
import java.time.ZoneId
import java.util.Locale

class AndroidPhoneTools(context: Context, private val foreground: () -> Boolean) {
    private val context = context.applicationContext

    /** Reads and freezes the target before a before → after card is shown; all provider IO stays on IO. */
    suspend fun prepareConfirmation(name: String, args: JsonObject): PhonePreparedConfirmation = withContext(Dispatchers.IO) {
        when (name) {
            "phone_contacts_update" -> {
                val prepared = ContactsTool(context).prepareUpdate(args)
                PhonePreparedConfirmation(prepared.text) { withContext(Dispatchers.IO) { ContactsTool(context).update(prepared) } }
            }
            "phone_calendar_update_event" -> {
                val prepared = CalendarTools(context).prepareUpdate(args)
                PhonePreparedConfirmation(prepared.text) { withContext(Dispatchers.IO) { CalendarTools(context).update(prepared) } }
            }
            "phone_calendar_delete_event" -> {
                val prepared = CalendarTools(context).prepareDelete(args)
                PhonePreparedConfirmation(prepared.text) { withContext(Dispatchers.IO) { CalendarTools(context).delete(prepared) } }
            }
            else -> error("Tool $name does not need confirmation preparation")
        }
    }

    suspend fun execute(name: String, args: JsonObject): PhoneToolResult = withContext(Dispatchers.IO) {
        when (name) {
            "phone_contacts_search" -> ContactsTool(context).search(args)
            "phone_contacts_create" -> ContactsTool(context).create(args)
            "phone_calendar_events" -> CalendarTools(context).events(args)
            "phone_calendar_create_event" -> CalendarTools(context).create(args)
            "phone_photos_recent" -> PhotosTool(context).recent(args)
            "phone_photo_get" -> PhotosTool(context).get(args)
            "phone_location_current" -> {
                if (!foreground()) PhoneToolText.needsForeground() else LocationTool(context).current()
            }
            "phone_health_summary" -> Health.summary(context, args)
            "phone_workouts_recent" -> Workouts.recent(context, args)
            "phone_workout_detail" -> Workouts.detail(context, args)
            "phone_device_info" -> device()
            "phone_clipboard_get", "phone_clipboard_set" -> withContext(Dispatchers.Main.immediate) {
                if (!foreground()) return@withContext PhoneToolText.needsForeground()
                val clipboard = context.getSystemService(ClipboardManager::class.java)
                if (name == "phone_clipboard_set") {
                    // An empty string is a request too: it clears the clipboard (iOS keeps it writable).
                    val text = args.raw("text") ?: invalid("text is required")
                    clipboard.setPrimaryClip(ClipData.newPlainText("Persona", text))
                    PhoneToolResult.text(buildJsonObject { put("ok", true); put("characters", text.characters()) }.toString())
                } else {
                    val text = clipboard.primaryClip?.takeIf { it.itemCount > 0 }?.getItemAt(0)?.coerceToText(context)?.toString()
                    PhoneToolResult.text(buildJsonObject {
                        if (text.isNullOrEmpty()) {
                            put("text", JsonNull); put("note", "The clipboard is empty or does not hold text.")
                        } else { put("text", text); put("characters", text.characters()) }
                    }.toString())
                }
            }
            "phone_call_start" -> if (foreground()) CallTool.start(context, args) else PhoneToolText.needsForeground()
            "phone_notify" -> PhoneNotifications(context).schedule(args)
            "phone_notifications_list" -> PhoneNotifications(context).list(args.int("limit", 50, 1, 100))
            else -> PhoneToolText.unknownTool(name, emptyList())
        }
    }

    /**
     * Undo from the assistant's conversation (iOS `CalendarTool.remove`): the event it just added comes off the phone
     * directly, without the before → after card `phone_calendar_delete_event` asks for, because the user is taking back
     * the assistant's own write. Provider work stays on IO; a refusal throws.
     */
    suspend fun removeCalendarEvent(id: Long): PhoneToolResult = withContext(Dispatchers.IO) {
        CalendarTools(context).remove(id)
    }

    /** iOS DeviceTool's keys (app_version, app_build, app_channel too); android_version and sdk stand where iOS has ios_version. */
    private fun device(): PhoneToolResult {
        val battery = context.getSystemService(BatteryManager::class.java)
        val power = context.getSystemService(PowerManager::class.java)
        val storage = StatFs(Environment.getDataDirectory().path)
        val network = context.getSystemService(ConnectivityManager::class.java)
        val level = battery.getIntProperty(BatteryManager.BATTERY_PROPERTY_CAPACITY)
        val status = battery.getIntProperty(BatteryManager.BATTERY_PROPERTY_STATUS)
        val locale = Locale.getDefault()
        return PhoneToolResult.text(buildJsonObject {
            put("model", Build.MODEL)
            put("model_name", listOf(Build.MANUFACTURER, Build.MODEL).filter { it.isNotBlank() }.joinToString(" "))
            put("system", "Android ${Build.VERSION.RELEASE}")
            put("android_version", Build.VERSION.RELEASE); put("sdk", Build.VERSION.SDK_INT)
            if (level in 0..100) put("battery_percent", level) else put("battery_percent", "unknown")
            put("battery_state", batteryState(status))
            put("low_power_mode", power.isPowerSaveMode)
            put("storage_free_bytes", storage.availableBytes); put("storage_total_bytes", storage.totalBytes)
            put("network", networkType(network.getNetworkCapabilities(network.activeNetwork)))
            val zone = ZoneId.systemDefault()
            put("timezone", zone.id); put("utc_offset_seconds", zone.rules.getOffset(java.time.Instant.now()).totalSeconds)
            put("locale", if (locale.country.isEmpty()) locale.language else "${locale.language}_${locale.country}")
            put("preferred_languages", buildJsonArray {
                val languages = LocaleList.getDefault()
                for (i in 0 until languages.size()) add(languages[i].toLanguageTag())
            })
            put("thermal_state", thermalState(power.currentThermalStatus))
            val app = context.packageManager.getPackageInfo(context.packageName, 0)
            put("app_version", app.versionName.orEmpty()); put("app_build", app.longVersionCode.toString())
            put("app_channel", appChannel(context.applicationInfo.flags))
        }.toString())
    }

    private fun networkType(capabilities: NetworkCapabilities?): String = when {
        capabilities == null -> "offline"
        capabilities.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) -> "wifi"
        capabilities.hasTransport(NetworkCapabilities.TRANSPORT_CELLULAR) -> "cellular"
        capabilities.hasTransport(NetworkCapabilities.TRANSPORT_ETHERNET) -> "wired"
        else -> "other"
    }
}

/** iOS's app_channel: a debuggable build is "debug", any other "release". */
internal fun appChannel(flags: Int): String = if (flags and android.content.pm.ApplicationInfo.FLAG_DEBUGGABLE != 0) "debug" else "release"

internal fun batteryState(status: Int): String = when (status) {
    BatteryManager.BATTERY_STATUS_CHARGING -> "charging"
    BatteryManager.BATTERY_STATUS_FULL -> "full"
    BatteryManager.BATTERY_STATUS_DISCHARGING, BatteryManager.BATTERY_STATUS_NOT_CHARGING -> "unplugged"
    else -> "unknown"
}

/** iOS ProcessInfo.ThermalState's names over Android's seven levels. */
internal fun thermalState(status: Int): String = when (status) {
    PowerManager.THERMAL_STATUS_NONE -> "nominal"
    PowerManager.THERMAL_STATUS_LIGHT, PowerManager.THERMAL_STATUS_MODERATE -> "fair"
    PowerManager.THERMAL_STATUS_SEVERE -> "serious"
    else -> if (status > PowerManager.THERMAL_STATUS_SEVERE) "critical" else "unknown"
}

/** Characters as people count them (code points), not UTF-16 units. */
internal fun String.characters(): Int = codePointCount(0, length)
