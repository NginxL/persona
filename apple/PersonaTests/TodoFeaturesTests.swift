import Foundation
import Observation
import Testing
@testable import Persona

/// The hub-level group as the app's pick gives it (nil: no hub paired).
@MainActor @Observable
private final class TodoHubPick {
    var group: String? = "a"
}

/// What the hub can do for To-dos, remembered per group, and the V1 gate it opens (#10465, docs/apple/ios.md "The gate
/// and its memory"): on for the hub-level group once its hello lists `todo_view`, or before its hello when its last one
/// did, and then on for that group until the app quits; picking another group's hub follows that group.
@MainActor
struct TodoFeaturesTests {
    private func defaults() throws -> UserDefaults {
        let name = "TodoFeaturesTests." + UUID().uuidString
        let d = try #require(UserDefaults(suiteName: name))
        d.removePersistentDomain(forName: name)
        return d
    }

    /// A gate whose hub-level group is `pick`'s and whose groups' hellos are `hellos` (else their last one, remembered).
    private func gate(_ d: UserDefaults, pick: TodoHubPick, hellos: [String: TodoFeatures] = [:], changes: (() -> Void)? = nil,
                      follows: Bool = false) -> TodosV1 {
        TodosV1(allowed: true, defaults: d, launchGroup: pick.group ?? "", hubGroup: { pick.group },
                features: { hellos[$0] ?? .remembered($0, in: d) }, changed: { changes?() }, followsHub: follows)
    }

    @Test func theHelloSaysWhatTheHubCanDo() {
        let f = TodoFeatures(["todo_view", "todo_places", "todo_seen", "sessions_list"])
        #expect(f == TodoFeatures(view: true, places: true))
        #expect(TodoFeatures(["todo_timeline", "todo_asks"]) == TodoFeatures(timeline: true, asks: true))
        #expect(TodoFeatures([]) == TodoFeatures())
    }

    /// Kept per group, written only when it changes, and forgotten with the group's hub.
    @Test func eachGroupsLastHelloIsRemembered() throws {
        let d = try defaults()
        #expect(TodoFeatures.remembered("g", in: d) == TodoFeatures())
        TodoFeatures.remember(TodoFeatures(view: true, places: true, asks: true), group: "g", in: d)
        #expect(TodoFeatures.remembered("g", in: d) == TodoFeatures(view: true, places: true, asks: true))
        #expect(TodoFeatures.remembered("other", in: d) == TodoFeatures())
        TodoFeatures.remember(TodoFeatures(), group: "g", in: d)
        #expect(TodoFeatures.remembered("g", in: d) == TodoFeatures())
        TodoFeatures.remember(TodoFeatures(view: true), group: "g", in: d)
        TodoFeatures.forget("g", in: d)
        #expect(TodoFeatures.remembered("g", in: d) == TodoFeatures() && d.object(forKey: "hubTodoFeatures.g") == nil)
    }

    /// The remembered value turns V1 on before this launch's hello, so the first frame is drawn the way it will stay.
    @Test func theRememberedValueTurnsV1OnInTheFirstFrame() throws {
        let d = try defaults()
        TodoFeatures.remember(TodoFeatures(view: true), group: "g", in: d)
        #expect(TodosV1(allowed: true, defaults: d, launchGroup: "g", hubGroup: { "g" }).isOn)
        #expect(!TodosV1(allowed: true, defaults: d, launchGroup: "another", hubGroup: { "another" }).isOn)
        d.set("g", forKey: AppModel.hubGroupKey)
        #expect(TodosV1(allowed: true, defaults: d, hubGroup: { "g" }).isOn, "the hub-level group as the app kept it")
    }

    /// Once on, a hello without `todo_view` (a manager reconnecting) never takes the page back.
    @Test func aHelloWithoutTheViewMidSessionKeepsItOn() throws {
        let pick = TodoHubPick()
        var told = 0
        let gate = gate(try defaults(), pick: pick, changes: { told += 1 })
        #expect(!gate.isOn)
        gate.heard(TodoFeatures(["todo_view", "todo_places"]), from: "a")
        #expect(gate.isOn && told == 1)
        gate.heard(TodoFeatures(["todo_seen"]), from: "a")
        gate.follow()
        #expect(gate.isOn && told == 1)
    }

    /// A hub that never lists `todo_view` keeps today's screens; so does another group's hello, and every hub while
    /// this build has V1 off (before the switch-on, and on visionOS).
    @Test func anOldHubKeepsItOff() throws {
        let gate = gate(try defaults(), pick: TodoHubPick())
        gate.heard(TodoFeatures(["todo_seen", "sessions_list"]), from: "a")
        gate.heard(TodoFeatures(["todo_view"]), from: "elsewhere")
        #expect(!gate.isOn)
        let d = try defaults()
        TodoFeatures.remember(TodoFeatures(view: true), group: "g", in: d)
        let dormant = TodosV1(allowed: false, defaults: d, launchGroup: "g", hubGroup: { "g" })
        dormant.heard(TodoFeatures(["todo_view"]), from: "g")
        dormant.follow()
        #expect(!dormant.isOn && !TodosV1.released)
        #expect(TodosV1.platform, "the iPhone and iPad; visionOS keeps today's To-dos")
    }

    /// Picking an older group's hub turns V1 off; picking back the group it was on for turns it on without another
    /// hello.
    @Test func pickingAnOldHubTurnsItOff() throws {
        let pick = TodoHubPick()
        var told = 0
        let gate = gate(try defaults(), pick: pick, hellos: ["b": TodoFeatures(["todo_seen"])], changes: { told += 1 })
        gate.heard(TodoFeatures(["todo_view"]), from: "a")
        #expect(gate.isOn)
        pick.group = "b"
        gate.follow()
        #expect(!gate.isOn && told == 2)
        pick.group = "a"
        gate.follow()
        #expect(gate.isOn && told == 3)
    }

    /// Picking a group whose hello listed `todo_view` while another was picked turns V1 on without another hello; so
    /// does one whose last hello did, before its hello in this run.
    @Test func pickingAV1HubTurnsItOn() throws {
        let d = try defaults()
        let pick = TodoHubPick()
        pick.group = "b"
        let gate = gate(d, pick: pick)
        gate.heard(TodoFeatures(["todo_view"]), from: "a")
        #expect(!gate.isOn, "not the hub-level group's")
        pick.group = "a"
        gate.follow()
        #expect(gate.isOn)
        pick.group = "b"
        gate.follow()
        #expect(!gate.isOn)
        TodoFeatures.remember(TodoFeatures(view: true), group: "c", in: d)
        pick.group = "c"
        gate.follow()
        #expect(gate.isOn, "c's last hello listed it")
    }

    /// Every hub signed out: off, and nothing kept for the groups this run knew, so a hub paired later under the same
    /// group starts from its own hello.
    @Test func signingOutForgetsTheGroups() throws {
        let d = try defaults()
        let pick = TodoHubPick()
        let gate = gate(d, pick: pick)
        gate.heard(TodoFeatures(["todo_view", "todo_places"]), from: "a")
        #expect(gate.isOn && TodoFeatures.remembered("a", in: d).view)
        pick.group = nil
        gate.follow()
        #expect(!gate.isOn && TodoFeatures.remembered("a", in: d) == TodoFeatures())
        pick.group = "a"
        gate.follow()
        #expect(!gate.isOn, "a new hub in the group: its own hello decides")
    }

    /// One group signed out while another stays: nothing is kept for it, so a launch whose stored pick still names it
    /// (only picking a hub writes that) starts with today's screens and lets the hub the app falls back to decide.
    @Test func aGoneGroupIsNotWhereTheNextLaunchStarts() throws {
        let d = try defaults()
        let pick = TodoHubPick()
        pick.group = "x"
        let gate = gate(d, pick: pick)
        gate.heard(TodoFeatures(["todo_view"]), from: "x")
        #expect(gate.isOn)
        gate.forget(["x"])
        pick.group = "a"
        gate.follow()
        #expect(!gate.isOn && TodoFeatures.remembered("x", in: d) == TodoFeatures())
        d.set("x", forKey: AppModel.hubGroupKey)
        #expect(!TodosV1(allowed: true, defaults: d, hubGroup: { "a" }).isOn, "the stored pick names a gone group")
        pick.group = "x"
        gate.follow()
        #expect(!gate.isOn, "a hub paired again under it: its own hello decides")
    }

    /// The app's own gate follows the owner's pick of the hub-level group.
    @Test func theGateFollowsThePick() async throws {
        let pick = TodoHubPick()
        let gate = gate(try defaults(), pick: pick, hellos: ["b": TodoFeatures()], follows: true)
        gate.heard(TodoFeatures(["todo_view"]), from: "a")
        #expect(gate.isOn)
        try await Task.sleep(for: .milliseconds(20)) // the gate starts following once made
        pick.group = "b"
        for _ in 0..<100 where gate.isOn { try await Task.sleep(for: .milliseconds(10)) }
        #expect(!gate.isOn)
        pick.group = "a"
        for _ in 0..<100 where !gate.isOn { try await Task.sleep(for: .milliseconds(10)) }
        #expect(gate.isOn)
    }

    /// A hello without `todo_view` after one with it in this run (a manager reconnecting) does not change how the next
    /// launch starts; the first hello of a run that lacks it does.
    @Test func aReconnectsHelloIsNotRememberedAsADowngrade() throws {
        let d = try defaults()
        let pick = TodoHubPick()
        let run = gate(d, pick: pick)
        run.heard(TodoFeatures(["todo_view", "todo_places"]), from: "a")
        run.heard(TodoFeatures(["todo_seen"]), from: "a")
        #expect(TodoFeatures.remembered("a", in: d) == TodoFeatures(view: true), "this run listed it: kept")
        let next = gate(d, pick: pick)
        #expect(next.isOn)
        next.heard(TodoFeatures(["todo_seen"]), from: "a")
        #expect(next.isOn, "on for this run")
        #expect(TodoFeatures.remembered("a", in: d) == TodoFeatures(), "the next launch starts with today's screens")
    }

    /// A group's link answers with what its last hello said until this launch's hello comes, then with the hello, and
    /// the hello is remembered for the next launch.
    @Test func aLinkUsesTheLastHelloUntilItsOwn() {
        let group = "todo-features-" + UUID().uuidString
        defer {
            for key in ["hubTodoFeatures.", "hubSideFold.", "hubExtensionsAPI."] { UserDefaults.standard.removeObject(forKey: key + group) }
        }
        TodoFeatures.remember(TodoFeatures(view: true, places: true), group: group)
        let config = ServerConfig(url: URL(string: "https://fake.invalid")!, token: "t")
        let link = HubLink(group: group, config: config, pins: PinsStore(deps: .init(api: { nil }, report: { _ in }, suffix: group)))
        #expect(link.todoFeatures == TodoFeatures(view: true, places: true))
        link.received(HubHello(features: ["todo_view", "sessions_list"]))
        #expect(link.todoFeatures == TodoFeatures(view: true))
        #expect(TodoFeatures.remembered(group) == TodoFeatures(view: true))
    }
}
