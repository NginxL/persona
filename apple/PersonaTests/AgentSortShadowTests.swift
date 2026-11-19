import Foundation
import Testing
@testable import Persona
import PersonaCore
import PersonaCoreTestSupport
import PersonaWire

@MainActor struct AgentSortShadowTests {
    private func session(_ json: ContinuityJSON) throws -> Session {
        try JSON.decoder.decode(Session.self, from: Data(json.encoded().utf8))
    }

    private func nativeInput(_ fixture: ProjectionFixture) throws -> AgentListProjection.Input {
        try nativeSnapshot(fixture).input
    }

    private func nativeSnapshot(_ fixture: ProjectionFixture) throws -> (input: AgentListProjection.Input, presentation: AgentSortPresentation, now: Date?) {
        let input = fixture.input
        let settings = try ProjectionFixture.require(input["settings"], "settings")
        var preferences: [String: AgentListProjection.Preferences] = [:]
        var hubPreferences = input["hub_preferences"]?.object ?? [:]
        if hubPreferences.isEmpty { hubPreferences[""] = .object(["pins": input["pins"] ?? .object([:]), "mutes": input["mutes"] ?? .object([:])]) }
        for (group, value) in hubPreferences {
            var preference = AgentListProjection.Preferences()
            preference.pins = try JSON.decoder.decode(Pins.self, from: Data((value["pins"] ?? .object([:])).encoded().utf8))
            preference.mutes = try JSON.decoder.decode(Pins.self, from: Data((value["mutes"] ?? .object([:])).encoded().utf8))
            preferences[group] = preference
        }
        let grouping = settings.text("grouping") ?? "state"
        let machineFilter = settings.text("machine_filter") ?? ""
        let machines = try (input["machines"]?.array ?? []).map { machine -> Machine in
            var fields = try ProjectionFixture.require(machine.object, "machine")
            fields["kind"] = .string("local")
            fields["online"] = .bool(true)
            fields["app_hub_group"] = fields["group"]
            fields["manager"] = machine["manager"]
            fields.removeValue(forKey: "group")
            return try JSON.decoder.decode(Machine.self, from: Data(ContinuityJSON.object(fields).encoded().utf8))
        }
        var nativeSettings = AgentListProjection.Settings(
            grouping: grouping, sort: settings.text("sort") ?? "state",
            filter: AgentFilter(rawValue: settings.text("quick") ?? "all") ?? .all,
            query: settings.text("query") ?? "", machine: machineFilter,
            preferences: preferences)
        if let held = settings.text("held") { nativeSettings.held = SessionKey(path: held) }
        nativeSettings.kept = Set((settings["kept"]?.array?.compactMap(\.string) ?? []).compactMap(SessionKey.init(path:)))
        // These shared fixtures predate multi-pane rows. They describe the underlying session order, not a tab-card UI.
        let sessions = try (input["sessions"]?.array ?? []).map(session)
        let native = AgentListProjection.Input(sessions: sessions, machines: machines, settings: nativeSettings)
        var presentation = AgentSortPresentation()
        presentation.folded = Set(settings["folded"]?.array?.compactMap(\.string) ?? [])
        presentation.expandedIdle = Set(settings["expanded_idle"]?.array?.compactMap(\.string) ?? [])
        presentation.expandedQuiet = Set(settings["expanded_quiet"]?.array?.compactMap(\.string) ?? [])
        let now = settings.text("now").flatMap { ISO8601DateFormatter().date(from: $0) }
        return (native, presentation, now)
    }

    @Test func swiftProjectionReplaysAllThirtyAgentsListFixtures() throws {
        let fixtures = try ProjectionFixture.cases(in: "sort", operation: "agents_list")
        // The original #9058 corpus has 30 cases; later additions stay in this same comparison.
        #expect(fixtures.count >= 30)
        for fixture in fixtures {
            do {
                let snapshot = try nativeSnapshot(fixture)
                let input = snapshot.input
                let native = AgentSortComparable(native: input, presentation: snapshot.presentation, now: snapshot.now)
                let core = try ProjectionFixture.project(fixture.input, fixture.name, using: PersonaCoreProjections.sort)
                let coreComparable = try AgentSortComparable(core: try JSONDecoder().decode(AgentSortCoreProjection.self, from: Data(PersonaCoreProjections.sort(inputJSON: AgentSortProjectionAdapter.coreInput(input, presentation: snapshot.presentation, now: snapshot.now)).utf8)), input: input, presentation: snapshot.presentation)
                let differences = AgentSortComparable.differences(native: native, core: coreComparable)
                let known = ["machine_uses_registry_order": Set(["quick-filter", "pin-membership"])] // #9221
                if let expected = known[fixture.name], expected == Set(differences) {
                    withKnownIssue("Tracked core discrepancy for \(fixture.name)") {
                        #expect(differences == [], "\(fixture.name)")
                    }
                } else {
                    #expect(differences == [], "\(fixture.name)")
                }
                try ProjectionFixture.assertEqual(core, fixture.expected, "\(fixture.name)")
            } catch {
                Issue.record("\(fixture.name): \(error)")
            }
        }
    }

    @Test func spaceTimeTieReversedPreservesFilteredUnreadOrderThroughTheRealCABI() throws {
        let fixture = try ProjectionFixture.require(try ProjectionFixture.cases(in: "sort", operation: "agents_list").first { $0.name == "space_time_tie_reversed" }, "space_time_tie_reversed")
        let snapshot = try nativeSnapshot(fixture)
        let native = AgentSortComparable(native: snapshot.input, presentation: snapshot.presentation, now: snapshot.now)
        let legacy = try ProjectionFixture.project(fixture.input, fixture.name, using: PersonaCoreProjections.sort)
        try ProjectionFixture.assertEqual(legacy, fixture.expected, fixture.name)
        let output = try PersonaCoreProjections.sort(inputJSON: AgentSortProjectionAdapter.coreInput(snapshot.input, presentation: snapshot.presentation, now: snapshot.now))
        let core = try AgentSortComparable(core: JSONDecoder().decode(AgentSortCoreProjection.self, from: Data(output.utf8)), input: snapshot.input, presentation: snapshot.presentation)
        let expectedGroups = ["machine-a/space-a", "machine-a/space-b"]
        let expectedUnread = ["machine-a/b", "machine-a/a"]
        #expect(native.groups.map(\.id) == expectedGroups)
        #expect(core.groups.map(\.id) == expectedGroups)
        #expect(native.unread == expectedUnread)
        #expect(core.unread == expectedUnread)
        #expect(AgentSortComparable.differences(native: native, core: core) == [])
    }

    @Test func adapterKeepsSwiftOutputIdentical() throws {
        let fixture = try ProjectionFixture.require(try ProjectionFixture.cases(in: "sort", operation: "agents_list").first, "fixture")
        let input = try nativeInput(fixture)
        let expected = AgentListProjection.build(input)
        let actual = AgentSortProjectionAdapter().build(input)
        #expect(actual.base == expected.base)
        #expect(actual.filtered == expected.filtered)
        #expect(actual.pinnedAgents == expected.pinnedAgents)
        #expect(actual.unread == expected.unread)
        #expect(actual.active.map(\.id) == expected.active.map(\.id))
        #expect(actual.quiet.map(\.id) == expected.quiet.map(\.id))
    }

    #if DEBUG
    @Test func productionShapedMultiHubHeldAndMuteInputsMatchThroughTheRealCABI() throws {
        let machine = try JSON.decoder.decode(Machine.self, from: Data(#"{"id":"shared","name":"Fixture","kind":"local","online":true}"#.utf8))
        let sessions = try [
            ("second", "idle", "hub-a"), ("first", "working", "hub-a"), ("first", "idle", "hub-b"),
        ].map { id, state, group in
            var session = try self.session(.object([
                "machine_id": .string("shared"), "session_id": .string(id), "app_hub_group": .string(group),
                "title": .string("needle"), "state": .string(state), "status": .string("idle"),
                "updated_at": .string("2027-01-01T00:00:00Z"),
            ]))
            return session
        }
        var settings = AgentListProjection.Settings(grouping: "state", query: "")
        settings.multiHub = true
        settings.preferences["hub-a"] = .init(pins: Pins(), mutes: Pins())
        settings.preferences["hub-b"] = .init(pins: Pins(), mutes: Pins())
        settings.preferences["hub-a"]!.pins.sessions = ["shared/first", "shared/second"]
        settings.preferences["hub-a"]!.mutes.sessions = ["shared/first"]
        settings.preferences["hub-b"]!.pins.sessions = ["shared/second"]
        settings.preferences["hub-b"]!.mutes.sessions = ["shared/first"]
        settings.preferences["hub-b"]!.mutes.allowed = ["shared/first"]
        settings.held = sessions[0].key
        var input = AgentListProjection.Input(sessions: sessions, machines: [machine], settings: settings)
        input.settings.query = ""
        let output = try PersonaCoreProjections.sort(inputJSON: AgentSortProjectionAdapter.coreInput(input))
        let core = try AgentSortComparable(core: try JSONDecoder().decode(AgentSortCoreProjection.self, from: Data(output.utf8)), input: input)
        #expect(AgentSortComparable.differences(native: AgentSortComparable(native: input), core: core) == [])
        input.settings.query = "is:pinned"
        let filtered = try PersonaCoreProjections.sort(inputJSON: AgentSortProjectionAdapter.coreInput(input))
        let filteredCore = try AgentSortComparable(core: try JSONDecoder().decode(AgentSortCoreProjection.self, from: Data(filtered.utf8)), input: input)
        #expect(filteredCore.groups.map { $0.id } == ["pinned"])
        #expect(filteredCore.groups.first?.rows == ["hub-a|shared/first", "hub-a|shared/second"])
        let nativeFiltered = AgentSortComparable(native: input)
        #expect(nativeFiltered.groups.first?.rows == filteredCore.groups.first?.rows)
        #expect(AgentSortComparable.differences(native: nativeFiltered, core: filteredCore) == [])
        input.settings.query = ""
        let heldOutput = try PersonaCoreProjections.sort(inputJSON: AgentSortProjectionAdapter.coreInput(input))
        let coreHeld = try AgentSortComparable(core: JSONDecoder().decode(AgentSortCoreProjection.self, from: Data(heldOutput.utf8)), input: input)
        let nativeHeld = AgentSortComparable(native: input)
        #expect(nativeHeld.groups.first { $0.id == "pinned" }?.rows == ["hub-a|shared/first", "hub-a|shared/second"])
        #expect(nativeHeld.groups.first { $0.id == "state/idle" }?.rows == ["hub-b|shared/first"])
        #expect(nativeHeld.groups.contains { $0.id == "state/unread" && $0.rows.contains("hub-a|shared/second") } == false)
        #expect(AgentSortComparable.differences(native: nativeHeld, core: coreHeld) == [])
    }

    private func selectedHubInput(_ filter: AgentFilter = .all) throws -> AgentListProjection.Input {
        let sessions = try [("working", "hub-a"), ("unread", "hub-b")].map { state, group in
            try session(.object([
                "machine_id": .string("shared"), "session_id": .string("same"), "app_hub_group": .string(group),
                "title": .string("needle"), "state": .string(state), "status": .string("idle"),
                "updated_at": .string("2027-01-01T00:00:00Z"),
            ]))
        }
        let machine = try JSON.decoder.decode(Machine.self, from: Data(#"{"id":"shared","name":"Fixture","kind":"local","online":true}"#.utf8))
        var hubA = machine
        hubA.group = "hub-a"
        var hubB = machine
        hubB.group = "hub-b"
        var settings = AgentListProjection.Settings(grouping: "state")
        settings.filter = filter
        settings.multiHub = true
        settings.hub = HubFilter.value("hub-a")
        return AgentListProjection.Input(sessions: sessions, machines: [hubA, hubB], settings: settings)
    }

    @Test func selectedHubNativeAndCoreOutputMatchThroughTheRealCABI() throws {
        var input = try selectedHubInput()
        let encoded = try AgentSortProjectionAdapter.coreInput(input)
        let object = try #require(JSONSerialization.jsonObject(with: Data(encoded.utf8)) as? [String: Any])
        let encodedSettings = try #require(object["settings"] as? [String: Any])
        #expect(encodedSettings["multi_hub"] as? Bool == true)
        #expect(encodedSettings["hub_filter"] as? String == "=hub-a")
        let native = AgentSortComparable(native: input)
        // Production admits hub-a before counts, grouping, or unread output; hub-b has the same short ID.
        #expect(native.filtered == ["hub-a|shared/same"])
        #expect(native.filterCounts == ["needsYou": 0, "unread": 0, "working": 1, "backgroundWaiting": 0])
        #expect(native.groups.map(\.id) == ["state/working"])
        #expect(native.groups.first?.rows == ["hub-a|shared/same"])
        #expect(native.unread == [])
        let core = try AgentSortComparable(core: JSONDecoder().decode(AgentSortCoreProjection.self, from: Data(PersonaCoreProjections.sort(inputJSON: encoded).utf8)), input: input)
        #expect(AgentSortComparable.differences(native: native, core: core) == [])

        input.settings.filter = .unread
        let quickNative = AgentSortComparable(native: input)
        #expect(quickNative.filtered == [])
        #expect(quickNative.filterCounts == ["needsYou": 0, "unread": 0, "working": 1, "backgroundWaiting": 0])
        #expect(quickNative.groups == [])
        #expect(quickNative.unread == [])
        let quickOutput = try PersonaCoreProjections.sort(inputJSON: AgentSortProjectionAdapter.coreInput(input))
        let quickCore = try AgentSortComparable(core: JSONDecoder().decode(AgentSortCoreProjection.self, from: Data(quickOutput.utf8)), input: input)
        #expect(AgentSortComparable.differences(native: quickNative, core: quickCore) == [])
    }

    @Test func shadowComparesSelectedHubInputInsteadOfRejectingItAsHubFilter() async throws {
        let recorder = Recorder()
        let shadow = AgentSortShadow(report: { value in Task { await recorder.report(value) } })
        shadow.submit(try selectedHubInput())
        try await recorder.waitForSummary()
        let reports = await recorder.reports
        #expect(reports.map(\.rule) == ["summary-compared-1-mismatches-0"])
    }

    @Test func shadowRunsOffMainCoalescesAndNeverLeaksContent() async throws {
        let recorder = Recorder()
        let gate = Gate()
        let shadow = AgentSortShadow(report: { value in Task { await recorder.report(value) } }, project: { _ in
            #expect(!Thread.isMainThread)
            gate.waitSync()
            throw PersonaCoreProjectionError.emptyResult
        })
        let fixture = try ProjectionFixture.require(try ProjectionFixture.cases(in: "sort", operation: "agents_list").first, "fixture")
        let input = try nativeInput(fixture)
        shadow.submit(input)
        await gate.started()
        shadow.submit(input)
        shadow.submit(input)
        shadow.submit(input)
        await gate.openGate()
        try await recorder.wait()
        let mismatch = await recorder.mismatch
        #expect(mismatch?.rule == "core-call-error")
        #expect(mismatch?.sessions == 5)
        let description = String(describing: mismatch)
        #expect(!description.contains("示例会话"))
        #expect(!description.contains("machine-a/"))
        #expect(!description.contains("/home/"))
        try await recorder.waitForSummary()
        await gate.close()
    }

    @Test func shadowCoalescesAnExecutingComparisonToTheLatestInput() async throws {
        let gate = Gate()
        let recorder = Recorder()
        let calls = Calls()
        let fixture = try ProjectionFixture.require(try ProjectionFixture.cases(in: "sort", operation: "agents_list").first, "fixture")
        let input = try nativeInput(fixture)
        let shadow = AgentSortShadow(report: { value in Task { await recorder.report(value) } }, project: { json in
            let data = try JSONSerialization.jsonObject(with: Data(json.utf8)) as? [String: Any]
            let sessions = data?["sessions"] as? [[String: Any]] ?? []
            calls.append(sessions.compactMap { $0["session_id"] as? String }.joined(separator: ","))
            gate.waitSync()
            return #"{"counts":{},"groups":[],"unread_shown":[]}"#
        })
        var first = input
        first.sessions[0].sessionID = "first"
        shadow.submit(first, projection: AgentListProjection(), presentation: .init(), token: .init(revision: 1, settings: first.settings, presentation: .init()))
        await gate.started()
        var skipped = input
        skipped.sessions[0].sessionID = "skipped"
        shadow.submit(skipped, projection: AgentListProjection(), presentation: .init(), token: .init(revision: 2, settings: skipped.settings, presentation: .init()))
        var latest = input
        latest.sessions[0].sessionID = "latest"
        shadow.submit(latest, projection: AgentListProjection(), presentation: .init(), token: .init(revision: 3, settings: latest.settings, presentation: .init()))
        await gate.openGate()
        try await recorder.waitForSummary()
        #expect(calls.values.count == 2)
        #expect(calls.values.first?.contains("first") == true)
        #expect(calls.values.last?.contains("latest") == true)
        #expect(calls.values.contains { $0.contains("skipped") } == false)
        await gate.close()
    }

    @Test func shadowReturnsBeforeTheNativeProjection() async throws {
        let gate = Gate()
        let recorder = Recorder()
        let shadow = AgentSortShadow(report: { value in Task { await recorder.report(value) } }, project: { _ in gate.waitSync(); return "{}" })
        let fixture = try ProjectionFixture.require(try ProjectionFixture.cases(in: "sort", operation: "agents_list").first, "fixture")
        let input = try nativeInput(fixture)
        let start = Date()
        shadow.submit(input)
        #expect(Date().timeIntervalSince(start) < 0.1)
        await gate.openGate()
        try await recorder.waitForSummary()
        await gate.close()
    }

    @Test func sourceQuickFiltersReportTheirScopeInsteadOfANativeFailure() async throws {
        let recorder = Recorder()
        let shadow = AgentSortShadow(report: { value in Task { await recorder.report(value) } })
        let fixture = try ProjectionFixture.require(try ProjectionFixture.cases(in: "sort", operation: "agents_list").first, "fixture")
        var input = try nativeInput(fixture)
        input.settings.filter = .feishu
        shadow.submit(input)
        try await recorder.wait()
        #expect(await recorder.mismatch?.rule == "quick-filter-scope")
        try await recorder.waitForSummary()
    }

    @Test func theSinceReferenceIsSharedAtTheFractionalSecondBoundary() throws {
        let now = Date(timeIntervalSince1970: 1_800_086_400.9)
        let recent = try session(.object(["machine_id": .string("m"), "session_id": .string("recent"), "state": .string("working"), "status": .string("idle"), "updated_at": .string("2027-01-15T08:00:00.900Z")]))
        let outside = try session(.object(["machine_id": .string("m"), "session_id": .string("outside"), "state": .string("working"), "status": .string("idle"), "updated_at": .string("2027-01-15T08:00:00.899Z")]))
        var input = AgentListProjection.Input(sessions: [recent, outside], machines: [], settings: .init(grouping: "state", query: "since:24h"))
        #expect(AgentListProjection.build(input, now: now).filtered.map(\.sessionID) == ["recent"])
        let json = try AgentSortProjectionAdapter.coreInput(input, now: now)
        let encoded = try #require(JSONSerialization.jsonObject(with: Data(json.utf8)) as? [String: Any])
        #expect((encoded["settings"] as? [String: Any])?["now"] as? String == AgentSortProjectionAdapter.coreTimestamp(now))
        #expect(AgentSortProjectionAdapter.coreTimestamp(now).contains(".900"))
        let core = try AgentSortComparable(core: JSONDecoder().decode(AgentSortCoreProjection.self, from: Data(PersonaCoreProjections.sort(inputJSON: json).utf8)), input: input)
        #expect(core.filtered == ["m/recent"])
        #expect(AgentSortComparable.differences(native: AgentSortComparable(native: input, now: now), core: core) == [])
    }

    @Test func realTabRowsAndFoldedChildrenCompareThroughTheCABI() throws {
        let lead = try session(.object(["machine_id": .string("box"), "session_id": .string("lead"), "state": .string("working"), "status": .string("idle"), "space_id": .string("w1"), "space_kind": .string("herdr"), "space_name": .string("app"), "slot_id": .string("s1"), "slot_index": .number("1"), "pane_id": .string("w1:pW"), "pane_index": .number("0")]))
        let branch = try session(.object(["machine_id": .string("box"), "session_id": .string("branch"), "state": .string("idle"), "status": .string("idle"), "space_id": .string("w1"), "space_kind": .string("herdr"), "space_name": .string("app"), "slot_id": .string("s1"), "slot_index": .number("1"), "pane_id": .string("w1:p2E"), "pane_index": .number("1")]))
        let machine = try JSON.decoder.decode(Machine.self, from: Data(#"{"id":"box","name":"Fixture","kind":"local","online":true}"#.utf8))
        var input = AgentListProjection.Input(sessions: [branch, lead], machines: [machine], settings: .init(grouping: "space"))
        let presentation = AgentSortPresentation(folded: ["tab/box/app/1"], compact: true)
        let native = AgentSortComparable(native: input, presentation: presentation)
        #expect(native.presentationRows?.count == 1)
        #expect(native.presentationRows?.first?.visibleRows == [])
        #expect(native.presentationRows?.first?.rows == ["box/lead", "box/branch"])
        let core = try AgentSortComparable(core: JSONDecoder().decode(AgentSortCoreProjection.self, from: Data(PersonaCoreProjections.sort(inputJSON: AgentSortProjectionAdapter.coreInput(input, presentation: presentation)).utf8)), input: input, presentation: presentation)
        #expect(core.presentationRows?.map(\.foldID) == native.presentationRows?.map(\.foldID))
        #expect(core.presentationRows?.map(\.visibleRows) == native.presentationRows?.map(\.visibleRows))
        #expect(core.groups.map(\.visibleRows) == native.groups.map(\.visibleRows))
        #expect(AgentSortComparable.differences(native: native, core: core) == [])
        var parent = lead
        parent.spaceID = "w1"
        parent.children = .init(total: 1, working: 1)
        parent.backgroundWaiting = true
        let orphan = try session(.object(["machine_id": .string("box"), "session_id": .string("orphan"), "state": .string("working"), "status": .string("idle"), "source_kind": .string("runtime"), "is_subagent": .bool(true)]))
        input.sessions = [parent, orphan]
        let nativeFold = AgentSortComparable(native: input, presentation: presentation)
        let coreFold = try AgentSortComparable(core: JSONDecoder().decode(AgentSortCoreProjection.self, from: Data(PersonaCoreProjections.sort(inputJSON: AgentSortProjectionAdapter.coreInput(input, presentation: presentation)).utf8)), input: input, presentation: presentation)
        #expect(nativeFold.filterCounts["working"] == 1)
        #expect(nativeFold.filterCounts["backgroundWaiting"] == 0)
        input.settings.sideFold = [""]
        parent.foldedUnder = nil
        let side = try session(.object(["machine_id": .string("box"), "session_id": .string("side"), "state": .string("working"), "status": .string("idle"), "space_id": .string("w1"), "space_kind": .string("herdr"), "space_name": .string("app"), "slot_id": .string("s1"), "slot_index": .number("1"), "pane_id": .string("w1:p3A"), "pane_index": .number("2"), "folded_under": .string("lead"), "folded_as": .string("side")]))
        input.sessions = [parent, branch, side, orphan]
        let sidePresentation = AgentSortPresentation(compact: true)
        let nativeSide = AgentSortComparable(native: input, presentation: sidePresentation)
        #expect(nativeSide.presentationRows?.map(\.id) == ["box/orphan", "tab|box/herdr:w1/s1"])
        #expect(nativeSide.presentationRows?.last?.rows == ["box/lead", "box/branch"])
        let coreSide = try AgentSortComparable(core: JSONDecoder().decode(AgentSortCoreProjection.self, from: Data(PersonaCoreProjections.sort(inputJSON: AgentSortProjectionAdapter.coreInput(input, presentation: sidePresentation)).utf8)), input: input, presentation: sidePresentation)
        #expect(coreSide.groups.map(\.visibleRows) == nativeSide.groups.map(\.visibleRows))
        #expect(AgentSortComparable.differences(native: nativeSide, core: coreSide) == [])
    }

    @Test func compactSpaceRowsAndUnreadKeepTheirOwnOrderThroughTheRealCABI() throws {
        let sessions = try ["b", "a"].map { id in
            try session(.object([
                "machine_id": .string("machine-a"), "session_id": .string(id),
                "title": .string("Fixture"), "state": .string("unread"), "status": .string("idle"),
                "space_id": .string("space-a"), "space_name": .string("Project"),
                "updated_at": .string("2026-01-01T00:00:10Z"),
            ]))
        }
        let machine = try JSON.decoder.decode(Machine.self, from: Data(#"{"id":"machine-a","name":"Fixture","kind":"local","online":true}"#.utf8))
        let input = AgentListProjection.Input(sessions: sessions, machines: [machine], settings: .init(grouping: "space"))
        let presentation = AgentSortPresentation(compact: true)
        let native = AgentSortComparable(native: input, presentation: presentation)
        let expectedOrder = ["machine-a/b", "machine-a/a"]
        #expect(native.filtered == expectedOrder)
        #expect(native.presentationRows?.count == 2)
        #expect(Set(native.presentationRows?.map(\.id) ?? []) == Set(expectedOrder))
        #expect(native.unread == expectedOrder)
        let output = try PersonaCoreProjections.sort(inputJSON: AgentSortProjectionAdapter.coreInput(input, presentation: presentation))
        let core = try AgentSortComparable(core: JSONDecoder().decode(AgentSortCoreProjection.self, from: Data(output.utf8)), input: input, presentation: presentation)
        // Compact display order and unread order are independent contracts; neither is derived from the other.
        #expect(core.presentationRows?.map(\.id) == native.presentationRows?.map(\.id))
        #expect(core.unread == expectedOrder)
        #expect(AgentSortComparable.differences(native: native, core: core) == [])
    }

    @Test func presentationComparisonDetectsReorderedRows() throws {
        let fixture = try ProjectionFixture.require(try ProjectionFixture.cases(in: "sort", operation: "agents_list").first, "fixture")
        var input = try nativeInput(fixture)
        let native = AgentSortComparable(native: input, presentation: AgentSortPresentation(compact: true))
        var reversed = native
        reversed.groups.reverse()
        #expect(AgentSortComparable.differences(native: native, core: reversed).contains("grouping"))
        var rows = [AgentSortCoreProjection.Row(id: "one", foldID: "one", rows: ["one"], visibleRows: ["one"], folded: false),
                    AgentSortCoreProjection.Row(id: "two", foldID: "two", rows: ["two"], visibleRows: ["two"], folded: false)]
        var left = native
        var right = native
        left.presentationRows = rows
        rows.reverse()
        right.presentationRows = rows
        #expect(AgentSortComparable.differences(native: left, core: right).contains("presentation-rows"))
    }

    @Test func presentationFoldChangesDoNotRebuildTheCachedProductionProjection() throws {
        let input = try nativeInput(ProjectionFixture.require(try ProjectionFixture.cases(in: "sort", operation: "agents_list").first, "fixture"))
        let builds = Counter()
        let adapter = AgentSortProjectionAdapter { input, _ in
            builds.increment()
            return AgentListProjection.build(input)
        }
        let cache = AgentListProjectionCache(projection: adapter)
        let key = AgentListProjectionCache.Key(revision: 1, partitions: [], settings: input.settings)
        func resolve() -> AgentListProjection {
            return cache.resolve(key, immediate: false, input: { input }, valid: { _ in true }, canPublish: { _ in true }, willPublish: {})
        }
        let first = resolve()
        var presentation = AgentSortPresentation()
        presentation.folded.insert("state/idle")
        presentation.expandedQuiet.insert("space")
        presentation.compact = true
        let before = key
        #expect(before.settings == key.settings)
        let after = AgentListProjectionCache.Key(revision: 1, partitions: [], settings: input.settings)
        #expect(before == after, "presentation does not change the production cache key")
        let changedToken = ShadowToken(revision: 1, settings: input.settings, presentation: presentation)
        let originalToken = ShadowToken(revision: 1, settings: input.settings, presentation: .init())
        #expect(changedToken != originalToken, "fold/compact change only the shadow token")
        #expect(resolve().identity == first.identity)
        #expect(resolve().base == first.base)
        #expect(builds.value == 1, "a fold is not in Settings or the cache key, so the Swift adapter is not called again")
        #expect(cache.pending == nil)
    }

    @Test func rawSideFoldInputPromotesOnlyItsParentForTheCore() throws {
        let parent = try session(.object(["machine_id": .string("box"), "session_id": .string("parent"), "state": .string("idle"), "status": .string("idle"), "updated_at": .string("2027-01-01T00:00:00Z")]))
        let side = try session(.object(["machine_id": .string("box"), "session_id": .string("side"), "state": .string("working"), "status": .string("idle"), "folded_under": .string("parent"), "folded_as": .string("side"), "updated_at": .string("2027-01-01T00:00:01Z")]))
        var settings = AgentListProjection.Settings(grouping: "state")
        settings.sideFold = [""]
        var input = AgentListProjection.Input(sessions: [parent, side], machines: [], settings: settings)
        let native = AgentSortComparable(native: input)
        #expect(native.filtered == ["box/parent"])
        #expect(native.filterCounts["working"] == 1)
        let core = try AgentSortComparable(core: JSONDecoder().decode(AgentSortCoreProjection.self, from: Data(PersonaCoreProjections.sort(inputJSON: AgentSortProjectionAdapter.coreInput(input)).utf8)), input: input)
        #expect(AgentSortComparable.differences(native: native, core: core) == [])
        input.sessions = [side]
        #expect(AgentListProjection.build(input).base.map(\.key) == [side.key], "a missing parent leaves the side visible")
        let orphanCore = try AgentSortComparable(core: JSONDecoder().decode(AgentSortCoreProjection.self, from: Data(PersonaCoreProjections.sort(inputJSON: AgentSortProjectionAdapter.coreInput(input)).utf8)), input: input)
        #expect(AgentSortComparable.differences(native: AgentSortComparable(native: input), core: orphanCore) == [])
    }

    private func waitForIdle(_ shadow: AgentSortShadow) async throws {
        let end = ContinuousClock.now + .seconds(10)
        while !shadow.isIdle && ContinuousClock.now < end { try await Task.sleep(for: .milliseconds(5)) }
        #expect(shadow.isIdle, "shadow did not become idle within 10 seconds")
    }

    private final class Counter: @unchecked Sendable {
        private let lock = NSLock()
        private var count = 0
        var value: Int { lock.lock(); defer { lock.unlock() }; return count }
        func increment() { lock.lock(); count += 1; lock.unlock() }
    }

    @Test func identicalSnapshotsAreNotSubmittedAgain() async throws {
        let calls = Calls()
        let recorder = Recorder()
        let shadow = AgentSortShadow(report: { value in Task { await recorder.report(value) } }, project: { _ in
            calls.append("called")
            return #"{"counts":{},"groups":[],"unread_shown":[]}"#
        })
        let input = try nativeInput(ProjectionFixture.require(try ProjectionFixture.cases(in: "sort", operation: "agents_list").first, "fixture"))
        for _ in 0..<4 { shadow.submit(input, projection: AgentListProjection()) }
        try await waitForIdle(shadow)
        #expect(calls.values.count == 1)
        shadow.submit(input, projection: AgentListProjection())
        try await waitForIdle(shadow)
        #expect(calls.values.count == 1, "a completed identical snapshot is not compared again")
        var presentation = AgentSortPresentation()
        presentation.folded.insert("state/idle")
        shadow.submit(input, projection: AgentListProjection(), presentation: presentation)
        try await recorder.waitForCompared(atLeast: 2)
        #expect(calls.values.count == 2)
    }

    @Test func aggregateCountsAreAppliedToBothSides() throws {
        let session = try self.session(.object(["machine_id": .string("m"), "session_id": .string("w"), "state": .string("working"), "status": .string("idle"), "background_waiting": .bool(true)]))
        var settings = AgentListProjection.Settings(grouping: "space")
        settings.pagedGroups = [""]
        let input = AgentListProjection.Input(sessions: [session], machines: [], settings: settings)
        let presentation = AgentSortPresentation(aggregate: ["m/-": SessionListCounts(total: 1, machine: [:], project: [:], group: [:], state: ["working": 1, "idle": 0], revision: "1")])
        let native = AgentSortComparable(native: input, presentation: presentation)
        let core = try AgentSortComparable(core: JSONDecoder().decode(AgentSortCoreProjection.self, from: Data(PersonaCoreProjections.sort(inputJSON: AgentSortProjectionAdapter.coreInput(input, presentation: presentation)).utf8)), input: input, presentation: presentation)
        #expect(native.groups.first?.stateCounts?["working"] == 1)
        #expect(native.groups.first?.stateCounts?["idle"] == 0)
        #expect(core.groups.first?.stateCounts == native.groups.first?.stateCounts)
        #expect(AgentSortComparable.differences(native: native, core: core) == [])
    }

    @Test func nestedRowsAreNotFlattenedIntoTheCoreInventory() throws {
        let parent = try session(.object(["machine_id": .string("m"), "session_id": .string("parent"), "state": .string("working"), "status": .string("idle")]))
        let child = try session(.object(["machine_id": .string("m"), "session_id": .string("child"), "state": .string("working"), "status": .string("idle"), "source_kind": .string("runtime"), "is_subagent": .bool(true), "parent_session_id": .string("parent")]))
        let input = AgentListProjection.Input(sessions: [parent, child], machines: [], settings: .init(grouping: "state"))
        let encoded = try AgentSortProjectionAdapter.coreInput(input)
        let object = try #require(JSONSerialization.jsonObject(with: Data(encoded.utf8)) as? [String: Any])
        #expect((object["sessions"] as? [[String: Any]])?.map { $0["session_id"] as? String } == ["parent"])
        let native = AgentSortComparable(native: input)
        let core = try AgentSortComparable(core: JSONDecoder().decode(AgentSortCoreProjection.self, from: Data(PersonaCoreProjections.sort(inputJSON: encoded).utf8)), input: input)
        #expect(AgentSortComparable.differences(native: native, core: core) == [])
    }

    private final class Calls: @unchecked Sendable {
        private let lock = NSLock()
        private var recorded: [String] = []
        var values: [String] { lock.lock(); defer { lock.unlock() }; return recorded }
        func append(_ value: String) { lock.lock(); recorded.append(value); lock.unlock() }
    }

    private final class Gate: @unchecked Sendable {
        private let lock = NSLock()
        private var opened = false
        private var waiters: [DispatchSemaphore] = []
        private let startedSemaphore = DispatchSemaphore(value: 0)

        nonisolated func waitSync() {
            startedSemaphore.signal()
            let semaphore = DispatchSemaphore(value: 0)
            lock.lock()
            if opened { lock.unlock(); return }
            waiters.append(semaphore)
            lock.unlock()
            #expect(semaphore.wait(timeout: .now() + 10) == .success, "shadow gate timed out")
        }
        nonisolated func started() async {
            await withCheckedContinuation { (continuation: CheckedContinuation<Void, Never>) in
                DispatchQueue.global().async {
                    _ = self.startedSemaphore.wait(timeout: .now() + 10)
                    continuation.resume()
                }
            }
        }
        nonisolated func openGate() {
            lock.lock()
            opened = true
            let waiters = self.waiters
            self.waiters = []
            lock.unlock()
            for waiter in waiters { waiter.signal() }
        }
        nonisolated func close() { openGate() }
    }

    private actor Recorder {
        var mismatch: AgentSortShadowMismatch?
        var reports: [AgentSortShadowMismatch] = []
        func report(_ value: AgentSortShadowMismatch) {
            reports.append(value)
            if mismatch == nil && !value.rule.hasPrefix("summary-") { mismatch = value }
        }
        var summaryCount: Int { reports.filter { $0.rule.hasPrefix("summary-") }.count }
        private var hasSummary: Bool { reports.contains { $0.rule.hasPrefix("summary-") } }
        func hasSummaryNow() -> Bool { hasSummary }
        func wait() async throws {
            let end = ContinuousClock.now + .seconds(10)
            while mismatch == nil && ContinuousClock.now < end { try await Task.sleep(for: .milliseconds(5)) }
            #expect(mismatch != nil, "shadow report did not arrive within 10 seconds")
        }
        func waitForCompared(atLeast count: Int) async throws {
            let end = ContinuousClock.now + .seconds(10)
            while ContinuousClock.now < end {
                let latest = reports.filter { $0.rule.hasPrefix("summary-") }.last?.rule
                if let latest, let compared = Int(latest.split(separator: "-")[2]), compared >= count { return }
                try await Task.sleep(for: .milliseconds(5))
            }
            #expect(Bool(false), "shadow comparison count \(count) did not arrive within 10 seconds")
        }
        func waitForSummaryCount(atLeast count: Int) async throws {
            let end = ContinuousClock.now + .seconds(10)
            while summaryCount < count && ContinuousClock.now < end { try await Task.sleep(for: .milliseconds(5)) }
            #expect(summaryCount >= count, "shadow summary \(count) did not arrive within 10 seconds")
        }
        func waitForSummary() async throws {
            let end = ContinuousClock.now + .seconds(10)
            while await !hasSummaryNow() && ContinuousClock.now < end {
                try await Task.sleep(for: .milliseconds(5))
            }
            #expect(await hasSummaryNow(), "shadow queue did not drain within 10 seconds")
        }
    }
    #endif
}
