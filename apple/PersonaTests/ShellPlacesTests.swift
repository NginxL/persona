import SwiftUI
import Testing
@testable import Persona

/// The sidebar's places keep their stacks (docs/design/sidebar-navigation.md, #6599).
@MainActor struct ShellPlacesTests {
    private func path(_ items: [ShellRoute], detail: String? = nil) -> NavigationPath {
        var p = NavigationPath(items)
        if let detail { p.append(MachineDetailRoute(id: detail)) }
        return p
    }

    @Test func aPlaceLeftForAnotherComesBackAsItWas() {
        var places = ShellPlaces()
        #expect(places.enter(.machines, from: NavigationPath()) == nil)
        let deep = path([.machines], detail: "macbook")
        #expect(places.enter(.knowledge, from: deep) == nil)
        #expect(places.current == .knowledge && places.keeps(.machines))
        #expect(places.enter(.machines, from: path([.knowledge])) == deep)
        #expect(places.current == .machines && !places.keeps(.machines) && places.keeps(.knowledge))
    }

    @Test func choosingThePlaceInFrontGoesToItsRoot() {
        var places = ShellPlaces()
        _ = places.enter(.inbox, from: NavigationPath())
        #expect(places.enter(.inbox, from: path([.inbox], detail: "x")) == nil)
        #expect(places.current == .inbox && !places.keeps(.inbox))
    }

    @Test func aConversationOrNewChatKeepsThePlaceLeft() {
        var places = ShellPlaces()
        _ = places.enter(.machines, from: NavigationPath())
        places.leave(with: path([.machines], detail: "a"))
        #expect(places.current == nil && places.keeps(.machines))
        _ = places.enter(.machines, from: NavigationPath())
        #expect(places.enter(.newConversation, from: path([.machines], detail: "b")) == nil)
        #expect(places.current == nil)
        #expect(places.enter(.machines, from: NavigationPath()) == path([.machines], detail: "b"))
    }

    @Test func backingOutStartsThePlaceAtItsRoot() {
        var places = ShellPlaces()
        _ = places.enter(.todos, from: NavigationPath())
        places.backedOut()
        places.leave(with: NavigationPath())
        #expect(!places.keeps(.todos))
        #expect(places.enter(.todos, from: NavigationPath()) == nil)
    }

    @Test func anotherAssistantForgetsEveryPlace() {
        var places = ShellPlaces()
        _ = places.enter(.machines, from: NavigationPath())
        _ = places.enter(.inbox, from: path([.machines], detail: "a"))
        places.forget()
        #expect(places.current == nil && !places.keeps(.machines))
    }

    /// A To-dos place picked in the drawer (#10465): To-dos opens on it at its root, the to-do it kept being the place
    /// before's; To-dos left and chosen again with no other place keeps its stack as every place does.
    @Test func aToDosPlacePickedStartsToDosAtItsRoot() {
        var places = ShellPlaces()
        _ = places.enter(.todos, from: NavigationPath())
        _ = places.enter(.inbox, from: path([.todos], detail: "kyoto"))
        #expect(places.keeps(.todos))
        #expect(places.enter(.todos, from: path([.inbox]), fresh: true) == nil)
        #expect(places.current == .todos && !places.keeps(.todos))
        #expect(places.enter(.todos, from: path([.todos], detail: "x"), fresh: true) == nil, "in front: its root")
        _ = places.enter(.inbox, from: path([.todos], detail: "y"))
        #expect(places.enter(.todos, from: path([.inbox])) == path([.todos], detail: "y"))
    }

    @Test func healthAndNewChatAreNotPlaces() {
        #expect(!ShellPlaces.isPlace(.health) && !ShellPlaces.isPlace(.newConversation))
        #expect(ShellPlaces.isPlace(.inbox) && ShellPlaces.isPlace(.knowledge) && ShellPlaces.isPlace(.recordings))
    }
}
