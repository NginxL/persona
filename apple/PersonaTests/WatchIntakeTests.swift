import Foundation
import SwiftUI
import Testing
import WidgetKit
import UIKit
import PersonaWire
@testable import Persona

@MainActor struct WatchIntakeTests {
    private let now = Date(timeIntervalSince1970: 1_767_355_200) // 2026-01-02 12:00 UTC
    private let credential = ExtensionSurfaceCredential(url: URL(string: "https://hub.example.com/base")!,
        token: "fixture-watch-answer", name: "Sample", answerOnly: true)

    private func stores() throws -> (URL, ExtensionSurfaceStore, WatchIntakeStore) {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        let surfaces = ExtensionSurfaceStore(root: root.appendingPathComponent("Extensions"))
        try surfaces.replaceCredentials(["": credential])
        return (root, surfaces, WatchIntakeStore(root: root.appendingPathComponent("Intake")))
    }
    private func response(_ request: URLRequest, status: Int = 200, water: Double = 750) -> (Data, HTTPURLResponse) {
        let data = Data("""
        {"water":{"today":\(water),"goal":2000,"entries":[]},"protein":{"today":32.5,"goal":60,"entries":[]},
         "days":[{"date":"2026-01-02","water":\(water),"protein":32.5}],"zone":"UTC"}
        """.utf8)
        return (data, HTTPURLResponse(url: request.url!, statusCode: status, httpVersion: nil, headerFields: nil)!)
    }

    @Test func timelineRequestsTheHealthSpaceWithTheWatchesOwnCredentialAndCachesBothAmounts() async throws {
        let (root, surfaces, cache) = try stores()
        defer { try? FileManager.default.removeItem(at: root) }
        let result = await WatchIntakeTimeline.load(group: nil, surfaces: surfaces, cache: cache, transport: { request in
            #expect(request.httpMethod == "GET" && request.httpBody == nil)
            #expect(request.url?.path == "/base/api/v1/assistant/health/intake")
            let query = URLComponents(url: request.url!, resolvingAgainstBaseURL: false)?.queryItems ?? []
            #expect(query.contains(.init(name: "space", value: "health")))
            #expect(query.contains(.init(name: "days", value: "1")))
            #expect(!query.contains { $0.name == "manager" })
            #expect(request.value(forHTTPHeaderField: "X-Persona-Manager") == nil)
            #expect(request.value(forHTTPHeaderField: "Authorization") == "Bearer fixture-watch-answer")
            return await response(request)
        }, now: now)
        #expect(result.paired && !result.cached && !result.needsHubUpdate)
        #expect(result.reading?.water.today == 750 && result.reading?.protein.today == 32.5)
        let saved = await WatchIntakeTimeline.cached(group: nil, surfaces: surfaces, cache: cache, now: now)
        #expect(saved.reading == result.reading)
        let bytes = try String(contentsOf: root.appendingPathComponent("Intake/snapshot.json"), encoding: .utf8)
        #expect(!bytes.contains(credential.token) && !bytes.contains(credential.url.absoluteString))
        #expect(!bytes.contains("entries") && !bytes.contains("note"))
    }

    @Test func networkAndMalformedRepliesKeepThePreviousCache() async throws {
        let (root, surfaces, cache) = try stores()
        defer { try? FileManager.default.removeItem(at: root) }
        _ = await WatchIntakeTimeline.load(group: nil, surfaces: surfaces, cache: cache,
            transport: { await response($0) }, now: now)
        let offline = await WatchIntakeTimeline.load(group: nil, surfaces: surfaces, cache: cache,
            transport: { _ in throw URLError(.notConnectedToInternet) }, now: now.addingTimeInterval(60))
        #expect(offline.cached && offline.reading?.water.today == 750)
        let malformed = await WatchIntakeTimeline.load(group: nil, surfaces: surfaces, cache: cache,
            transport: { request in (Data("{}".utf8), await response(request).1) }, now: now.addingTimeInterval(120))
        #expect(malformed.cached && malformed.reading == offline.reading)
    }

    @Test func oldHub403ShowsUpdateAndNeverRevokesExtensionCredentialsOrTheirCache() async throws {
        let (root, surfaces, cache) = try stores()
        defer { try? FileManager.default.removeItem(at: root) }
        let original = try #require(surfaces.read().groups[""])
        try surfaces.catalog(group: "", expected: original,
            widgets: [ExtensionWidgetPlace(extension: "sample", id: "today", title: "Today", families: ["small"], refreshMinutes: 30)], shortcuts: [])
        let state = try #require(surfaces.read().groups[""])
        try surfaces.save(ExtensionSurfaceCache(key: .init(group: "", owner: "sample", contribution: "today"),
            family: "small", locale: "en-US", timeZone: "UTC", answer: ExtensionWidgetAnswer(expiresAt: "2040-01-01T00:00:00Z")), expected: state)
        let extensionFile = root.appendingPathComponent("Extensions/snapshot.json")
        let before = try Data(contentsOf: extensionFile)
        _ = await WatchIntakeTimeline.load(group: nil, surfaces: surfaces, cache: cache,
            transport: { await response($0) }, now: now)
        let denied = await WatchIntakeTimeline.load(group: nil, surfaces: surfaces, cache: cache,
            transport: { await response($0, status: 403) }, now: now.addingTimeInterval(1))
        #expect(denied.needsHubUpdate && denied.reading == nil)
        #expect(try Data(contentsOf: extensionFile) == before)
        let offline = await WatchIntakeTimeline.load(group: nil, surfaces: surfaces, cache: cache,
            transport: { _ in throw URLError(.timedOut) }, now: now.addingTimeInterval(2))
        #expect(offline.needsHubUpdate)
        let upgraded = await WatchIntakeTimeline.load(group: nil, surfaces: surfaces, cache: cache,
            transport: { await response($0, water: 1000) }, now: now.addingTimeInterval(3))
        #expect(!upgraded.needsHubUpdate && upgraded.reading?.water.today == 1000)
        #expect(try Data(contentsOf: extensionFile) == before)
        #expect(String(localized: "Update the hub [Home Widgets]", locale: .init(identifier: "zh-Hans")) == "需要更新 hub")
    }

    @Test func aSuccessfulReloadReadsAgainEvenWithinFifteenMinutes() async throws {
        let (root, surfaces, cache) = try stores()
        defer { try? FileManager.default.removeItem(at: root) }
        _ = await WatchIntakeTimeline.load(group: nil, surfaces: surfaces, cache: cache,
            transport: { await response($0) }, now: now)
        let next = await WatchIntakeTimeline.load(group: nil, surfaces: surfaces, cache: cache,
            transport: { await response($0, water: 1000) }, now: now.addingTimeInterval(1))
        #expect(next.reading?.water.today == 1000)
        #expect(WatchIntakeKind.widgetKinds == ["persona.watch.water", "persona.watch.protein"])
        #expect(WatchIntakeKind.families == [.accessoryCircular, .accessoryRectangular, .accessoryInline])
        #expect(WatchFaceTokens.waterSymbol == Theme.Icon.water && WatchFaceTokens.proteinSymbol == Theme.Icon.protein)
        let hub = WatchIntakeHub(group: "", name: "Sample")
        #expect(!hub.id.isEmpty && hub.group.isEmpty)
        #expect(WatchIntakeConfiguration(hub: hub).hub?.group == "")
        #expect(WatchIntakeConfiguration().hub == nil)
    }

    @Test func removedRepairedAndUnselectedHubsNeverReuseAnotherPairingsCache() async throws {
        let (root, surfaces, cache) = try stores()
        defer { try? FileManager.default.removeItem(at: root) }
        _ = await WatchIntakeTimeline.load(group: nil, surfaces: surfaces, cache: cache,
            transport: { await response($0) }, now: now)
        try surfaces.replaceCredentials([:])
        let removed = await WatchIntakeTimeline.cached(group: nil, surfaces: surfaces, cache: cache, now: now)
        #expect(!removed.paired)
        try surfaces.replaceCredentials(["": credential])
        let repaired = await WatchIntakeTimeline.cached(group: nil, surfaces: surfaces, cache: cache, now: now)
        #expect(repaired.reading == nil)
        #expect(WatchIntakeTimeline.group(nil, in: .init(groups: ["b": .init(credential: credential)])) == "b")
        #expect(WatchIntakeTimeline.group(nil, in: .init(groups: ["a": .init(credential: credential), "b": .init(credential: credential)])) == nil)
        #expect(WatchIntakeTimeline.group("gone", in: surfaces.read()) == nil)
    }

    @Test func aReplyArrivingAfterUnpairDoesNotPublishOrWrite() async throws {
        let (root, surfaces, cache) = try stores()
        defer { try? FileManager.default.removeItem(at: root) }
        let result = await WatchIntakeTimeline.load(group: nil, surfaces: surfaces, cache: cache, transport: { request in
            try surfaces.replaceCredentials([:])
            return await response(request)
        }, now: now)
        #expect(!result.paired && result.reading == nil)
        #expect(!FileManager.default.fileExists(atPath: root.appendingPathComponent("Intake/snapshot.json").path))
    }

    @Test func olderRequestsCannotOverwriteANewerSuccessfulReadOrUpdateState() async throws {
        let (root, surfaces, cache) = try stores()
        defer { try? FileManager.default.removeItem(at: root) }
        let generation = try #require(surfaces.read().groups[""]?.generation)
        let reading = WatchIntakeReading(water: .init(today: 1250, goal: 2000), protein: .init(today: 30, goal: 60),
                                        nextDay: now.addingTimeInterval(3600), zone: "UTC")
        try cache.storeIntake(.init(generation: generation, requestedAt: now.addingTimeInterval(10), reading: reading),
                       group: "", current: surfaces.read().groups)
        let late = await WatchIntakeTimeline.load(group: nil, surfaces: surfaces, cache: cache,
            transport: { await response($0, status: 403) }, now: now)
        #expect(!late.needsHubUpdate && late.reading?.water.today == 1250)
    }

    @Test func timelineSchedulesFifteenMinutesAndClearsAtTheHubsMidnightIncludingDST() throws {
        let data = Data(#"{"water":{"today":750,"goal":2000},"protein":{"today":32.5,"goal":60},"days":[{"date":"2026-03-08"}],"zone":"America/Los_Angeles"}"#.utf8)
        let reading = try WatchIntakeReading(JSONDecoder().decode(WatchIntakeReading.Answer.self, from: data), at: now)
        #expect(reading.nextDay == ISO8601DateFormatter().date(from: "2026-03-09T07:00:00Z"))
        let entry = WatchIntakeEntry(date: reading.nextDay.addingTimeInterval(-60), reading: reading, paired: true)
        let timeline = WatchIntakeTimeline.timeline(entry)
        #expect(timeline.entries.map(\.date) == [entry.date, reading.nextDay])
        #expect(timeline.entries[1].reading?.portion(.water, at: reading.nextDay).today == 0)
        #expect(timeline.entries[1].reading?.portion(.protein, at: reading.nextDay).goal == 60)
        #expect(timeline.policy == .after(reading.nextDay))
        let normal = WatchIntakeTimeline.timeline(.init(date: reading.nextDay.addingTimeInterval(-3600), reading: reading, paired: true))
        #expect(normal.policy == .after(normal.entries[0].date.addingTimeInterval(900)))
        #expect(WatchIntakeReading.Portion(today: 3000, goal: 2000).fraction == 1)
        #expect(WatchIntakeReading.Portion(today: 0, goal: 0).fraction == 0)
    }

    @Test func threeFamiliesRenderForBothIntakesInLightAndDark() throws {
        let reading = WatchIntakeReading(water: .init(today: 1250, goal: 2000), protein: .init(today: 32.5, goal: 60),
                                        nextDay: now.addingTimeInterval(3600), zone: "UTC")
        let entry = WatchIntakeEntry(date: now, reading: reading, paired: true)
        let output = ProcessInfo.processInfo.environment["PERSONA_WATCH_INTAKE_SHOTS"].map { URL(fileURLWithPath: $0) }
        for kind in WatchIntakeKind.allCases {
            for scheme in [ColorScheme.light, .dark] {
                let view = VStack(alignment: .leading, spacing: 16) {
                    Text(verbatim: "\(kind.rawValue) · \(scheme == .dark ? "dark" : "light")").font(.headline)
                    WatchIntakeComplicationView(entry: entry, kind: kind, family: .accessoryCircular)
                        .frame(width: 60, height: 60)
                    WatchIntakeComplicationView(entry: entry, kind: kind, family: .accessoryRectangular)
                        .frame(width: 172, height: 76)
                    WatchIntakeComplicationView(entry: entry, kind: kind, family: .accessoryInline)
                        .frame(width: 210, height: 24, alignment: .leading)
                }
                .padding(20).frame(width: 280).background(scheme == .dark ? Color.black : .white)
                .environment(\.colorScheme, scheme).environment(\.locale, Locale(identifier: "zh-Hans"))
                let image = try hostedImage(view, width: 280, scheme: scheme)
                #expect(image.size.width == 280 && image.size.height > 200)
                if let output {
                    try FileManager.default.createDirectory(at: output, withIntermediateDirectories: true)
                    try #require(image.pngData()).write(to: output.appendingPathComponent("\(kind.rawValue)-\(scheme == .dark ? "dark" : "light").png"))
                }
            }
        }
    }

    @Test func anUnresolvedHubLocalZoneNeverClearsTodaysAmountAtAGuessedUTCMidnight() throws {
        let data = Data(#"{"water":{"today":750,"goal":2000},"protein":{"today":32.5,"goal":60},"days":[{"date":"2026-01-02"}],"zone":"Local"}"#.utf8)
        // It can still be Jan 2 at the hub while the watch and UTC already say Jan 3.
        let watchDate = try #require(ISO8601DateFormatter().date(from: "2026-01-03T04:00:00Z"))
        let reading = try WatchIntakeReading(JSONDecoder().decode(WatchIntakeReading.Answer.self, from: data), at: watchDate)
        #expect(reading.portion(.water, at: watchDate).today == 750)
        let timeline = WatchIntakeTimeline.timeline(.init(date: watchDate, reading: reading, paired: true))
        #expect(timeline.entries.count == 2 && timeline.entries[1].cached)
        #expect(timeline.policy == .after(watchDate.addingTimeInterval(900)))
    }

    @Test func updateOfflineAndUnpairedStatesRenderWithoutInventedAmounts() throws {
        let reading = WatchIntakeReading(water: .init(today: 1250, goal: 2000), protein: .init(today: 32.5, goal: 60),
                                        nextDay: now.addingTimeInterval(3600), zone: "UTC")
        let entries = [WatchIntakeEntry(date: now, paired: true, needsHubUpdate: true),
                       .init(date: now), .init(date: now, paired: true),
                       .init(date: now, reading: reading, paired: true, cached: true)]
        let output = ProcessInfo.processInfo.environment["PERSONA_WATCH_INTAKE_SHOTS"].map { URL(fileURLWithPath: $0) }
        for scheme in [ColorScheme.light, .dark] {
            let view = VStack(alignment: .leading, spacing: 16) {
                ForEach(entries.indices, id: \.self) { index in
                    HStack(spacing: 12) {
                        WatchIntakeComplicationView(entry: entries[index], kind: .water, family: .accessoryCircular)
                            .frame(width: 60, height: 60)
                        WatchIntakeComplicationView(entry: entries[index], kind: .water, family: .accessoryRectangular)
                            .frame(width: 172, height: 76)
                    }
                }
            }
            .padding(20).frame(width: 296).background(scheme == .dark ? Color.black : .white)
            .environment(\.colorScheme, scheme).environment(\.locale, Locale(identifier: "zh-Hans"))
            let data = try #require(hostedImage(view, width: 296, scheme: scheme).pngData())
            #expect(data.count > 1000)
            if let output { try data.write(to: output.appendingPathComponent("states-\(scheme == .dark ? "dark" : "light").png")) }
        }
    }

    /// Native gauge subviews need a hosting window; ImageRenderer cannot flatten their UIKit adaptor.
    private func hostedImage<V: View>(_ content: V, width: CGFloat, scheme: ColorScheme) throws -> UIImage {
        let controller = UIHostingController(rootView: content)
        controller.safeAreaRegions = []
        controller.overrideUserInterfaceStyle = scheme == .dark ? .dark : .light
        let size = controller.sizeThatFits(in: CGSize(width: width, height: 1000))
        let scene = try #require(UIApplication.shared.connectedScenes.first as? UIWindowScene)
        let window = UIWindow(windowScene: scene)
        window.frame = CGRect(origin: .zero, size: size)
        window.rootViewController = controller
        window.isHidden = false
        defer { window.isHidden = true }
        controller.view.frame = window.bounds
        controller.view.layoutIfNeeded()
        let format = UIGraphicsImageRendererFormat(); format.scale = 2
        return UIGraphicsImageRenderer(size: size, format: format).image { _ in
            controller.view.drawHierarchy(in: controller.view.bounds, afterScreenUpdates: true)
        }
    }
}
