#if os(iOS)
import Foundation
import HealthKit
import PersonaWire

/// One HealthKit sample as `phone_health_samples` returns it (#13740): the same fields for every kind (id, start, end,
/// source, device, metadata) plus the kind's own. Runs on HealthKit's queue inside the query callback, so it is
/// nonisolated and returns plain JSON. Nothing here is logged.
nonisolated enum HealthSampleJSON {
    /// The longest metadata string kept; app-defined metadata can be arbitrary.
    static let metadataTextLimit = 500

    static func encode(_ sample: HKSample, entry: HealthCatalog.Entry, unit: HKUnit?) -> JSONValue {
        var pairs: [(String, JSONValue?)] = [
            ("id", .string(sample.uuid.uuidString)),
            ("start", .string(ToolDate.string(sample.startDate))),
            ("end", .string(ToolDate.string(sample.endDate))),
            ("source", source(sample.sourceRevision)),
            ("device", device(sample.device)),
            ("metadata", metadata(sample.metadata)),
        ]
        pairs += fields(sample, entry: entry, unit: unit)
        return Output.object(pairs)
    }

    /// The kind's own fields.
    static func fields(_ sample: HKSample, entry: HealthCatalog.Entry, unit: HKUnit?) -> [(String, JSONValue?)] {
        switch sample {
        case let quantity as HKQuantitySample:
            let unit = unit ?? HealthCatalog.unit(for: quantity.quantityType)
            guard let unit, quantity.quantity.is(compatibleWith: unit) else {
                return [("value", nil), ("quantity", .string(quantity.quantity.description))]
            }
            return [
                ("value", .number(quantity.quantity.doubleValue(for: unit))),
                ("unit", .string(unit.unitString)),
                ("count", quantity.count > 1 ? .number(Double(quantity.count)) : nil),
            ]
        case let category as HKCategorySample:
            return [
                ("value", .number(Double(category.value))),
                ("value_label", Output.opt(HealthCatalog.categoryLabel(entry, value: category.value))),
            ]
        case let workout as HKWorkout:
            let energy = workout.statistics(for: HKQuantityType(.activeEnergyBurned))?.sumQuantity()?.doubleValue(for: .kilocalorie())
            return [
                ("activity_type", .number(Double(workout.workoutActivityType.rawValue))),
                ("duration_s", .number(workout.duration)),
                ("active_energy_kcal", Output.opt(energy)),
                ("distance_m", Output.opt(workout.totalDistance?.doubleValue(for: .meter()))),
            ]
        case let correlation as HKCorrelation:
            let parts = correlation.objects.sorted { $0.startDate < $1.startDate }
            return [("objects", .array(parts.map { part in
                var pairs: [(String, JSONValue?)] = [
                    ("id", .string(part.uuid.uuidString)),
                    ("type", .string(HealthCatalog.entry(named: part.sampleType.identifier)?.name ?? part.sampleType.identifier)),
                ]
                pairs += fields(part, entry: HealthCatalog.entry(named: part.sampleType.identifier) ?? entry, unit: nil)
                return Output.object(pairs)
            }))]
        case let ecg as HKElectrocardiogram:
            let bpm = HKUnit.count().unitDivided(by: .minute())
            return [
                ("classification", .string(classification(ecg.classification))),
                ("symptoms_status", .number(Double(ecg.symptomsStatus.rawValue))),
                ("average_heart_rate", Output.opt(ecg.averageHeartRate?.doubleValue(for: bpm))),
                ("sampling_frequency_hz", Output.opt(ecg.samplingFrequency?.doubleValue(for: .hertz()))),
                ("voltage_measurements", .number(Double(ecg.numberOfVoltageMeasurements))),
            ]
        case let audiogram as HKAudiogramSample:
            return [("sensitivity_points", .array(audiogram.sensitivityPoints.map { point in
                Output.object([
                    ("frequency_hz", .number(point.frequency.doubleValue(for: .hertz()))),
                    ("left_ear_db_hl", Output.opt(point.leftEarSensitivity?.doubleValue(for: .decibelHearingLevel()))),
                    ("right_ear_db_hl", Output.opt(point.rightEarSensitivity?.doubleValue(for: .decibelHearingLevel()))),
                ])
            }))]
        case let series as HKHeartbeatSeriesSample:
            return [("beats", .number(Double(series.count)))]
        case let mind as HKStateOfMind:
            return [
                ("mind_kind", .number(Double(mind.kind.rawValue))),
                ("valence", .number(mind.valence)),
                ("labels", .array(mind.labels.map { .number(Double($0.rawValue)) })),
                ("associations", .array(mind.associations.map { .number(Double($0.rawValue)) })),
            ]
        default:
            return []
        }
    }

    static func classification(_ value: HKElectrocardiogram.Classification) -> String {
        switch value {
        case .notSet: "not_set"
        case .sinusRhythm: "sinus_rhythm"
        case .atrialFibrillation: "atrial_fibrillation"
        case .inconclusiveLowHeartRate: "inconclusive_low_heart_rate"
        case .inconclusiveHighHeartRate: "inconclusive_high_heart_rate"
        case .inconclusivePoorReading: "inconclusive_poor_reading"
        case .inconclusiveOther: "inconclusive_other"
        case .unrecognized: "unrecognized"
        @unknown default: "other_\(value.rawValue)"
        }
    }

    /// The app (and its version) that saved the sample.
    static func source(_ revision: HKSourceRevision) -> JSONValue {
        let os = revision.operatingSystemVersion
        return Output.object([
            ("name", .string(revision.source.name)),
            ("bundle_id", .string(revision.source.bundleIdentifier)),
            ("version", Output.opt(revision.version)),
            ("product_type", Output.opt(revision.productType)),
            ("os_version", os.majorVersion > 0 ? .string("\(os.majorVersion).\(os.minorVersion).\(os.patchVersion)") : nil),
        ])
    }

    /// The device that measured it. Its local identifier and UDI are left out: they identify the hardware, not the reading.
    static func device(_ device: HKDevice?) -> JSONValue? {
        guard let device else { return nil }
        let value = Output.object([
            ("name", Output.opt(device.name)),
            ("manufacturer", Output.opt(device.manufacturer)),
            ("model", Output.opt(device.model)),
            ("hardware_version", Output.opt(device.hardwareVersion)),
            ("firmware_version", Output.opt(device.firmwareVersion)),
            ("software_version", Output.opt(device.softwareVersion)),
        ])
        return value.object?.isEmpty == true ? nil : value
    }

    static func metadata(_ metadata: [String: Any]?) -> JSONValue? {
        guard let metadata, !metadata.isEmpty else { return nil }
        return .object(metadata.mapValues(metadataValue))
    }

    static func metadataValue(_ value: Any) -> JSONValue {
        switch value {
        case let text as String:
            return .string(String(text.prefix(metadataTextLimit)))
        case let date as Date:
            return .string(ToolDate.string(date))
        case let quantity as HKQuantity:
            return .string(quantity.description)
        case let number as NSNumber:
            return CFGetTypeID(number) == CFBooleanGetTypeID() ? .bool(number.boolValue) : .number(number.doubleValue)
        default:
            return .string(String(String(describing: value).prefix(metadataTextLimit)))
        }
    }
}

/// One sample as the on-device snapshot keeps it: its id and end for merging and trimming, and the JSON the tool returns.
nonisolated struct HealthStoredSample: Codable, Sendable, Equatable {
    var id: String
    var end: Date
    var json: JSONValue
}

/// What one watched type's snapshot holds, written while the phone is unlocked and read at any time.
nonisolated struct HealthSampleSnapshot: Codable, Sendable {
    /// How far back a snapshot keeps samples, and how many it keeps at most.
    static let keepDays = 7
    static let maximumSamples = 2000

    var name: String
    var asOf: Date
    /// HealthKit's anchor after the last read, so the next background read gets only what is new.
    var anchor: Data?
    /// In the order HealthKit added them.
    var samples: [HealthStoredSample]

    /// Applies one anchored read: deleted ids leave, added samples replace older copies of themselves and go to the end,
    /// samples that ended before `keepAfter` leave, and only the newest `maximumSamples` stay.
    static func merged(
        _ existing: [HealthStoredSample],
        added: [HealthStoredSample],
        deleted: Set<String>,
        keepAfter: Date,
        maximum: Int = maximumSamples
    ) -> [HealthStoredSample] {
        let replaced = Set(added.map(\.id)).union(deleted)
        var out = existing.filter { !replaced.contains($0.id) }
        out += added.filter { !deleted.contains($0.id) }
        out.removeAll { $0.end < keepAfter }
        if out.count > maximum { out.removeFirst(out.count - maximum) }
        return out
    }
}
#endif
