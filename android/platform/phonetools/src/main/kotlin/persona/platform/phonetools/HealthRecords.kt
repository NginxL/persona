package persona.platform.phonetools

import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import androidx.health.connect.client.HealthConnectClient
import androidx.health.connect.client.changes.DeletionChange
import androidx.health.connect.client.changes.UpsertionChange
import androidx.health.connect.client.permission.HealthPermission
import androidx.health.connect.client.records.ActiveCaloriesBurnedRecord
import androidx.health.connect.client.records.BasalBodyTemperatureRecord
import androidx.health.connect.client.records.BasalMetabolicRateRecord
import androidx.health.connect.client.records.BloodGlucoseRecord
import androidx.health.connect.client.records.BloodPressureRecord
import androidx.health.connect.client.records.BodyFatRecord
import androidx.health.connect.client.records.BodyTemperatureRecord
import androidx.health.connect.client.records.BodyWaterMassRecord
import androidx.health.connect.client.records.BoneMassRecord
import androidx.health.connect.client.records.CervicalMucusRecord
import androidx.health.connect.client.records.CyclingPedalingCadenceRecord
import androidx.health.connect.client.records.DistanceRecord
import androidx.health.connect.client.records.ElevationGainedRecord
import androidx.health.connect.client.records.ExerciseSessionRecord
import androidx.health.connect.client.records.FloorsClimbedRecord
import androidx.health.connect.client.records.HeartRateRecord
import androidx.health.connect.client.records.HeartRateVariabilityRmssdRecord
import androidx.health.connect.client.records.HeightRecord
import androidx.health.connect.client.records.HydrationRecord
import androidx.health.connect.client.records.IntermenstrualBleedingRecord
import androidx.health.connect.client.records.LeanBodyMassRecord
import androidx.health.connect.client.records.MenstruationFlowRecord
import androidx.health.connect.client.records.MenstruationPeriodRecord
import androidx.health.connect.client.records.NutritionRecord
import androidx.health.connect.client.records.OvulationTestRecord
import androidx.health.connect.client.records.OxygenSaturationRecord
import androidx.health.connect.client.records.PowerRecord
import androidx.health.connect.client.records.Record
import androidx.health.connect.client.records.RespiratoryRateRecord
import androidx.health.connect.client.records.RestingHeartRateRecord
import androidx.health.connect.client.records.SexualActivityRecord
import androidx.health.connect.client.records.SleepSessionRecord
import androidx.health.connect.client.records.SpeedRecord
import androidx.health.connect.client.records.StepsCadenceRecord
import androidx.health.connect.client.records.StepsRecord
import androidx.health.connect.client.records.TotalCaloriesBurnedRecord
import androidx.health.connect.client.records.Vo2MaxRecord
import androidx.health.connect.client.records.WeightRecord
import androidx.health.connect.client.records.WheelchairPushesRecord
import androidx.health.connect.client.records.metadata.Device
import androidx.health.connect.client.request.ChangesTokenRequest
import androidx.health.connect.client.request.ReadRecordsRequest
import androidx.health.connect.client.time.TimeRangeFilter
import androidx.health.connect.client.units.Mass
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonArray
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import persona.core.contracts.api.PhoneToolResult
import java.time.Duration
import java.time.Instant
import java.util.Base64
import kotlin.reflect.KClass

/**
 * The generic health tools on Health Connect (#13740, iOS `HealthRecords`): every record type this build declares, read
 * record by record with its source package, device and metadata; pages and incremental reads through one opaque anchor
 * (Health Connect's page token, then its changes token); the apps and devices behind them; and access per type. There is
 * no `phone_health_watch`: the hub never wakes an Android phone and only calls it while Persona is on screen, so a
 * background snapshot would answer nobody. Health data goes back only in the tool result; nothing here logs it.
 */
internal object HealthRecords {
    const val PRIVACY = "Health data goes only to the hub that asked and is not written to logs."

    /** One record type: the name the tools take, its kind and the unit its main value comes in. */
    data class Type(val name: String, val kind: String, val record: KClass<out Record>, val unit: String? = null) {
        val permission: String get() = HealthPermission.getReadPermission(record)

        /** The record class as ReadRecordsRequest's type parameter wants it (erased at run time either way). */
        @Suppress("UNCHECKED_CAST")
        val readable: KClass<Record> get() = record as KClass<Record>
    }

    val types: List<Type> = listOf(
        Type("active_calories_burned", "quantity", ActiveCaloriesBurnedRecord::class, "kcal"),
        Type("basal_body_temperature", "quantity", BasalBodyTemperatureRecord::class, "degC"),
        Type("basal_metabolic_rate", "quantity", BasalMetabolicRateRecord::class, "kcal/day"),
        Type("blood_glucose", "quantity", BloodGlucoseRecord::class, "mg/dL"),
        Type("blood_pressure", "correlation", BloodPressureRecord::class, "mmHg"),
        Type("body_fat", "quantity", BodyFatRecord::class, "%"),
        Type("body_temperature", "quantity", BodyTemperatureRecord::class, "degC"),
        Type("body_water_mass", "quantity", BodyWaterMassRecord::class, "kg"),
        Type("bone_mass", "quantity", BoneMassRecord::class, "kg"),
        Type("cervical_mucus", "category", CervicalMucusRecord::class),
        Type("cycling_pedaling_cadence", "series", CyclingPedalingCadenceRecord::class, "rpm"),
        Type("distance", "quantity", DistanceRecord::class, "m"),
        Type("elevation_gained", "quantity", ElevationGainedRecord::class, "m"),
        Type("exercise_session", "workout", ExerciseSessionRecord::class),
        Type("floors_climbed", "quantity", FloorsClimbedRecord::class, "count"),
        Type("heart_rate", "series", HeartRateRecord::class, "count/min"),
        Type("heart_rate_variability_rmssd", "quantity", HeartRateVariabilityRmssdRecord::class, "ms"),
        Type("height", "quantity", HeightRecord::class, "m"),
        Type("hydration", "quantity", HydrationRecord::class, "L"),
        Type("intermenstrual_bleeding", "category", IntermenstrualBleedingRecord::class),
        Type("lean_body_mass", "quantity", LeanBodyMassRecord::class, "kg"),
        Type("menstruation_flow", "category", MenstruationFlowRecord::class),
        Type("menstruation_period", "category", MenstruationPeriodRecord::class),
        Type("nutrition", "correlation", NutritionRecord::class, "g"),
        Type("ovulation_test", "category", OvulationTestRecord::class),
        Type("oxygen_saturation", "quantity", OxygenSaturationRecord::class, "%"),
        Type("power", "series", PowerRecord::class, "W"),
        Type("respiratory_rate", "quantity", RespiratoryRateRecord::class, "count/min"),
        Type("resting_heart_rate", "quantity", RestingHeartRateRecord::class, "count/min"),
        Type("sexual_activity", "category", SexualActivityRecord::class),
        Type("sleep_session", "category", SleepSessionRecord::class),
        Type("speed", "series", SpeedRecord::class, "m/s"),
        Type("steps", "quantity", StepsRecord::class, "count"),
        Type("steps_cadence", "series", StepsCadenceRecord::class, "steps/min"),
        Type("total_calories_burned", "quantity", TotalCaloriesBurnedRecord::class, "kcal"),
        Type("vo2_max", "quantity", Vo2MaxRecord::class, "mL/(kg*min)"),
        Type("weight", "quantity", WeightRecord::class, "kg"),
        Type("wheelchair_pushes", "quantity", WheelchairPushesRecord::class, "count"),
    )

    /** The type a tool argument names; iOS names of the same data are accepted too (step_count, body_mass, ...). */
    fun type(raw: String?): Type? {
        val name = raw?.trim()?.lowercase()?.takeIf { it.isNotEmpty() } ?: return null
        val alias = mapOf(
            "step_count" to "steps", "body_mass" to "weight", "sleep_analysis" to "sleep_session", "workout" to "exercise_session",
            "active_energy_burned" to "active_calories_burned", "distance_walking_running" to "distance", "food" to "nutrition",
            "flights_climbed" to "floors_climbed", "vo2max" to "vo2_max",
        )[name] ?: name
        return types.firstOrNull { it.name == alias || it.record.simpleName?.lowercase() == alias }
    }

    private fun typeArgument(args: JsonObject): Type {
        val raw = args.text("type") ?: invalid("type is required: a type name from phone_health_types")
        return type(raw) ?: invalid("unknown health type \"$raw\"; call phone_health_types for the names")
    }

    /** The types a `types` array names, each once; `"all"` is every type. */
    internal fun typeList(args: JsonObject): List<Type> {
        val names = (args["types"] as? JsonArray).orEmpty().mapNotNull { (it as? JsonPrimitive)?.takeIf { p -> p.isString }?.content }
        if (names.isEmpty()) invalid("types is required: type names from phone_health_types")
        if (names.any { it.equals("all", ignoreCase = true) }) return types
        return names.map { type(it) ?: invalid("unknown health type \"$it\"; call phone_health_types for the names") }.distinct()
    }

    // MARK: Access

    /** The permissions this build's manifest asks for: Health Connect grants nothing else. */
    fun declared(c: Context): Set<String> = runCatching {
        @Suppress("DEPRECATION")
        c.packageManager.getPackageInfo(c.packageName, PackageManager.GET_PERMISSIONS).requestedPermissions?.toSet()
    }.getOrNull().orEmpty()

    /** granted / not_granted, as Health Connect tells; unavailable when this build does not declare the permission. */
    fun access(type: Type, granted: Set<String>, declared: Set<String>): String = when (type.permission) {
        in granted -> "granted"
        !in declared -> "unavailable"
        else -> "not_granted"
    }

    private fun accessRow(type: Type, access: String) = buildJsonObject {
        put("type", type.name)
        put("identifier", type.record.simpleName ?: type.name)
        put("kind", type.kind)
        type.unit?.let { put("unit", it) }
        put("access", access)
        if (access == "unavailable") put("reason", "This build of Persona does not declare the Health Connect permission for this type.")
    }

    private fun requireHealthConnect(c: Context) {
        Health.modelReason(c)?.let { unavailable("$it / Health Connect is not available on this Android phone, so there is no health data to read.") }  // i18n-ignore: tool result for the model
    }

    suspend fun types(c: Context, args: JsonObject): PhoneToolResult {
        requireHealthConnect(c)
        val kind = args.text("kind")
        val granted = Health.granted(c)
        val declared = declared(c)
        val rows = types.filter { kind == null || it.kind == kind }.map { accessRow(it, access(it, granted, declared)) }
        return PhoneToolResult.text(buildJsonObject {
            put("types", JsonArray(rows))
            put("count", rows.size)
            put("note", "Health Connect says which types Persona may read: granted or not_granted. $PRIVACY")
        }.toString())
    }

    suspend fun requestAccess(c: Context, args: JsonObject, foreground: Boolean): PhoneToolResult {
        requireHealthConnect(c)
        val wanted = typeList(args)
        if (!foreground) {
            return PhoneToolResult.text(buildJsonObject {
                put("status", "needs_foreground")
                put("note", "Persona is not open on the phone, so Health Connect's permission page cannot show. Ask the user to open Persona, then call phone_health_request_access again.")
            }.toString())
        }
        val granted = Health.granted(c)
        val declared = declared(c)
        val missing = wanted.filter { access(it, granted, declared) == "not_granted" }
        var opened = false
        if (missing.isNotEmpty()) {
            // Health Connect asks through an activity result; a tool call has none, so it opens Persona's page in Health
            // Connect, where the user switches the types on.
            val intent = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE) {
                Intent("android.health.connect.action.MANAGE_HEALTH_PERMISSIONS").putExtra(Intent.EXTRA_PACKAGE_NAME, c.packageName)
            } else {
                Intent(HealthConnectClient.ACTION_HEALTH_CONNECT_SETTINGS)
            }
            opened = runCatching { c.startActivity(intent.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)) }.isSuccess
        }
        return PhoneToolResult.text(buildJsonObject {
            put("asked", opened)
            put("types", JsonArray(wanted.map { accessRow(it, access(it, granted, declared)) }))
            put("note", when {
                opened -> "Health Connect's permission page for Persona is open on the phone; the access above is from before the user chose. Call phone_health_types once they are back."
                missing.isEmpty() -> "Nothing to ask: every type here is granted or not declared by this build."
                else -> "Health Connect's permission page could not be opened on this phone."
            })
        }.toString())
    }

    // MARK: Samples

    /** `p1.<page token>.<changes token>` for the next page, `c1.<changes token>` for what changed since. */
    internal sealed interface Anchor {
        data class Page(val pageToken: String, val changesToken: String) : Anchor
        data class Changes(val changesToken: String) : Anchor
    }

    private fun b64(text: String) = Base64.getUrlEncoder().withoutPadding().encodeToString(text.toByteArray())
    private fun unb64(text: String) = runCatching { String(Base64.getUrlDecoder().decode(text)) }.getOrNull()

    internal fun anchorText(anchor: Anchor): String = when (anchor) {
        is Anchor.Page -> "p1.${b64(anchor.pageToken)}.${b64(anchor.changesToken)}"
        is Anchor.Changes -> "c1.${b64(anchor.changesToken)}"
    }

    internal fun parseAnchor(text: String): Anchor? {
        val parts = text.split(".")
        return when {
            parts.size == 3 && parts[0] == "p1" -> {
                val page = unb64(parts[1]) ?: return null
                val changes = unb64(parts[2]) ?: return null
                Anchor.Page(page, changes)
            }
            parts.size == 2 && parts[0] == "c1" -> unb64(parts[1])?.let { Anchor.Changes(it) }
            else -> null
        }
    }

    suspend fun samples(c: Context, args: JsonObject, now: Instant = Instant.now()): PhoneToolResult {
        requireHealthConnect(c)
        val type = typeArgument(args)
        val start = parseTime(args.text("start")) ?: now.minus(Duration.ofDays(7))
        val end = parseTime(args.text("end"))
        if (end != null && !end.isAfter(start)) invalid("end must be after start")
        val limit = args.int("limit", 100, 1, 500)
        val anchor = args.text("anchor")?.let { parseAnchor(it) ?: invalid("anchor is not one this phone returned; leave it out to start from the beginning") }
        val base = buildJsonObject {
            put("type", type.name)
            put("identifier", type.record.simpleName ?: type.name)
            put("kind", type.kind)
            put("start", iso(start))
            end?.let { put("end", iso(it)) }
            type.unit?.let { put("unit", it) }
        }
        val status = access(type, Health.granted(c), declared(c))
        if (status != "granted") {
            return PhoneToolResult.text(JsonObject(base + buildJsonObject {
                put("access", status)
                put("samples", JsonArray(emptyList()))
                put("note", if (status == "unavailable") "This build of Persona does not declare this type's Health Connect permission."
                    else "Persona may not read this type yet: call phone_health_request_access with it while Persona is open on the phone.")
            }).toString())
        }
        val client = HealthConnectClient.getOrCreate(c)
        val filter = if (end == null) TimeRangeFilter.after(start) else TimeRangeFilter.between(start, end)
        val origins = Origins(c)
        val out = linkedMapOf<String, JsonElement>()
        when (anchor) {
            is Anchor.Changes -> {
                val changes = client.getChanges(anchor.changesToken)
                if (changes.changesTokenExpired) {
                    return PhoneToolResult.text(JsonObject(base + buildJsonObject {
                        put("access", status); put("status", "anchor_expired"); put("samples", JsonArray(emptyList()))
                        put("note", "Health Connect keeps changes for a limited time: read the window again without an anchor.")
                    }).toString())
                }
                val upserts = changes.changes.filterIsInstance<UpsertionChange>().map { it.record }
                    .filter { type.record.isInstance(it) && inWindow(timeOf(it).second, start, end) }
                out["samples"] = JsonArray(upserts.map { recordJson(it, origins) })
                out["deleted"] = JsonArray(changes.changes.filterIsInstance<DeletionChange>().map { JsonPrimitive(it.recordId) })
                out["more"] = JsonPrimitive(changes.hasMore)
                out["anchor"] = JsonPrimitive(anchorText(Anchor.Changes(changes.nextChangesToken)))
            }
            else -> {
                // The changes token comes first, so nothing saved while the pages are read is missed later.
                val changesToken = (anchor as? Anchor.Page)?.changesToken ?: client.getChangesToken(ChangesTokenRequest(setOf(type.record)))
                val response = client.readRecords(ReadRecordsRequest(type.readable, filter, ascendingOrder = true, pageSize = limit,
                    pageToken = (anchor as? Anchor.Page)?.pageToken))
                val page = response.pageToken?.takeIf { it.isNotEmpty() }
                out["samples"] = JsonArray(response.records.map { recordJson(it, origins) })
                out["deleted"] = JsonArray(emptyList())
                out["more"] = JsonPrimitive(page != null)
                out["anchor"] = JsonPrimitive(anchorText(if (page != null) Anchor.Page(page, changesToken) else Anchor.Changes(changesToken)))
            }
        }
        return PhoneToolResult.text(JsonObject(base + mapOf("access" to JsonPrimitive(status)) + out + mapOf("source" to JsonPrimitive("live"))).toString())
    }

    internal fun inWindow(end: Instant, start: Instant, windowEnd: Instant?) = !end.isBefore(start) && (windowEnd == null || !end.isAfter(windowEnd))

    /** App labels for the packages records come from, looked up once per call. */
    internal class Origins(private val c: Context?) {
        private val labels = mutableMapOf<String, String?>()
        fun source(packageName: String): JsonObject = buildJsonObject {
            val label = labels.getOrPut(packageName) {
                c?.let { ctx ->
                    runCatching {
                        ctx.packageManager.getApplicationLabel(ctx.packageManager.getApplicationInfo(packageName, 0)).toString()
                    }.getOrNull()
                }
            }
            put("name", label ?: packageName)
            put("package", packageName)
        }
    }

    internal fun deviceJson(device: Device?): JsonObject? {
        device ?: return null
        val kind = when (device.type) {
            Device.TYPE_WATCH -> "watch"; Device.TYPE_PHONE -> "phone"; Device.TYPE_SCALE -> "scale"; Device.TYPE_RING -> "ring"
            Device.TYPE_HEAD_MOUNTED -> "head_mounted"; Device.TYPE_FITNESS_BAND -> "fitness_band"; Device.TYPE_CHEST_STRAP -> "chest_strap"
            Device.TYPE_SMART_DISPLAY -> "smart_display"; else -> null
        }
        val json = buildJsonObject {
            device.manufacturer?.let { put("manufacturer", it) }
            device.model?.let { put("model", it) }
            kind?.let { put("type", it) }
        }
        return json.takeIf { it.isNotEmpty() }
    }

    /** start and end of any record; an instantaneous one starts and ends at its time. */
    internal fun timeOf(r: Record): Pair<Instant, Instant> = when (r) {
        is ActiveCaloriesBurnedRecord -> r.startTime to r.endTime
        is CyclingPedalingCadenceRecord -> r.startTime to r.endTime
        is DistanceRecord -> r.startTime to r.endTime
        is ElevationGainedRecord -> r.startTime to r.endTime
        is ExerciseSessionRecord -> r.startTime to r.endTime
        is FloorsClimbedRecord -> r.startTime to r.endTime
        is HeartRateRecord -> r.startTime to r.endTime
        is HydrationRecord -> r.startTime to r.endTime
        is MenstruationPeriodRecord -> r.startTime to r.endTime
        is NutritionRecord -> r.startTime to r.endTime
        is PowerRecord -> r.startTime to r.endTime
        is SleepSessionRecord -> r.startTime to r.endTime
        is SpeedRecord -> r.startTime to r.endTime
        is StepsRecord -> r.startTime to r.endTime
        is StepsCadenceRecord -> r.startTime to r.endTime
        is TotalCaloriesBurnedRecord -> r.startTime to r.endTime
        is WheelchairPushesRecord -> r.startTime to r.endTime
        is BasalBodyTemperatureRecord -> r.time to r.time
        is BasalMetabolicRateRecord -> r.time to r.time
        is BloodGlucoseRecord -> r.time to r.time
        is BloodPressureRecord -> r.time to r.time
        is BodyFatRecord -> r.time to r.time
        is BodyTemperatureRecord -> r.time to r.time
        is BodyWaterMassRecord -> r.time to r.time
        is BoneMassRecord -> r.time to r.time
        is CervicalMucusRecord -> r.time to r.time
        is HeartRateVariabilityRmssdRecord -> r.time to r.time
        is HeightRecord -> r.time to r.time
        is IntermenstrualBleedingRecord -> r.time to r.time
        is LeanBodyMassRecord -> r.time to r.time
        is MenstruationFlowRecord -> r.time to r.time
        is OvulationTestRecord -> r.time to r.time
        is OxygenSaturationRecord -> r.time to r.time
        is RespiratoryRateRecord -> r.time to r.time
        is RestingHeartRateRecord -> r.time to r.time
        is SexualActivityRecord -> r.time to r.time
        is Vo2MaxRecord -> r.time to r.time
        is WeightRecord -> r.time to r.time
        else -> r.metadata.lastModifiedTime to r.metadata.lastModifiedTime
    }

    private fun Mass?.grams(): Double? = this?.inGrams

    /** One record as phone_health_samples returns it: the same fields for every type, then the type's own. */
    internal fun recordJson(r: Record, origins: Origins): JsonObject {
        val (start, end) = timeOf(r)
        return buildJsonObject {
            put("id", r.metadata.id)
            put("start", iso(start))
            put("end", iso(end))
            put("source", origins.source(r.metadata.dataOrigin.packageName))
            deviceJson(r.metadata.device)?.let { put("device", it) }
            put("metadata", buildJsonObject {
                put("last_modified", iso(r.metadata.lastModifiedTime))
                r.metadata.clientRecordId?.let { put("client_record_id", it) }
                put("recording_method", r.metadata.recordingMethod)
            })
            for ((key, value) in fields(r)) put(key, value)
        }
    }

    internal fun fields(r: Record): Map<String, JsonElement> {
        val out = linkedMapOf<String, JsonElement>()
        fun n(key: String, v: Number?) { if (v != null) out[key] = JsonPrimitive(v) }
        fun s(key: String, v: String?) { if (v != null) out[key] = JsonPrimitive(v) }
        fun series(list: List<Pair<Instant, Number>>) {
            out["samples"] = buildJsonArray { list.forEach { (t, v) -> add(buildJsonObject { put("time", iso(t)); put("value", v) }) } }
        }
        when (r) {
            is ActiveCaloriesBurnedRecord -> n("value", r.energy.inKilocalories)
            is TotalCaloriesBurnedRecord -> n("value", r.energy.inKilocalories)
            is BasalBodyTemperatureRecord -> { n("value", r.temperature.inCelsius); n("measurement_location", r.measurementLocation) }
            is BodyTemperatureRecord -> { n("value", r.temperature.inCelsius); n("measurement_location", r.measurementLocation) }
            is BasalMetabolicRateRecord -> n("value", r.basalMetabolicRate.inKilocaloriesPerDay)
            is BloodGlucoseRecord -> {
                n("value", r.level.inMilligramsPerDeciliter); n("meal_type", r.mealType)
                n("relation_to_meal", r.relationToMeal); n("specimen_source", r.specimenSource)
            }
            is BloodPressureRecord -> {
                n("systolic", r.systolic.inMillimetersOfMercury); n("diastolic", r.diastolic.inMillimetersOfMercury)
                n("body_position", r.bodyPosition); n("measurement_location", r.measurementLocation)
            }
            is BodyFatRecord -> n("value", r.percentage.value)
            is OxygenSaturationRecord -> n("value", r.percentage.value)
            is BodyWaterMassRecord -> n("value", r.mass.inKilograms)
            is BoneMassRecord -> n("value", r.mass.inKilograms)
            is LeanBodyMassRecord -> n("value", r.mass.inKilograms)
            is WeightRecord -> n("value", r.weight.inKilograms)
            is HeightRecord -> n("value", r.height.inMeters)
            is DistanceRecord -> n("value", r.distance.inMeters)
            is ElevationGainedRecord -> n("value", r.elevation.inMeters)
            is FloorsClimbedRecord -> n("value", r.floors)
            is StepsRecord -> n("value", r.count)
            is WheelchairPushesRecord -> n("value", r.count)
            is HydrationRecord -> n("value", r.volume.inLiters)
            is HeartRateVariabilityRmssdRecord -> n("value", r.heartRateVariabilityMillis)
            is RespiratoryRateRecord -> n("value", r.rate)
            is RestingHeartRateRecord -> n("value", r.beatsPerMinute)
            is Vo2MaxRecord -> { n("value", r.vo2MillilitersPerMinuteKilogram); n("measurement_method", r.measurementMethod) }
            is HeartRateRecord -> series(r.samples.map { it.time to it.beatsPerMinute })
            is PowerRecord -> series(r.samples.map { it.time to it.power.inWatts })
            is SpeedRecord -> series(r.samples.map { it.time to it.speed.inMetersPerSecond })
            is StepsCadenceRecord -> series(r.samples.map { it.time to it.rate })
            is CyclingPedalingCadenceRecord -> series(r.samples.map { it.time to it.revolutionsPerMinute })
            is CervicalMucusRecord -> { n("appearance", r.appearance); n("sensation", r.sensation) }
            is MenstruationFlowRecord -> n("value", r.flow)
            is OvulationTestRecord -> n("value", r.result)
            is SexualActivityRecord -> n("protection_used", r.protectionUsed)
            is ExerciseSessionRecord -> { n("exercise_type", r.exerciseType); s("title", r.title); s("notes", r.notes) }
            is SleepSessionRecord -> {
                s("title", r.title); s("notes", r.notes)
                out["stages"] = buildJsonArray {
                    r.stages.forEach { st -> add(buildJsonObject { put("start", iso(st.startTime)); put("end", iso(st.endTime)); put("stage", st.stage) }) }
                }
            }
            is NutritionRecord -> {
                s("name", r.name); n("meal_type", r.mealType)
                n("energy_kcal", r.energy?.inKilocalories)
                n("protein_g", r.protein.grams()); n("total_carbohydrate_g", r.totalCarbohydrate.grams()); n("total_fat_g", r.totalFat.grams())
                n("saturated_fat_g", r.saturatedFat.grams()); n("sugar_g", r.sugar.grams()); n("dietary_fiber_g", r.dietaryFiber.grams())
                n("sodium_g", r.sodium.grams()); n("cholesterol_g", r.cholesterol.grams()); n("caffeine_g", r.caffeine.grams())
                n("potassium_g", r.potassium.grams()); n("calcium_g", r.calcium.grams()); n("iron_g", r.iron.grams())
            }
            else -> {}
        }
        return out
    }

    // MARK: Sources

    suspend fun sources(c: Context, args: JsonObject, now: Instant = Instant.now()): PhoneToolResult {
        requireHealthConnect(c)
        val only = args.text("type")?.let { typeArgument(args) }
        val days = args.int("days", 30, 1, 3650)
        val start = now.minus(Duration.ofDays(days.toLong()))
        val granted = Health.granted(c)
        val client = HealthConnectClient.getOrCreate(c)
        val origins = Origins(c)
        val byPackage = sortedMapOf<String, Pair<JsonObject, MutableList<String>>>()
        val devices = linkedMapOf<String, JsonObject>()
        for (type in only?.let { listOf(it) } ?: types) {
            if (type.permission !in granted) continue
            val records = runCatching {
                client.readRecords(ReadRecordsRequest(type.readable, TimeRangeFilter.after(start), ascendingOrder = false, pageSize = 1000)).records
            }.getOrNull() ?: continue
            for (r in records) {
                val pkg = r.metadata.dataOrigin.packageName
                val row = byPackage.getOrPut(pkg) { origins.source(pkg) to mutableListOf() }
                if (type.name !in row.second) row.second += type.name
                if (only != null) deviceJson(r.metadata.device)?.let { devices[it.toString()] = it }
            }
        }
        return PhoneToolResult.text(buildJsonObject {
            put("days", days)
            only?.let { put("type", it.name) }
            put("sources", buildJsonArray {
                byPackage.values.forEach { (source, types) ->
                    add(JsonObject(source + mapOf("types" to JsonArray(types.map(::JsonPrimitive)))))
                }
            })
            if (only != null) put("devices", JsonArray(devices.values.toList()))
            put("source", "live")
            put("note", (if (only == null) "Types Persona may not read are left out; the newest 1000 records of each type are looked at. Pass type for the devices behind one type. " else "") + PRIVACY)
        }.toString())
    }
}
