import SwiftUI
import Testing
@testable import Persona

/// The Sidebar layout's page: each sidebar item is a root of its own, only drill-downs push (#8359).
@MainActor struct SidebarPageRootsTests {
    private let inbox = SidebarPageRoot.page(ShellRoute.inbox)
    private let recordings = SidebarPageRoot.page(ShellRoute.recordings)
    private let agent = SidebarPageRoot.page(SessionKey(machineID: "m", sessionID: "a1"))

    private func path(_ ids: String...) -> NavigationPath { NavigationPath(ids.map { MachineDetailRoute(id: $0) }) }

    @Test func fromTheConversationAPageOrAnAgentReplacesTheRootWithoutAPush() {
        var roots = SidebarPageRoots()
        for root in [recordings, agent] {
            roots = SidebarPageRoots()
            let step = roots.select(root, path: NavigationPath())
            #expect(step.move == .switchRoot && !step.animated && step.path.isEmpty)
            #expect(step.root == root && roots.current == root)
        }
    }

    @Test func backToTheConversationIsASwitchNotAPop() {
        var roots = SidebarPageRoots()
        _ = roots.select(inbox, path: NavigationPath())
        let step = roots.select(.conversation, path: NavigationPath())
        #expect(step.move == .switchRoot && !step.animated && step.root == .conversation)
    }

    @Test func drillingDownPushesWithAnimation() {
        var roots = SidebarPageRoots()
        _ = roots.select(inbox, path: NavigationPath())
        let step = roots.drill(MachineDetailRoute(id: "x"), over: NavigationPath())
        #expect(step.move == .drillDown && step.animated && !step.closesOverlay)
        #expect(step.root == inbox && step.path == path("x"))
    }

    @Test func eachRootKeepsItsDrillDown() {
        var roots = SidebarPageRoots()
        _ = roots.select(inbox, path: NavigationPath())
        _ = roots.select(agent, path: path("item"))
        #expect(roots.keeps(inbox))
        _ = roots.select(.conversation, path: path("sub"))
        #expect(roots.keeps(agent))
        let back = roots.select(inbox, path: NavigationPath())
        #expect(back.move == .switchRoot && !back.animated && back.path == path("item"))
        #expect(!roots.keeps(inbox))
        #expect(roots.select(agent, path: NavigationPath()).path == path("sub"))
    }

    @Test func theRootInFrontPickedAgainGoesToItsRoot() {
        var roots = SidebarPageRoots()
        _ = roots.select(inbox, path: NavigationPath())
        let pop = roots.select(inbox, path: path("item"))
        #expect(pop.move == .popToRoot && pop.animated && pop.path.isEmpty && pop.closesOverlay)
        let none = roots.select(inbox, path: NavigationPath())
        #expect(none.move == .none && !none.animated && none.closesOverlay)
    }

    @Test func anotherConversationStartsAtItsRootWithoutAPop() {
        var roots = SidebarPageRoots()
        let step = roots.select(.conversation, path: path("sub"), contentChanged: true)
        #expect(step.move == .switchRoot && !step.animated && step.path.isEmpty)
        #expect(!roots.keeps(.conversation))
    }

    @Test func anotherAssistantForgetsEveryRoot() {
        var roots = SidebarPageRoots()
        _ = roots.select(inbox, path: NavigationPath())
        _ = roots.select(agent, path: path("a"))
        let step = roots.forget()
        #expect(step.root == .conversation && step.path.isEmpty && !step.animated)
        #expect(roots.current == .conversation && !roots.keeps(inbox))
    }

    /// Choosing the Agents tab forgets its roots each time (#10270): with nothing to forget the roots are not written
    /// again, so nothing that reads them is drawn again in the frame the tab switches in; with something, they go back.
    @Test func forgettingNothingWritesNothing() {
        let sidebar = UnifiedSidebar(part: .agents)
        #expect(SidebarPageRoots.start.forgotten)
        let observed = RootsObservationFlag()
        withObservationTracking { _ = sidebar.roots } onChange: { MainActor.assumeIsolated { observed.changed = true } }
        sidebar.forget(path: .constant(NavigationPath()))
        #expect(!observed.changed)
        sidebar.select(agent, path: .constant(NavigationPath()))
        #expect(observed.changed && !sidebar.roots.forgotten)
        sidebar.forget(path: .constant(NavigationPath()))
        #expect(sidebar.root == .conversation && sidebar.roots.forgotten)
    }

    @Test func theShadedPlaceFollowsTheRoot() {
        #expect(inbox.place == .inbox && recordings.place == .recordings)
        #expect(SidebarPageRoot.page(SidebarRoute.health).place == .health)
        #expect(agent.place == nil && SidebarPageRoot.conversation.place == nil)
    }
}

@MainActor private final class RootsObservationFlag { var changed = false }
