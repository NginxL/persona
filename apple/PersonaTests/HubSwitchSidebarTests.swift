import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Another hub made current (#11657): what the sidebar lists, Persona's chats and the Agents list narrowed to one hub,
/// is the current hub's, wherever the switch was made. Two groups ("" Home, "g1" Work), each with a Persona chat and an
/// Agent of its own on machines with the same ids.
@MainActor struct HubSwitchSidebarTests {
    private static func hub(_ id: String, group: String, port: Int) -> PairedHub {
        PairedHub(id: id, config: ServerConfig(url: URL(string: "http://127.0.0.1:\(port)")!, token: "t-\(id)"), group: group, hubID: id)
    }

    private static func session(_ json: String, group: String) throws -> Session {
        var s = try JSON.decoder.decode(Session.self, from: Data(json.utf8))
        s.group = group
        return s
    }

    /// Home and Work paired, Home current, each listing its Persona chat and an Agent.
    private func twoHubs() throws -> AppModel {
        let model = AppModel(stateFolder: FileManager.default.temporaryDirectory.appending(path: "hub-switch-sidebar-" + UUID().uuidString))
        model.linkHooksForTests = (transport: try #require(FakeHub().client.transport), socket: { SyncSocket() })
        model.registry = HubRegistry(hubs: [Self.hub("home", group: "", port: 1), Self.hub("work", group: "g1", port: 2)])
        model.buildLinks()
        var byMachine: [MachineRef: [Session]] = [:]
        for (group, name) in [("", "Home"), ("g1", "Work")] {
            let chat = try Self.session(#"{"machine_id":"server","session_id":"chat-\#(name)","title":"\#(name) chat","assistant":true}"#,
                                        group: group)
            let agent = try Self.session(#"{"machine_id":"mac","session_id":"agent-\#(name)","title":"\#(name) agent"}"#, group: group)
            byMachine[chat.machineRef, default: []].append(chat)
            byMachine[agent.machineRef, default: []].append(agent)
        }
        model.applySessions(byMachine)
        return model
    }

    /// The Agents list as the sidebar shows it narrowed to one hub (`stored`, the menu's last pick): its rows' titles.
    private func agents(_ model: AppModel, stored: String) -> [String] {
        let shown = HubFilter.shown(stored, current: model.hub?.group)
        return model.allSessions.filter { !$0.assistant && HubFilter.matches(shown, $0.group) }.map(\.title).sorted()
    }

    @Test func aListNarrowedToOneHubShowsTheCurrentHub() {
        #expect(HubFilter.shown("=", current: "g1") == HubFilter.value("g1"))
        #expect(HubFilter.shown("=g1", current: "") == HubFilter.value(""))
        #expect(HubFilter.shown("=g1", current: "g1") == "=g1")
        #expect(HubFilter.shown("", current: "g1") == "", "every hub stays every hub")
        #expect(HubFilter.shown("=g1", current: nil) == "=g1", "nothing paired: the filter as it was")
    }

    @Test func afterASwitchTheSidebarListsTheNewHubsChatsAndAgents() throws {
        let model = try twoHubs()
        defer { for link in model.links { link.live.disconnect() } }
        let narrowed = HubFilter.value("") // the Agents list narrowed to Home, as its menu left it
        #expect(model.assistantConversations.map(\.title) == ["Home chat"])
        #expect(agents(model, stored: narrowed) == ["Home agent"])

        model.selectHub("g1") // the Persona page's menu, the Inbox or Settings › hub
        #expect(model.hub?.group == "g1")
        #expect(model.assistantConversations.map(\.title) == ["Work chat"])
        #expect(agents(model, stored: narrowed) == ["Work agent"], "the Agents list stayed on the hub before")
        #expect(agents(model, stored: "") == ["Home agent", "Work agent"], "All Hubs still lists both")

        model.selectHub("")
        #expect(model.assistantConversations.map(\.title) == ["Home chat"])
        #expect(agents(model, stored: HubFilter.value("g1")) == ["Home agent"])
    }

    /// The sidebar reads which chats the scheduled tasks started again for another hub, not only for another manager or
    /// another number of chats: a column that stays out (the iPad's) kept the hub before's.
    @Test func theTaskChatsAreReadAgainForAnotherHub() {
        let home = AssistantTaskChatsStore.readKey(out: true, group: "", manager: nil, conversations: 4)
        let work = AssistantTaskChatsStore.readKey(out: true, group: "g1", manager: nil, conversations: 4)
        #expect(home != work)
        #expect(AssistantTaskChatsStore.readKey(out: true, group: "", manager: "m-2", conversations: 4) != home)
        #expect(AssistantTaskChatsStore.readKey(out: true, group: "", manager: nil, conversations: 5) != home)
        #expect(AssistantTaskChatsStore.readKey(out: false, group: "g1", manager: nil, conversations: 4) == "", "nothing while it is in")
    }
}
