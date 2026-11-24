#if os(iOS)
import Foundation
import HealthKit
import PersonaWire

/// Every Apple Health type the generic health tools know (#13740): `phone_health_types` lists them, `phone_health_samples`,
/// `phone_health_sources`, `phone_health_request_access` and `phone_health_watch` take their names. HealthKit has no API
/// that enumerates its identifiers, so they are listed here by raw value; a type the running iOS does not have yields no
/// `HKObjectType` and is listed as unsupported instead of crashing (the `forIdentifier:` constructors return nil).
nonisolated enum HealthCatalog {
    enum Kind: String, Sendable, CaseIterable {
        case quantity, category, correlation, workout, electrocardiogram, audiogram
        case heartbeatSeries = "heartbeat_series", workoutRoute = "workout_route"
        case activitySummary = "activity_summary", stateOfMind = "state_of_mind", clinical
    }

    struct Entry: Sendable, Equatable {
        /// What the tools take: snake case of the identifier without its HealthKit prefix (`step_count`).
        let name: String
        /// HealthKit's own identifier (`HKQuantityTypeIdentifierStepCount`); also accepted as a type name.
        let identifier: String
        let kind: Kind
    }

    // MARK: - Identifiers

    static let quantityPrefix = "HKQuantityTypeIdentifier"
    static let categoryPrefix = "HKCategoryTypeIdentifier"
    static let correlationPrefix = "HKCorrelationTypeIdentifier"
    static let clinicalPrefix = "HKClinicalTypeIdentifier"

    /// Quantity identifiers after `HKQuantityTypeIdentifier`. Deprecated ones (NikeFuel) are left out: HealthKit may refuse
    /// to authorize them, and a refused type in a request raises an exception.
    static let quantityIdentifiers: [String] = [
        // Body measurements
        "BodyMassIndex", "BodyFatPercentage", "Height", "BodyMass", "LeanBodyMass", "WaistCircumference",
        "AppleSleepingWristTemperature",
        // Fitness
        "StepCount", "DistanceWalkingRunning", "DistanceCycling", "DistanceWheelchair", "BasalEnergyBurned",
        "ActiveEnergyBurned", "FlightsClimbed", "AppleExerciseTime", "PushCount", "DistanceSwimming",
        "SwimmingStrokeCount", "VO2Max", "DistanceDownhillSnowSports", "AppleStandTime", "AppleMoveTime",
        "PhysicalEffort", "EstimatedWorkoutEffortScore", "WorkoutEffortScore", "TimeInDaylight",
        "DistanceRowing", "RowingSpeed", "DistancePaddleSports", "PaddleSportsSpeed", "DistanceCrossCountrySkiing",
        "CrossCountrySkiingSpeed", "DistanceSkatingSports",
        // Mobility
        "WalkingSpeed", "WalkingDoubleSupportPercentage", "WalkingAsymmetryPercentage", "WalkingStepLength",
        "SixMinuteWalkTestDistance", "StairAscentSpeed", "StairDescentSpeed", "AppleWalkingSteadiness",
        "RunningStrideLength", "RunningVerticalOscillation", "RunningGroundContactTime", "RunningPower", "RunningSpeed",
        "CyclingSpeed", "CyclingPower", "CyclingFunctionalThresholdPower", "CyclingCadence",
        // Vital signs and heart
        "HeartRate", "RestingHeartRate", "WalkingHeartRateAverage", "HeartRateVariabilitySDNN",
        "HeartRateRecoveryOneMinute", "AtrialFibrillationBurden", "OxygenSaturation", "BodyTemperature",
        "BasalBodyTemperature", "BloodPressureSystolic", "BloodPressureDiastolic", "RespiratoryRate",
        "AppleSleepingBreathingDisturbances",
        // Lab and test results
        "BloodGlucose", "ElectrodermalActivity", "InhalerUsage", "InsulinDelivery", "BloodAlcoholContent",
        "ForcedVitalCapacity", "ForcedExpiratoryVolume1", "PeakExpiratoryFlowRate", "PeripheralPerfusionIndex",
        "NumberOfTimesFallen", "NumberOfAlcoholicBeverages",
        // Hearing and environment
        "EnvironmentalAudioExposure", "HeadphoneAudioExposure", "EnvironmentalSoundReduction", "UVExposure",
        "UnderwaterDepth", "WaterTemperature",
        // Nutrition
        "DietaryEnergyConsumed", "DietaryProtein", "DietaryCarbohydrates", "DietaryFiber", "DietarySugar",
        "DietaryFatTotal", "DietaryFatPolyunsaturated", "DietaryFatMonounsaturated", "DietaryFatSaturated",
        "DietaryCholesterol", "DietarySodium", "DietaryVitaminA", "DietaryVitaminB6", "DietaryVitaminB12",
        "DietaryVitaminC", "DietaryVitaminD", "DietaryVitaminE", "DietaryVitaminK", "DietaryCalcium", "DietaryIron",
        "DietaryThiamin", "DietaryRiboflavin", "DietaryNiacin", "DietaryFolate", "DietaryBiotin",
        "DietaryPantothenicAcid", "DietaryPhosphorus", "DietaryIodine", "DietaryMagnesium", "DietaryZinc",
        "DietarySelenium", "DietaryCopper", "DietaryManganese", "DietaryChromium", "DietaryMolybdenum",
        "DietaryChloride", "DietaryPotassium", "DietaryCaffeine", "DietaryWater",
    ]

    /// Symptoms: their value is an `HKCategoryValueSeverity`.
    static let symptomIdentifiers: [String] = [
        "AbdominalCramps", "Acne", "AppetiteChanges", "BladderIncontinence", "Bloating", "BreastPain",
        "ChestTightnessOrPain", "Chills", "Constipation", "Coughing", "Diarrhea", "Dizziness", "DrySkin", "Fainting",
        "Fatigue", "Fever", "GeneralizedBodyAche", "HairLoss", "Headache", "Heartburn", "HotFlashes", "LossOfSmell",
        "LossOfTaste", "LowerBackPain", "MemoryLapse", "MoodChanges", "Nausea", "NightSweats", "PelvicPain",
        "RapidPoundingOrFlutteringHeartbeat", "RunnyNose", "ShortnessOfBreath", "SinusCongestion", "SkippedHeartbeat",
        "SleepChanges", "SoreThroat", "VaginalDryness", "Vomiting", "Wheezing",
    ]

    /// Category identifiers after `HKCategoryTypeIdentifier`, symptoms included. The deprecated AudioExposureEvent is left out.
    static let categoryIdentifiers: [String] = [
        "SleepAnalysis", "AppleStandHour", "MindfulSession", "HighHeartRateEvent", "LowHeartRateEvent",
        "IrregularHeartRhythmEvent", "LowCardioFitnessEvent", "EnvironmentalAudioExposureEvent",
        "HeadphoneAudioExposureEvent", "HandwashingEvent", "ToothbrushingEvent", "AppleWalkingSteadinessEvent",
        "SleepApneaEvent", "MenstrualFlow", "IntermenstrualBleeding", "InfrequentMenstrualCycles",
        "IrregularMenstrualCycles", "PersistentIntermenstrualBleeding", "ProlongedMenstrualPeriods", "OvulationTestResult",
        "CervicalMucusQuality", "SexualActivity", "Contraceptive", "Pregnancy", "PregnancyTestResult",
        "ProgesteroneTestResult", "Lactation", "BleedingAfterPregnancy", "BleedingDuringPregnancy",
    ] + symptomIdentifiers

    static let correlationIdentifiers = ["BloodPressure", "Food"]

    /// Health Records: listed so callers know they exist, never requested (see `unreadableReason`).
    static let clinicalIdentifiers = [
        "AllergyRecord", "ClinicalNoteRecord", "ConditionRecord", "CoverageRecord", "ImmunizationRecord",
        "LabResultRecord", "MedicationRecord", "ProcedureRecord", "VitalSignRecord",
    ]

    // MARK: - Entries

    static let entries: [Entry] = {
        var out: [Entry] = []
        for suffix in quantityIdentifiers { out.append(Entry(name: snakeName(suffix), identifier: quantityPrefix + suffix, kind: .quantity)) }
        for suffix in categoryIdentifiers { out.append(Entry(name: snakeName(suffix), identifier: categoryPrefix + suffix, kind: .category)) }
        for suffix in correlationIdentifiers {
            out.append(Entry(name: snakeName(suffix), identifier: correlationPrefix + suffix, kind: .correlation))
        }
        out.append(Entry(name: "workout", identifier: HKObjectType.workoutType().identifier, kind: .workout))
        out.append(Entry(name: "electrocardiogram", identifier: HKObjectType.electrocardiogramType().identifier, kind: .electrocardiogram))
        out.append(Entry(name: "audiogram", identifier: HKObjectType.audiogramSampleType().identifier, kind: .audiogram))
        out.append(Entry(name: "heartbeat_series", identifier: HKSeriesType.heartbeat().identifier, kind: .heartbeatSeries))
        out.append(Entry(name: "workout_route", identifier: HKSeriesType.workoutRoute().identifier, kind: .workoutRoute))
        out.append(Entry(name: "activity_summary", identifier: HKObjectType.activitySummaryType().identifier, kind: .activitySummary))
        out.append(Entry(name: "state_of_mind", identifier: HKObjectType.stateOfMindType().identifier, kind: .stateOfMind))
        for suffix in clinicalIdentifiers {
            out.append(Entry(name: snakeName(suffix), identifier: clinicalPrefix + suffix, kind: .clinical))
        }
        return out
    }()

    private static let byName: [String: Entry] = {
        var out: [String: Entry] = [:]
        for entry in entries {
            out[entry.name] = entry
            out[entry.identifier.lowercased()] = entry
        }
        return out
    }()

    /// The entry a tool argument names: the snake-case name or HealthKit's identifier, any case.
    static func entry(named raw: String?) -> Entry? {
        guard let raw = raw?.trimmingCharacters(in: .whitespaces), !raw.isEmpty else { return nil }
        return byName[raw.lowercased()] ?? byName[snakeName(raw)]
    }

    /// `StepCount` → `step_count`, `VO2Max` → `vo2_max`, `HeartRateVariabilitySDNN` → `heart_rate_variability_sdnn`,
    /// `UVExposure` → `uv_exposure`.
    static func snakeName(_ camel: String) -> String {
        let chars = Array(camel)
        var out = ""
        for (i, c) in chars.enumerated() {
            if c.isUppercase, i > 0 {
                let previous = chars[i - 1]
                let nextIsLower = i + 1 < chars.count && chars[i + 1].isLowercase
                if previous.isLowercase || previous.isNumber || (previous.isUppercase && nextIsLower) { out.append("_") }
            }
            out.append(contentsOf: c.lowercased())
        }
        return out
    }

    // MARK: - HealthKit types

    static func objectType(_ entry: Entry) -> HKObjectType? {
        switch entry.kind {
        case .quantity: HKObjectType.quantityType(forIdentifier: HKQuantityTypeIdentifier(rawValue: entry.identifier))
        case .category: HKObjectType.categoryType(forIdentifier: HKCategoryTypeIdentifier(rawValue: entry.identifier))
        case .correlation: HKObjectType.correlationType(forIdentifier: HKCorrelationTypeIdentifier(rawValue: entry.identifier))
        case .workout: HKObjectType.workoutType()
        case .electrocardiogram: HKObjectType.electrocardiogramType()
        case .audiogram: HKObjectType.audiogramSampleType()
        case .heartbeatSeries: HKSeriesType.heartbeat()
        case .workoutRoute: HKSeriesType.workoutRoute()
        case .activitySummary: HKObjectType.activitySummaryType()
        case .stateOfMind: HKObjectType.stateOfMindType()
        case .clinical: HKObjectType.clinicalType(forIdentifier: HKClinicalTypeIdentifier(rawValue: entry.identifier))
        }
    }

    /// Why a type is never read, in words for the model; nil when it can be.
    static func unreadableReason(_ entry: Entry) -> String? {
        switch entry.kind {
        case .clinical:
            "Health Records need a separate HealthKit entitlement (clinical records) this build does not have."
        case .workoutRoute:
            "Workout routes are locations; Persona does not read them."
        default:
            nil
        }
    }

    /// What to ask HealthKit for to read the entry. Correlations cannot be authorized themselves: HealthKit raises an
    /// exception for them, so their component types are asked for instead.
    static func authorizationTypes(_ entry: Entry) -> Set<HKObjectType> {
        guard unreadableReason(entry) == nil, let type = objectType(entry) else { return [] }
        guard entry.kind == .correlation else { return [type] }
        let components: [Entry] = entry.name == "blood_pressure"
            ? entries.filter { $0.name == "blood_pressure_systolic" || $0.name == "blood_pressure_diastolic" }
            : entries.filter { $0.kind == .quantity && $0.name.hasPrefix("dietary_") }
        return Set(components.compactMap(objectType))
    }

    /// The readable entries of `kind` (all kinds when nil) whose type this iOS has.
    static func readable(kind: Kind? = nil) -> [Entry] {
        entries.filter { (kind == nil || $0.kind == kind) && !authorizationTypes($0).isEmpty }
    }

    // MARK: - Units

    /// The unit values of a quantity type come in: the first metric unit the type is compatible with. Metric on purpose
    /// (kg, m, °C, kcal), whatever the user's own Health preferences say, so a caller can compare readings.
    static func unit(for type: HKQuantityType) -> HKUnit? {
        candidateUnits(for: type.identifier).first { type.is(compatibleWith: $0) }
    }

    static func candidateUnits(for identifier: String) -> [HKUnit] {
        var list: [HKUnit] = []
        if identifier == quantityPrefix + "BodyMass" || identifier == quantityPrefix + "LeanBodyMass" {
            list.append(.gramUnit(with: .kilo))
        }
        if identifier == quantityPrefix + "BloodGlucose" {
            list.append(.gramUnit(with: .milli).unitDivided(by: .literUnit(with: .deci)))
        }
        if identifier == quantityPrefix + "RunningGroundContactTime" || identifier == quantityPrefix + "HeartRateVariabilitySDNN" {
            list.append(.secondUnit(with: .milli))
        }
        if identifier.contains("Heart") || identifier == quantityPrefix + "RespiratoryRate" || identifier == quantityPrefix + "CyclingCadence" {
            list.append(.count().unitDivided(by: .minute()))
        }
        if identifier == quantityPrefix + "AppleExerciseTime" || identifier == quantityPrefix + "AppleStandTime"
            || identifier == quantityPrefix + "AppleMoveTime" || identifier == quantityPrefix + "TimeInDaylight" {
            list.append(.minute())
        }
        list += [
            .count(), .count().unitDivided(by: .minute()), .meter(), .kilocalorie(), .gram(), .percent(), .second(),
            .degreeCelsius(), .millimeterOfMercury(), .decibelAWeightedSoundPressureLevel(), .liter(),
            .literUnit(with: .milli).unitDivided(by: .gramUnit(with: .kilo).unitMultiplied(by: .minute())),
            .meter().unitDivided(by: .second()), .watt(), .internationalUnit(), .siemenUnit(with: .micro),
            .kilocalorie().unitDivided(by: .gramUnit(with: .kilo).unitMultiplied(by: .hour())),
            .liter().unitDivided(by: .minute()), .appleEffortScore(),
        ]
        return list
    }

    // MARK: - Labels

    /// A category value in words, for the types whose values are an enumeration a caller cannot guess; nil otherwise.
    static func categoryLabel(_ entry: Entry, value: Int) -> String? {
        if entry.name == "sleep_analysis" {
            return pick(["in_bed", "asleep_unspecified", "awake", "asleep_core", "asleep_deep", "asleep_rem"], value)
        }
        if entry.name == "apple_stand_hour" { return pick(["stood", "idle"], value) }
        if symptomIdentifiers.contains(where: { categoryPrefix + $0 == entry.identifier }) {
            return pick(["unspecified", "not_present", "mild", "moderate", "severe"], value)
        }
        if entry.name == "menstrual_flow" { return pick(["", "unspecified", "light", "medium", "heavy", "none"], value) }
        return nil
    }

    private static func pick(_ labels: [String], _ value: Int) -> String? {
        guard labels.indices.contains(value), !labels[value].isEmpty else { return nil }
        return labels[value]
    }
}

#endif
