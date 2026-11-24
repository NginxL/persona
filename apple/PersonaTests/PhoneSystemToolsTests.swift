#if os(iOS) && canImport(AlarmKit)
import ActivityKit
import AlarmKit
import SwiftUI
import UIKit
#endif
import Foundation
import MapKit
import Testing
@testable import Persona
import PersonaWire

/// The iPhone system integrations (#2276–#2286): what each tool reads from its arguments, what its approval card says,
/// and the rules that keep it from acting on a guess (an ambiguous occurrence, an overwritten field, an unallowed shortcut).
@MainActor
@Suite(.serialized)
struct PhoneSystemToolsTests {
    private func json(_ text: String) throws -> JSONValue {
        try JSONDecoder().decode(JSONValue.self, from: Data(text.utf8))
    }

    private func text(of content: [ToolContent]) -> String {
        content.compactMap(\.text).joined(separator: "\n")
    }

    // MARK: - Sources

    /// They act outside Persona: nothing is offered until the user turns a source on.
    @Test func theIPhoneIntegrationsStartOff() {
        for source in PhoneDataSource.iPhoneOnly {
            let key = "phoneSource.\(source.rawValue)"
            let saved = UserDefaults.standard.object(forKey: key)
            defer { UserDefaults.standard.set(saved, forKey: key) }
            UserDefaults.standard.removeObject(forKey: key)
            #expect(!source.isEnabled, "\(source.rawValue) must be opt-in")
        }
    }

    @Test func homeIsOfferedOnlyInABuildWithHomeKit() {
        #expect(PhoneDataSource.onThisDevice.contains(.home) == HomeAccess.isBuilt)
        #expect(PhoneDataSource.onThisDevice.contains(.alarms) == AlarmsAccess.isSupported) // AlarmKit: iOS 26 (#6730)
    }

    // MARK: - Calendar (#2276)

    @Test func aNewStartKeepsTheEventsLength() throws {
        let start = Date(timeIntervalSince1970: 1_800_000_000)
        let change = try CalendarTool.EventChange.parse(json(#"{"id":"e","start":"2027-01-15T15:00:00+08:00"}"#),
                                                        currentStart: start, currentEnd: start.addingTimeInterval(1800))
        let newStart = try #require(change.start)
        #expect(change.end == newStart.addingTimeInterval(1800))
    }

    @Test func aChangeWithNothingOrAnEndBeforeTheStartIsRefused() {
        let start = Date(timeIntervalSince1970: 1_800_000_000)
        #expect(throws: ToolFailure.self) {
            try CalendarTool.EventChange.parse(json(#"{"id":"e"}"#), currentStart: start, currentEnd: start)
        }
        #expect(throws: ToolFailure.self) {
            try CalendarTool.EventChange.parse(json(#"{"id":"e","start":"2027-01-15T15:00:00+08:00","end":"2027-01-15T14:00:00+08:00"}"#),
                                               currentStart: start, currentEnd: start)
        }
    }

    @Test func theSpanIsThisUnlessFutureIsAsked() throws {
        #expect(try CalendarTool.parseSpan(json(#"{}"#)) == .thisEvent)
        #expect(try CalendarTool.parseSpan(json(#"{"span":"future"}"#)) == .futureEvents)
        #expect(throws: ToolFailure.self) { try CalendarTool.parseSpan(json(#"{"span":"all"}"#)) }
    }

    @Test func theChangeCardShowsBeforeAndAfterAndTheSpan() throws {
        let start = Date(timeIntervalSince1970: 1_800_000_000)
        let target = CalendarTool.EventTarget(title: "Dentist", start: start, end: start.addingTimeInterval(3600), allDay: false,
                                              calendar: "Home", location: nil, recurring: true)
        let change = try CalendarTool.EventChange.parse(json(#"{"id":"e","start":"2027-01-15T15:00:00+08:00","title":"Dentist (moved)"}"#),
                                                        currentStart: target.start, currentEnd: target.end)
        let card = CalendarTool.changeSummary(target, change: change, future: false)
        #expect(card.contains("Dentist") && card.contains("Dentist (moved)") && card.contains("→") && card.contains("Home"))
        #expect(card.contains(String(localized: "Only this occurrence")))
        let future = CalendarTool.changeSummary(target, change: change, future: true)
        #expect(future.contains(String(localized: "This and all future occurrences")))
        let delete = CalendarTool.deleteSummary(target, future: false)
        #expect(delete.contains("Dentist") && delete.contains(String(localized: "Only this occurrence")))
    }

    /// A made-up id matches nothing: the card says so and the tool then fails without deleting.
    @Test func theDeleteCardForAnUnknownEventSaysSo() {
        let tool = PhoneToolRegistry.all.first { $0.spec.name == "phone_calendar_delete_event" }
        let card = tool?.confirmation?(.object(["id": .string("no-such-event")]))
        #expect(card?.contains("no-such-event") == true)
    }

    @Test func theNewEventCardShowsEverythingItWillWrite() throws {
        guard let start = try ToolDate.parse("2027-01-15T15:00:00+08:00"),
              let end = try ToolDate.parse("2027-01-15T16:00:00+08:00") else {
            Issue.record("valid test dates required")
            return
        }
        let notes = "bring gift " + String(repeating: "n", count: 240)
        let tool = PhoneToolRegistry.all.first { $0.spec.name == "phone_calendar_create_event" }
        let card = try #require(tool?.confirmation?(.object([
            "title": .string("Dinner"), "start": .string("2027-01-15T15:00:00+08:00"),
            "end": .string("2027-01-15T16:00:00+08:00"), "calendar": .string("Family"), "notes": .string(notes),
        ])))
        #expect(card.contains("Family"))
        #expect(card.contains(String(notes.prefix(200))))
        #expect(!card.contains(String(notes.prefix(201))))
        let timeZone = try #require(TimeZone(secondsFromGMT: 8 * 3600))
        #expect(card.contains(CardTime.text(start, timeZone: timeZone)))
        #expect(card.contains(CardTime.text(end, timeZone: timeZone)))
        #expect(!card.contains("2027-01-15T15:00:00+08:00"))
    }

    // MARK: - Reminders (#2277)

    @Test func theNewReminderCardShowsEverythingItWillWrite() throws {
        guard let due = try ToolDate.parse("2027-01-15T18:00:00+08:00") else {
            Issue.record("valid test due date required")
            return
        }
        let tool = PhoneToolRegistry.all.first { $0.spec.name == "phone_reminders_create" }
        let card = try #require(tool?.confirmation?(.object([
            "title": .string("Expenses"), "due": .string("2027-01-15T18:00:00+08:00"),
            "notes": .string("call first"), "list": .string("Personal"), "priority": .number(1),
        ])))
        #expect(card.contains("Personal") && card.contains("call first"))
        #expect(card.contains(RemindersTool.priorityName(1)))
        #expect(card.contains(CardTime.text(due)))
        #expect(!card.contains("2027-01-15T18:00:00+08:00"))
    }

    @Test func theReminderCardNamesNotesPriorityAndList() throws {
        let target = RemindersTool.CompletionTarget(title: "Expenses", list: "Work", due: nil)
        let card = RemindersTool.changeSummary(id: "r1", target: target,
                                               args: try json(#"{"id":"r1","notes":"call first","priority":1,"list":"Personal"}"#))
        #expect(card.contains("call first") && card.contains("Personal"))
        #expect(card.contains(RemindersTool.priorityName(1)))
        let cleared = RemindersTool.changeSummary(id: "r1", target: target, args: try json(#"{"id":"r1","notes":null}"#))
        #expect(cleared.contains(String(localized: "Notes removed")))
    }

    // MARK: - Contacts (#2286)

    @Test func aNewContactNeedsAName() throws {
        #expect(throws: ToolFailure.self) { try ContactsTool.ContactEdit.plan(current: nil, args: json(#"{"job_title":"Chef"}"#)) }
        let edit = try ContactsTool.ContactEdit.plan(current: nil, args: json(#"{"given_name":"Sam","phones":[{"value":"555-0100","label":"mobile"}]}"#))
        #expect(edit.fields.map(\.key) == ["given_name"])
        #expect(edit.phones == [.init(value: "555-0100", label: "mobile")])
        #expect(ContactsTool.createSummary(edit).contains("Sam"))
    }

    @Test func anExistingFieldIsNotOverwrittenUnlessAsked() throws {
        let current = ContactsTool.ContactFields(givenName: "Sam", company: "Acme", phones: ["555 0100"])
        #expect(throws: ToolFailure.self) {
            try ContactsTool.ContactEdit.plan(current: current, args: json(#"{"id":"c","company":"Initech"}"#))
        }
        let replaced = try ContactsTool.ContactEdit.plan(current: current, args: json(#"{"id":"c","company":"Initech","replace":true}"#))
        #expect(replaced.fields == [.init(key: "company", before: "Acme", after: "Initech")])
        // An empty field is filled without replace.
        let filled = try ContactsTool.ContactEdit.plan(current: current, args: json(#"{"id":"c","job_title":"Chef"}"#))
        #expect(filled.fields == [.init(key: "job_title", before: "", after: "Chef")])
    }

    @Test func phonesAndEmailsAreAddedOnceAndReplacedOnlyWhenAsked() throws {
        let current = ContactsTool.ContactFields(givenName: "Sam", phones: ["555 0100"], emails: ["sam@example.com"])
        let added = try ContactsTool.ContactEdit.plan(current: current,
            args: json(#"{"id":"c","phones":["555-0100","555-0199"],"emails":["SAM@example.com","s@example.org"]}"#))
        #expect(added.phones.map(\.value) == ["555-0199"])
        #expect(added.emails.map(\.value) == ["s@example.org"])
        #expect(!added.replacePhones && !added.replaceEmails)
        #expect(throws: ToolFailure.self) {
            try ContactsTool.ContactEdit.plan(current: current, args: json(#"{"id":"c","phones":["555-0100"]}"#))
        }
        let replaced = try ContactsTool.ContactEdit.plan(current: current, args: json(#"{"id":"c","phones":["555-0199"],"replace":true}"#))
        #expect(replaced.replacePhones && replaced.phones.map(\.value) == ["555-0199"])
        #expect(ContactsTool.updateSummary(current, replaced).contains("555-0199"))
        #expect(throws: ToolFailure.self) {
            try ContactsTool.ContactEdit.plan(current: current, args: json(#"{"id":"c","emails":["not an email"]}"#))
        }
    }

    // MARK: - Calls (#2280)

    @Test func aCallTakesOneNumberAndNothingElse() throws {
        let call = try CallTool.CallRequest.parse(json(#"{"number":"+1 (555) 010-0199","kind":"phone","name":"Sam"}"#))
        #expect(call.address == "+15550100199")
        #expect(call.url?.absoluteString == "tel:+15550100199")
        #expect(CallTool.summary(call).contains("Sam") && CallTool.summary(call).contains("+15550100199"))
        #expect(throws: ToolFailure.self) { try CallTool.CallRequest.parse(json(#"{"number":"555-0100 or 555-0199"}"#)) }
        #expect(throws: ToolFailure.self) { try CallTool.CallRequest.parse(json(#"{"number":"12"}"#)) }
        let facetime = try CallTool.CallRequest.parse(json(#"{"number":"me@example.com","kind":"facetime_audio"}"#))
        #expect(facetime.url?.absoluteString == "facetime-audio:me@example.com")
    }

    // MARK: - Messages (#2279)

    @Test func aDraftNeedsRecipientsAndText() throws {
        let draft = try MessagesTool.Draft.parse(json(#"{"recipients":[" 555-0100 ","me@example.com"],"body":"Home at 7"}"#))
        #expect(draft == .init(recipients: ["555-0100", "me@example.com"], body: "Home at 7"))
        #expect(throws: ToolFailure.self) { try MessagesTool.Draft.parse(json(#"{"recipients":[],"body":"x"}"#)) }
        #expect(throws: ToolFailure.self) { try MessagesTool.Draft.parse(json(#"{"recipients":["Mom"],"body":"x"}"#)) }
        #expect(throws: ToolFailure.self) { try MessagesTool.Draft.parse(json(#"{"recipients":["555-0100"],"body":"  "}"#)) }
    }

    /// The compose sheet is the user's own Send: no extra card, and the hub waits for it like a confirmed write.
    @Test func composingAsksNoCard() {
        let tool = PhoneToolRegistry.all.first { $0.spec.name == "phone_messages_compose" }
        #expect(tool != nil && tool?.confirmation == nil)
    }

    // MARK: - Maps (#2281)

    @Test func aDestinationNeedsCoordinatesAndAKnownMode() throws {
        let walk = try MapsTool.Destination.parse(json(#"{"latitude":31.2,"longitude":121.4,"name":"Cafe","mode":"walking"}"#))
        #expect(walk.mode == .walking && walk.name == "Cafe")
        #expect(try MapsTool.Destination.parse(json(#"{"latitude":1,"longitude":2}"#)).mode == .driving)
        #expect(throws: ToolFailure.self) { try MapsTool.Destination.parse(json(#"{"latitude":91,"longitude":2}"#)) }
        #expect(throws: ToolFailure.self) { try MapsTool.Destination.parse(json(#"{"latitude":1,"longitude":2,"mode":"flying"}"#)) }
    }

    /// A route starts at the phone unless both start coordinates are given (#5544); each mode asks Maps for its own kind
    /// of route.
    @Test func aRouteStartsAtThePhoneOrAtBothCoordinatesGiven() throws {
        #expect(try MapsTool.routeStart(json(#"{"latitude":1,"longitude":2}"#)) == nil)
        let given = try #require(try MapsTool.routeStart(json(#"{"from_latitude":31.2,"from_longitude":121.4}"#)))
        #expect(given.latitude == 31.2 && given.longitude == 121.4)
        #expect(throws: ToolFailure.self) { try MapsTool.routeStart(json(#"{"from_latitude":31.2}"#)) }
        #expect(throws: ToolFailure.self) { try MapsTool.routeStart(json(#"{"from_latitude":31.2,"from_longitude":200}"#)) }
        let modes = try ["driving", "walking", "transit"].map {
            try MapsTool.Destination.parse(json(#"{"latitude":1,"longitude":2,"mode":"\#($0)"}"#)).transportType
        }
        #expect(modes == [.automobile, .walking, .transit])
        let tool = PhoneToolRegistry.all.first { $0.spec.name == "phone_maps_route" }
        #expect(tool != nil && tool?.confirmation == nil) // it opens nothing, so it asks nobody
    }

    /// A route's line is short enough for the model and the card: its ends stay, a straight stretch is two points, and a
    /// winding one keeps at most `RouteLine.most`.
    @Test func aRouteLineKeepsItsEndsAndStaysShort() {
        func point(_ latitude: Double, _ longitude: Double) -> CLLocationCoordinate2D {
            CLLocationCoordinate2D(latitude: latitude, longitude: longitude)
        }
        let straight = (0..<500).map { point(31.2 + Double($0) * 0.00001, 121.4 + Double($0) * 0.00002) }
        let line = RouteLine.simplified(straight)
        #expect(line.count == 2 && line.first?.latitude == straight.first?.latitude && line.last?.latitude == straight.last?.latitude)
        let winding = (0..<3000).map { i -> CLLocationCoordinate2D in
            let t = Double(i) / 3000
            return point(31.2 + t * 0.05, 121.4 + 0.01 * sin(t * 40))
        }
        let kept = RouteLine.simplified(winding)
        #expect(kept.count > 2 && kept.count <= RouteLine.most)
        #expect(kept.first?.longitude == winding.first?.longitude && kept.last?.latitude == winding.last?.latitude)
        #expect(RouteLine.simplified(Array(winding.prefix(10))).count == 10)
        #expect(RouteLine.rounded(31.2304449) == 31.23044)
    }

    // MARK: - Shortcuts (#2283)

    @Test func theRunLinkComesBackWithItsToken() throws {
        let url = try #require(ShortcutsTool.Link.run(name: "Coming Home & Lights", input: "a=b?", token: "T1"))
        let parts = try #require(URLComponents(url: url, resolvingAgainstBaseURL: false))
        #expect(url.scheme == "shortcuts" && url.host == "x-callback-url" && url.path == "/run-shortcut")
        let items = Dictionary(uniqueKeysWithValues: (parts.queryItems ?? []).map { ($0.name, $0.value ?? "") })
        #expect(items["name"] == "Coming Home & Lights")
        #expect(items["text"] == "a=b?")
        #expect(items["x-success"] == "persona://shortcut-callback/success?token=T1")

        let back = try #require(URL(string: "persona://shortcut-callback/success?token=T1&result=done"))
        let parsed = try #require(ShortcutsTool.Link.parse(back))
        #expect(parsed.token == "T1" && parsed.outcome == "success" && parsed.text == "done")
        #expect(ShortcutsTool.Link.parse(try #require(URL(string: "persona://pair?x=1"))) == nil)
    }

    @Test func onlyAnAllowedShortcutRuns() async {
        let source = PhoneDataSource.shortcuts
        let enabled = source.isEnabled
        let saved = ShortcutsTool.Allowlist.all
        defer { source.isEnabled = enabled; ShortcutsTool.Allowlist.all = saved }
        source.isEnabled = true
        ShortcutsTool.Allowlist.all = [.init(name: "Coming Home", purpose: nil)]
        #expect(ShortcutsTool.Allowlist.find("coming home", in: ShortcutsTool.Allowlist.all)?.name == "Coming Home")

        let result = await PhoneToolRegistry.call(name: "phone_shortcuts_run", args: .object(["name": .string("Delete Everything")])) { _ in true }
        #expect(result.isError)
        #expect(text(of: result.content).contains("not a shortcut the user allowed"))
    }

    // MARK: - Alarms (#2278)

    @Test func anAlarmTimeIsATimeOfDayOrAFutureMoment() throws {
        let morning = try AlarmsTool.AlarmRequest.parse(json(#"{"time":"06:40","repeat":["Mon","fri","mon"],"label":"Wake up"}"#))
        #expect(morning.when == .time(hour: 6, minute: 40, weekdays: ["mon", "fri"]))
        #expect(AlarmsTool.alarmSummary(morning).contains("06:40") && AlarmsTool.alarmSummary(morning).contains("Wake up"))
        #expect(throws: ToolFailure.self) { try AlarmsTool.AlarmRequest.parse(json(#"{"time":"25:00"}"#)) }
        #expect(throws: ToolFailure.self) { try AlarmsTool.AlarmRequest.parse(json(#"{"time":"06:40","repeat":["someday"]}"#)) }
        #expect(throws: ToolFailure.self) { try AlarmsTool.AlarmRequest.parse(json(#"{"time":"2020-01-01T06:40:00+08:00"}"#)) }
        let at = try AlarmsTool.AlarmRequest.parse(json(#"{"time":"2099-01-01T06:40:00+08:00"}"#))
        if case .at = at.when {} else { Issue.record("an ISO time is one moment") }
    }

    @Test func aTimerLastsASecondToADay() throws {
        #expect(try AlarmsTool.timerSeconds(json(#"{"seconds":1500}"#)) == 1500)
        #expect(throws: ToolFailure.self) { try AlarmsTool.timerSeconds(json(#"{"seconds":0}"#)) }
        #expect(throws: ToolFailure.self) { try AlarmsTool.timerSeconds(json(#"{"seconds":86401}"#)) }
        #expect(AlarmsTool.timerSummary(seconds: 1500, label: "Focus").contains("Focus"))
    }

    @Test func aPausedTimerShowsTheTimeLeftAsTheClockAppDoes() {
        #expect(AlarmActivityTime.paused(total: 1500, elapsed: 1.2) == "24:58")
        #expect(AlarmActivityTime.paused(total: 3 * 3600, elapsed: 3600) == "2:00:00")
        #expect(AlarmActivityTime.paused(total: 60, elapsed: 90) == "0:00")
    }

    @Test func theCountdownNeverStartsAfterItRings() {
        let fire = Date(timeIntervalSince1970: 1_000)
        #expect(AlarmActivityTime.countdown(start: Date(timeIntervalSince1970: 400), fire: fire).lowerBound == Date(timeIntervalSince1970: 400))
        #expect(AlarmActivityTime.countdown(start: Date(timeIntervalSince1970: 2_000), fire: fire) == fire...fire)
    }

    #if os(iOS) && canImport(AlarmKit)
    /// The delivered fields of ActivityContent: the wrapper itself is not Codable, so the test carries both unchanged
    /// values through one Codable transport rather than silently reducing the assertion to ContentState alone.
    @available(iOS 26.0, *)
    private struct TimerActivityTransport: Codable {
        var state: AlarmPresentationState
        var staleDate: Date?

        init(_ content: ActivityContent<AlarmPresentationState>) {
            state = content.state
            staleDate = content.staleDate
        }

        var content: ActivityContent<AlarmPresentationState> {
            .init(state: state, staleDate: staleDate)
        }
    }

    private struct TintTransport: Codable {
        let tintColor: String
    }

    /// Compare the original transport archive with the archive inside the decoded Color. Encoding the decoded object
    /// again can normalize a malformed archive and make a tint assertion pass without testing the transport.
    @available(iOS 26.0, *)
    private func requirePreservedTint<Metadata: AlarmMetadata>(
        _ original: AlarmAttributes<Metadata>, _ decoded: AlarmAttributes<Metadata>, context: String
    ) throws {
        let originalTransport = try #require(try? JSONDecoder().decode(TintTransport.self, from: JSONEncoder().encode(original)),
                                             "\(context): original transport")
        let originalTint = originalTransport.tintColor
        let decodedJSON = try #require(JSONSerialization.jsonObject(with: JSONEncoder().encode(decoded)) as? [String: Any])
        let decodedTint = try #require(decodedJSON["tintColor"] as? String, "\(context): decoded tint archive")
        #expect(originalTint == decodedTint, "\(context): tint archive must survive encoding and decoding")
        let originalData = try #require(Data(base64Encoded: originalTint), "\(context): original tint base64")
        let decodedData = try #require(Data(base64Encoded: decodedTint), "\(context): decoded tint base64")
        let originalColorValue = try NSKeyedUnarchiver.unarchivedObject(ofClass: UIColor.self, from: originalData)
        let originalColor = try #require(originalColorValue)
        let decodedColorValue = try NSKeyedUnarchiver.unarchivedObject(ofClass: UIColor.self, from: decodedData)
        let decodedColor = try #require(decodedColorValue)
        var red: CGFloat = 0, green: CGFloat = 0, blue: CGFloat = 0, alpha: CGFloat = 0
        var decodedRed: CGFloat = 0, decodedGreen: CGFloat = 0, decodedBlue: CGFloat = 0, decodedAlpha: CGFloat = 0
        #expect(originalColor.getRed(&red, green: &green, blue: &blue, alpha: &alpha))
        #expect(decodedColor.getRed(&decodedRed, green: &decodedGreen, blue: &decodedBlue, alpha: &decodedAlpha))
        #expect((red, green, blue, alpha) == (decodedRed, decodedGreen, decodedBlue, decodedAlpha),
                "\(context): tint RGBA must survive encoding and decoding")
    }

    /// iOS 26.0 requires the Alert Stop field. From iOS 26.1 Apple deprecates it and the newer AlarmKit encoder omits
    /// it; Xcode 27.2 / iOS 27.0 demonstrates that omission. No other version may substitute Pause/Resume for Stop.
    @available(iOS 26.0, *)
    private func requireEncodedStop(
        alert: [String: Any], tintArchive: String, expectedText: LocalizedStringResource, state: String
    ) throws {
        #if compiler(>=6.4)
        if ProcessInfo.processInfo.operatingSystemVersion.majorVersion > 26 ||
            (ProcessInfo.processInfo.operatingSystemVersion.majorVersion == 26 &&
             ProcessInfo.processInfo.operatingSystemVersion.minorVersion >= 1) {
            if alert["stopButton"] == nil {
                return
            }
        }
        #endif
        let stop = try #require(alert["stopButton"] as? [String: Any],
                                "\(state): iOS 26.0 requires the Alert Stop configuration")
        #expect(stop["systemImageName"] as? String == Theme.Icon.interrupt, "\(state): actual Stop symbol")
        #expect(stop["textColor"] as? String == tintArchive, "\(state): actual Stop tint archive")
        let text = try #require(stop["text"] as? [String: Any], "\(state): actual Stop text")
        let defaultValue = try #require(text["defaultValue"] as? [String: Any], "\(state): Stop default value")
        let arguments = try #require(defaultValue["arguments"] as? [Any], "\(state): Stop arguments")
        #expect(arguments.isEmpty)
        #expect(text["key"] as? String == expectedText.key, "\(state): actual Stop resource key")
        #expect(defaultValue["key"] as? String == String(localized: expectedText),
                "\(state): actual Stop visible text")
    }

    /// An app-requested ActivityKit activity takes at most 4 KB of attributes. The sample's resources carry no app
    /// bundle lookup data, while the title, action marker, icon and tint stay complete for the custom widget.
    @Test func theTimerSampleFitsInsideActivityKitsLimit() throws {
        guard #available(iOS 26.0, *) else { return }
        let encoder = JSONEncoder()
        let decoder = JSONDecoder()
        let runningAttributes = AlarmsTool.sampleAttributes(label: "Focus", paused: false)
        let pausedAttributes = AlarmsTool.sampleAttributes(label: "Focus", paused: true)
        let resource = AlarmsTool.sampleTitle(key: "sample.unused.key", defaultValue: "Different visible label")
        let resourceData = try encoder.encode(resource)
        let decodedResource = try decoder.decode(LocalizedStringResource.self, from: resourceData)
        #expect(decodedResource.key == "sample.unused.key")
        #expect(decodedResource.defaultValue == String.LocalizationValue("Different visible label"))
        #expect(String(localized: decodedResource) == "Different visible label")
        print("timer resource sentinel: key=\(decodedResource.key), defaultValue=\(String(localized: decodedResource))")
        #expect(decodedResource.key != String(localized: decodedResource))
        var decodedRunning = runningAttributes
        var decodedPaused = pausedAttributes
        for sample in [runningAttributes, pausedAttributes] {
            let data = try encoder.encode(sample)
            let state = sample.presentation.countdown == nil ? "paused" : "running"
            #expect(data.count < 4096, "\(state) sample attributes bytes: \(data.count)")
            print("timer sample \(state): attributes=\(data.count) bytes")
            let decoded = try decoder.decode(AlarmAttributes<PersonaAlarmData>.self, from: data)
            if state == "running" {
                decodedRunning = decoded
            } else {
                decodedPaused = decoded
            }
            #expect(decoded.metadata?.label == "Focus")
            #expect(decoded.metadata?.isSample == true)
            #expect(decoded.presentation.alert.title.key == "sample.title")
            #expect(decoded.presentation.alert.title.defaultValue == String.LocalizationValue("Focus"))
            #expect(String(localized: decoded.presentation.alert.title) == "Focus")
            print("timer sample \(state): title=\(decoded.presentation.alert.title.key), defaultValue=\(String(localized: decoded.presentation.alert.title))")
            let encoded = try #require(JSONSerialization.jsonObject(with: data) as? [String: Any])
            let presentation = try #require(encoded["presentation"] as? [String: Any])
            let alert = try #require(presentation["alert"] as? [String: Any])
            let archived = try #require(encoded["tintColor"] as? String)
            try requireEncodedStop(alert: alert, tintArchive: archived, expectedText: decoded.presentation.alert.title,
                                   state: "sample \(state)")
            try requirePreservedTint(sample, decoded, context: "sample \(state)")
        }
        // The widget's Pause gate reads decoded countdown.pauseButton, not the original builder. Neither state may
        // acquire the opposite presentation during the round trip; Resume is required in the decoded paused state.
        let runningPause = try #require(decodedRunning.presentation.countdown?.pauseButton,
                                        "decoded running countdown.pauseButton must remain available")
        #expect(runningPause.systemImageName == Theme.Icon.pause)
        #expect(decodedRunning.presentation.paused == nil)
        #expect(decodedPaused.presentation.countdown == nil)
        #expect(decodedPaused.presentation.paused?.resumeButton.systemImageName == Theme.Icon.run,
                "decoded paused paused.resumeButton must remain available")

        // These are the exact ActivityContent values requested with the sample, including the wrapper's staleDate.
        let now = Date(timeIntervalSince1970: 1_800_000_000)
        let running = AlarmsTool.sampleContent(paused: false, now: now)
        let paused = AlarmsTool.sampleContent(paused: true, now: now)
        let runningTransport = TimerActivityTransport(running)
        let pausedTransport = TimerActivityTransport(paused)
        let runningData = try encoder.encode(runningTransport)
        let pausedData = try encoder.encode(pausedTransport)
        #expect(runningData.count < 4096, "running ActivityContent bytes: \(runningData.count)")
        #expect(pausedData.count < 4096, "paused ActivityContent bytes: \(pausedData.count)")
        let runningContent = try decoder.decode(TimerActivityTransport.self, from: runningData).content
        let pausedContent = try decoder.decode(TimerActivityTransport.self, from: pausedData).content
        #expect(runningContent.staleDate == nil && pausedContent.staleDate == nil)
        #expect(runningContent.state.alarmID == running.state.alarmID)
        #expect(pausedContent.state.alarmID == paused.state.alarmID)
        #expect(runningContent.state.alarmID != pausedContent.state.alarmID)

        // AlarmButtons's gates: the decoded sample has Pause or Resume; each button carries this sample's ID, which
        // AlarmIntentRouter intercepts in DEBUG so taps act on the sample activity instead of AlarmManager (#13269).
        if case .countdown = runningContent.state.mode {
            #expect(decodedRunning.presentation.countdown?.pauseButton != nil)
        } else {
            Issue.record("running must expose Pause")
        }
        if case .paused = pausedContent.state.mode {
            #expect(decodedPaused.presentation.paused?.resumeButton.systemImageName == Theme.Icon.run)
        } else {
            Issue.record("paused must expose Resume")
        }
        // Stop is unconditional in AlarmButtons, with each sample's preserved original tint archive.
        try requirePreservedTint(runningAttributes, decodedRunning, context: "running Stop")
        try requirePreservedTint(pausedAttributes, decodedPaused, context: "paused Stop")
        // AlarmButtons owns its Stop symbol; the app target maps the same meaning to Theme.Icon.interrupt.
        #expect(Theme.Icon.interrupt == "stop.fill")
        #expect(PauseAlarmIntent(alarmID: runningContent.state.alarmID).alarmID == runningContent.state.alarmID.uuidString)
        #expect(ResumeAlarmIntent(alarmID: pausedContent.state.alarmID).alarmID == pausedContent.state.alarmID.uuidString)
        #expect(StopAlarmIntent(alarmID: runningContent.state.alarmID).alarmID == runningContent.state.alarmID.uuidString)
        #expect(StopAlarmIntent(alarmID: pausedContent.state.alarmID).alarmID == pausedContent.state.alarmID.uuidString)

        guard case .countdown(let countdown) = runningContent.state.mode else {
            Issue.record("the running sample must count down")
            return
        }
        #expect(countdown.startDate == now && countdown.fireDate == now.addingTimeInterval(1500))
        #expect(countdown.totalCountdownDuration == 1500 && countdown.previouslyElapsedDuration == 0)
        guard case .paused(let pause) = pausedContent.state.mode else {
            Issue.record("the paused sample must keep the time left")
            return
        }
        #expect(pause.totalCountdownDuration == 1500 && pause.previouslyElapsedDuration == 301)

        // Real timers keep both states and the complete AlarmKit presentation, unlike the custom widget's sample.
        let real = AlarmsTool.attributes(label: "Focus", timer: true)
        let timer = real.presentation
        #expect(timer.countdown?.pauseButton != nil && timer.paused?.resumeButton != nil)
        #expect(timer.countdown?.pauseButton?.systemImageName == Theme.Icon.pause)
        #expect(timer.paused?.resumeButton.systemImageName == Theme.Icon.run)
        // Encode/decode the real builder, never a reconstructed button. Newer SDKs omit the unused Alert Stop;
        // Pause/Resume still serialize, and AlarmButtons supplies Stop from the real tint and ContentState alarm ID.
        let realData = try encoder.encode(real)
        let decodedReal = try decoder.decode(AlarmAttributes<PersonaAlarmData>.self, from: realData)
        #expect(decodedReal.metadata?.label == "Focus")
        #expect(decodedReal.metadata?.isSample == nil)
        #expect(decodedReal.presentation.countdown?.pauseButton?.systemImageName == Theme.Icon.pause)
        #expect(decodedReal.presentation.paused?.resumeButton.systemImageName == Theme.Icon.run)
        #expect(decodedReal.presentation.countdown?.title.key == "Focus")
        #expect(decodedReal.presentation.paused?.title.key == "Focus")
        let realEncoded = try #require(JSONSerialization.jsonObject(with: realData) as? [String: Any])
        let realMetadata = try #require(realEncoded["metadata"] as? [String: Any])
        #expect(realMetadata["isSample"] == nil)
        let realPresentation = try #require(realEncoded["presentation"] as? [String: Any])
        let realAlert = try #require(realPresentation["alert"] as? [String: Any])
        let realTint = try #require(realEncoded["tintColor"] as? String)
        try requireEncodedStop(alert: realAlert, tintArchive: realTint, expectedText: real.presentation.alert.stopButton.text,
                               state: "real timer")
        try requirePreservedTint(real, decodedReal, context: "real timer")
        #expect(decodedReal.presentation.alert.stopButton.systemImageName.isEmpty == false)
        #expect(decodedReal.presentation.alert.title.defaultValue == real.presentation.alert.title.defaultValue)
        #expect(String(localized: decodedReal.presentation.alert.title) == String(localized: real.presentation.alert.title))
    }

    /// In-memory ActivityKit stand-in for testing DEBUG sample button handling without the live ActivityKit daemon.
    @available(iOS 26.0, *)
    @MainActor
    private final class FakeSampleActivities {
        var active: [String: AlarmsTool.SampleActivitySnapshot] = [:]
        var updated: [(String, ActivityContent<AlarmPresentationState>)] = []
        var ended: [String] = []
        var requested: [(String, AlarmAttributes<PersonaAlarmData>, ActivityContent<AlarmPresentationState>)] = []
        private var nextNumber = 1

        func add(activityID: String, attributes: AlarmAttributes<PersonaAlarmData>, content: ActivityContent<AlarmPresentationState>) {
            active[activityID] = AlarmsTool.SampleActivitySnapshot(
                activityID: activityID,
                state: content.state,
                label: attributes.metadata?.label ?? "Focus",
                hasCountdownPauseButton: attributes.presentation.countdown?.pauseButton != nil,
                isSample: attributes.metadata?.isSample == true
            )
        }

        var backend: AlarmsTool.SampleActivityBackend {
            AlarmsTool.SampleActivityBackend(
                find: { alarmID in
                    self.active.values.first { $0.state.alarmID == alarmID }
                },
                update: { activityID, content in
                    self.updated.append((activityID, content))
                    if var snapshot = self.active[activityID] {
                        snapshot.state = content.state
                        self.active[activityID] = snapshot
                    }
                },
                end: { activityID in
                    self.ended.append(activityID)
                    self.active.removeValue(forKey: activityID)
                },
                request: { attributes, content in
                    let id = "sample-replacement-\(self.nextNumber)"
                    self.nextNumber += 1
                    self.requested.append((id, attributes, content))
                    self.add(activityID: id, attributes: attributes, content: content)
                    return id
                }
            )
        }
    }

    @available(iOS 26.0, *)
    private final class AlarmManagerSpy: @unchecked Sendable {
        var calls: [(AlarmIntentAction, UUID)] = []
        var now: Date

        init(now: Date) {
            self.now = now
        }

        func deps(backend: AlarmsTool.SampleActivityBackend) -> AlarmIntentRouter.Deps {
            AlarmIntentRouter.Deps(
                pauseAlarm: { self.calls.append((.pause, $0)) },
                resumeAlarm: { self.calls.append((.resume, $0)) },
                stopAlarm: { self.calls.append((.stop, $0)) },
                handleSample: { action, id in
                    await AlarmsTool.handleSampleAction(action, alarmID: id, now: self.now, backend: backend)
                }
            )
        }
    }

    /// Tapping Pause, Resume or Stop on the DEBUG ActivityKit-only timer sample updates or ends the sample activity
    /// directly and never forwards the unscheduled sample UUID to `AlarmManager` (#13269).
    @Test func theTimerSampleButtonsStayOffAlarmManagerAndUpdateTheSampleActivity() async throws {
        guard #available(iOS 26.0, *) else { return }
        defer { AlarmsTool.resetSampleTracking() }

        let t0 = Date(timeIntervalSince1970: 1_800_000_000)
        let activities = FakeSampleActivities()
        let spy = AlarmManagerSpy(now: t0)
        let deps = spy.deps(backend: activities.backend)

        // 1. Running sample: Pause -> Resume -> Stop -> duplicate Stop.
        let runningAttrs = AlarmsTool.sampleAttributes(label: "Focus", paused: false)
        let runningContent = AlarmsTool.sampleContent(paused: false, now: t0)
        let runningID = runningContent.state.alarmID
        activities.add(activityID: "sample-running", attributes: runningAttrs, content: runningContent)
        AlarmsTool.resetSampleTracking(sampleActivityID: "sample-running", sampleAlarmID: runningID)

        try await AlarmIntentRouter.withDeps(deps) {
            spy.now = t0.addingTimeInterval(301)
            _ = try await PauseAlarmIntent(alarmID: runningID).perform()
            #expect(spy.calls.isEmpty, "sample Pause must never call AlarmManager")
            let pausedState = try #require(activities.active["sample-running"]?.state)
            #expect(pausedState.alarmID == runningID)
            guard case .paused(let pausedMode) = pausedState.mode else {
                Issue.record("sample Pause must switch the sample activity to paused")
                return
            }
            #expect(pausedMode.totalCountdownDuration == 1500 && pausedMode.previouslyElapsedDuration == 301)
            #expect(activities.requested.isEmpty)

            spy.now = t0.addingTimeInterval(500)
            _ = try await ResumeAlarmIntent(alarmID: runningID).perform()
            #expect(spy.calls.isEmpty, "sample Resume must never call AlarmManager")
            let resumedState = try #require(activities.active["sample-running"]?.state)
            guard case .countdown(let resumedMode) = resumedState.mode else {
                Issue.record("sample Resume must switch the sample activity back to countdown")
                return
            }
            #expect(resumedMode.totalCountdownDuration == 1500 && resumedMode.previouslyElapsedDuration == 301)
            #expect(resumedMode.startDate == spy.now && resumedMode.fireDate == spy.now.addingTimeInterval(1199))
            #expect(activities.requested.isEmpty, "running sample already has countdown.pauseButton and updates in place")

            _ = try await StopAlarmIntent(alarmID: runningID).perform()
            #expect(spy.calls.isEmpty, "sample Stop must never call AlarmManager")
            #expect(activities.ended == ["sample-running"])
            #expect(activities.active["sample-running"] == nil)
            #expect(AlarmsTool.sampleActivityID == nil && AlarmsTool.sampleAlarmID == nil)

            // A duplicate tap after Stop still recognizes the sample ID and never falls through to AlarmManager.
            _ = try await StopAlarmIntent(alarmID: runningID).perform()
            #expect(spy.calls.isEmpty, "late or duplicate sample Stop must still never call AlarmManager")
        }

        // 2. Paused-only sample (`-showAlarmSample paused`): Resume replaces the paused-only activity with a running
        // sample so Pause is available while staying below ActivityKit's 4 KB limit.
        let pausedAttrs = AlarmsTool.sampleAttributes(label: "Focus", paused: true)
        let pausedContent = AlarmsTool.sampleContent(paused: true, now: t0)
        let pausedID = pausedContent.state.alarmID
        activities.add(activityID: "sample-paused", attributes: pausedAttrs, content: pausedContent)
        // Clear in-memory tracking to also prove post-relaunch recognition via `PersonaAlarmData.isSample`.
        AlarmsTool.resetSampleTracking()

        try await AlarmIntentRouter.withDeps(deps) {
            spy.now = t0.addingTimeInterval(100)
            _ = try await ResumeAlarmIntent(alarmID: pausedID).perform()
            #expect(spy.calls.isEmpty, "paused sample Resume must never call AlarmManager even after relaunch")
            #expect(activities.ended.contains("sample-paused"))
            let replacement = try #require(activities.requested.last)
            #expect(replacement.1.presentation.countdown?.pauseButton != nil)
            #expect(replacement.1.metadata?.isSample == true)
            let encoded = try JSONEncoder().encode(replacement.1)
            #expect(encoded.count < 4096)
            #expect(replacement.2.state.alarmID == pausedID)
            guard case .countdown(let countdown) = replacement.2.state.mode else {
                Issue.record("resumed paused-only sample must count down")
                return
            }
            #expect(countdown.startDate == spy.now && countdown.fireDate == spy.now.addingTimeInterval(1199))
            #expect(AlarmsTool.sampleActivityID == replacement.0 && AlarmsTool.sampleAlarmID == pausedID)
        }
    }

    /// Real AlarmKit timer IDs always route directly to `AlarmManager`, even while a DEBUG sample activity exists, and
    /// stopping the sample never touches a real timer activity (#13269).
    @Test func realTimerButtonsStillRouteToAlarmManagerAlongsideASample() async throws {
        guard #available(iOS 26.0, *) else { return }
        defer { AlarmsTool.resetSampleTracking() }

        let t0 = Date(timeIntervalSince1970: 1_800_000_000)
        let activities = FakeSampleActivities()
        let spy = AlarmManagerSpy(now: t0)
        let deps = spy.deps(backend: activities.backend)

        let realID = UUID()
        let realAttrs = AlarmsTool.attributes(label: "Tea", timer: true)
        let realMode = AlarmPresentationState.Mode.countdown(.init(
            totalCountdownDuration: 300, previouslyElapsedDuration: 0, startDate: t0, fireDate: t0.addingTimeInterval(300)
        ))
        let realContent = ActivityContent(state: AlarmPresentationState(alarmID: realID, mode: realMode), staleDate: nil)
        activities.add(activityID: "real-activity", attributes: realAttrs, content: realContent)

        let sampleAttrs = AlarmsTool.sampleAttributes(label: "Focus", paused: false)
        let sampleContent = AlarmsTool.sampleContent(paused: false, now: t0)
        let sampleID = sampleContent.state.alarmID
        activities.add(activityID: "sample-activity", attributes: sampleAttrs, content: sampleContent)
        AlarmsTool.resetSampleTracking(sampleActivityID: "sample-activity", sampleAlarmID: sampleID)

        try await AlarmIntentRouter.withDeps(deps) {
            _ = try await PauseAlarmIntent(alarmID: realID).perform()
            _ = try await ResumeAlarmIntent(alarmID: realID).perform()
            _ = try await StopAlarmIntent(alarmID: realID).perform()

            #expect(spy.calls.count == 3)
            #expect(spy.calls[0].0 == .pause && spy.calls[0].1 == realID)
            #expect(spy.calls[1].0 == .resume && spy.calls[1].1 == realID)
            #expect(spy.calls[2].0 == .stop && spy.calls[2].1 == realID)
            #expect(activities.updated.isEmpty && activities.ended.isEmpty)

            // Stopping the sample ends only the sample and sends nothing to AlarmManager.
            _ = try await StopAlarmIntent(alarmID: sampleID).perform()
            #expect(spy.calls.count == 3)
            #expect(activities.ended == ["sample-activity"])
            #expect(activities.active["real-activity"] != nil)

            // A malformed ID triggers neither AlarmManager nor sample handling.
            try await AlarmIntentRouter.perform(.stop, alarmID: "not-a-uuid")
            #expect(spy.calls.count == 3)
        }
    }

    /// `updateLiveActivity` and `endLiveActivity` run `@concurrent` off the main actor and explicitly type `activity`
    /// as `Activity<AlarmAttributes<PersonaAlarmData>>` so `activity.update(_:)` neither runs ActivityKit work on the
    /// main actor nor creates an untyped `.update(_:)` edge to `HomeCalendarStore.update(_:)` (#13770).
    @Test func theTimerSampleActivityHelpersRunConcurrentlyWithTypedActivity() throws {
        let sourceURL = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent()
            .deletingLastPathComponent()
            .appending(path: "Persona/PhoneTools/AlarmsTool.swift")
        let lines = try String(contentsOf: sourceURL, encoding: .utf8).components(separatedBy: "\n")

        func declarationAndBody(_ marker: String) throws -> String {
            let index = try #require(lines.firstIndex { $0.contains(marker) }, "missing \(marker)")
            var start = index
            while start > 0 {
                let previous = lines[start - 1].trimmingCharacters(in: .whitespaces)
                if previous.hasPrefix("@") { start -= 1 } else { break }
            }
            var depth = 0, opened = false, out: [String] = []
            for line in lines[start...] {
                out.append(line)
                for ch in line {
                    if ch == "{" { depth += 1; opened = true }
                    if ch == "}" { depth -= 1 }
                }
                if opened, depth == 0 { break }
            }
            return out.joined(separator: "\n")
        }

        let updateHelper = try declarationAndBody("static func updateLiveActivity(")
        #expect(updateHelper.contains("@concurrent"), "updateLiveActivity must run off the main actor")
        #expect(updateHelper.contains("activity: Activity<AlarmAttributes<PersonaAlarmData>>"),
                "activity must be explicitly typed so activity.update(_:) cannot match HomeCalendarStore.update(_:)")

        let endHelper = try declarationAndBody("static func endLiveActivity(")
        #expect(endHelper.contains("@concurrent"), "endLiveActivity must run off the main actor")
        #expect(endHelper.contains("activity: Activity<AlarmAttributes<PersonaAlarmData>>"),
                "activity must be explicitly typed")
    }
    #endif

    // MARK: - Home (#2285)

    @Test func homeCommandsBecomeHomeKitValues() throws {
        #expect(try HomeTool.HomeCommand.parse(json(#"{"accessory_id":"a","control":"lock","value":true}"#)).number == 1)
        #expect(try HomeTool.HomeCommand.parse(json(#"{"accessory_id":"a","control":"door","value":"true"}"#)).number == 0)
        #expect(try HomeTool.HomeCommand.parse(json(#"{"accessory_id":"a","control":"power","value":"off"}"#)).number == 0)
        #expect(try HomeTool.HomeCommand.parse(json(#"{"accessory_id":"a","control":"brightness","value":42.4}"#)).number == 42)
        #expect(throws: ToolFailure.self) { try HomeTool.HomeCommand.parse(json(#"{"accessory_id":"a","control":"brightness","value":140}"#)) }
        #expect(throws: ToolFailure.self) { try HomeTool.HomeCommand.parse(json(#"{"accessory_id":"a","control":"colour","value":1}"#)) }
    }

    @Test func aSecurityAccessorySaysSoOnTheCard() throws {
        let unlock = try HomeTool.HomeCommand.parse(json(#"{"accessory_id":"a","control":"lock","value":false}"#))
        let card = HomeTool.summary(accessory: "Front Door", room: "Hall", sensitive: true, command: unlock)
        #expect(card.contains("Front Door") && card.contains("Hall"))
        #expect(card.contains(String(localized: "Security accessory: check before you allow it.")))
        let lamp = HomeTool.summary(accessory: "Lamp", room: nil, sensitive: false,
                                    command: try HomeTool.HomeCommand.parse(json(#"{"accessory_id":"b","control":"power","value":true}"#)))
        #expect(!lamp.contains(String(localized: "Security accessory: check before you allow it.")))
    }

    // MARK: - Media (#2284)

    @Test func otherServicesOpenASearchOnly() throws {
        #expect(MediaTool.searchURL(service: "spotify", query: "lofi beats")?.absoluteString == "https://open.spotify.com/search/lofi%20beats")
        #expect(MediaTool.searchURL(service: "youtube", query: "a&b")?.absoluteString == "https://www.youtube.com/results?search_query=a%26b")
        #expect(MediaTool.searchURL(service: "other", query: "x") == nil)
    }

    @Test func aPlaylistDraftIsCheckedBeforeTheCard() throws {
        let args = JSONValue.object([
            "name": .string(" Road Trip "),
            "songs": .array([.string("Midnight City M83"), .string("Outro M83")]),
            "play": .bool(true),
        ])
        let draft = try MediaTool.PlaylistDraft.parse(args)
        #expect(draft.name == "Road Trip" && draft.songs.count == 2 && draft.play && draft.note == nil)
        let summary = MediaTool.playlistConfirmation(args)
        #expect(summary.contains("Road Trip") && summary.contains("2"))
        #expect(try MediaTool.PlaylistDraft.parse(.object([
            "name": .string("Quiet"),
            "songs": .array([.string("Song")]),
            "play": .bool(false),
        ])).play == false)
        #expect((try? MediaTool.PlaylistDraft.parse(.object([
            "name": .string(""),
            "songs": .array([.string("Song")]),
        ]))) == nil)
        #expect((try? MediaTool.PlaylistDraft.parse(.object([
            "name": .string("Empty"),
            "songs": .array([]),
        ]))) == nil)
    }
}
