#if os(iOS)
import Foundation
import Testing
@testable import Persona

/// The shell's one source of the page in front (docs/apple/shell.md): `select` and `route` change it at once and the strip
/// and the app's tab follow; the strip's `slide` is idempotent and a slide not started yet is replaced. And the status
/// row (`ShellNotices`): only the link's state (the toasts float), waiting for a finger unless urgent.
@MainActor
struct ShellStateTests {
    @Test func selectMovesThePageTheStripAndTheTab() {
        let model = AppModel()
        model.selectedTab = .assistant
        let shell = ShellState()
        shell.attach(to: model)
        defer { shell.detach(from: model) }
        shell.select(1)
        #expect(shell.page == 1)
        #expect(model.selectedTab == .chats)
        #expect(shell.agentsBuilt)
        shell.select(0)
        #expect(shell.page == 0)
        #expect(model.selectedTab == .assistant)
    }

    @Test func aTabSetAnywhereIsRoutedAsItIsSet() {
        let model = AppModel()
        model.selectedTab = .assistant
        let shell = ShellState()
        shell.attach(to: model)
        defer { shell.detach(from: model) }
        model.selectedTab = .chats // a notification, Hand to Agent…
        #expect(shell.page == 1, "the shell did not take the tab as it was set")
        model.selectedTab = .mail // the inbox lands on Assistant
        #expect(shell.page == 0)
        #expect(model.selectedTab == .assistant)
        #expect(shell.request?.page == .inbox)
        shell.select(1)
        model.selectedTab = .settings // Settings is a sheet over the page in front
        #expect(model.shellSettings)
        #expect(shell.page == 1)
        #expect(model.selectedTab == .chats)
    }

    /// A tap on a push or a Live Activity row while the Settings sheet is up: the sheet goes and the chat shows (#3818).
    @Test func openingASessionClosesTheSettingsSheet() {
        let model = AppModel()
        model.selectedTab = .assistant
        let shell = ShellState()
        shell.attach(to: model)
        defer { shell.detach(from: model) }
        model.selectedTab = .settings
        #expect(model.shellSettings)
        let key = SessionKey(machineID: "m", sessionID: "s")
        model.open(key)
        #expect(!model.shellSettings)
        #expect(shell.page == 1 && model.selectedTab == .chats)
        #expect(model.openChat == key)
    }

    /// A tap on an Agent's notification while the drawer is out: the drawer goes, or it would cover the chat (#3114).
    @Test func openingAnAgentsChatClosesTheDrawer() {
        let model = AppModel()
        model.selectedTab = .assistant
        let shell = ShellState()
        shell.attach(to: model)
        defer { shell.detach(from: model) }
        shell.drawer.progress = 1
        #expect(shell.drawer.isOpen)
        let key = SessionKey(machineID: "m", sessionID: "s")
        model.open(key)
        #expect(!shell.drawer.isOpen)
        #expect(shell.page == 1 && model.openChat == key)
    }

    @Test func aDetachedShellNoLongerRoutes() {
        let model = AppModel()
        let shell = ShellState()
        shell.attach(to: model)
        shell.detach(from: model)
        let page = shell.page
        model.selectedTab = page == 0 ? .chats : .assistant
        #expect(shell.page == page)
    }

    @Test func onlyThePageInFrontIsBuiltAtLaunch() {
        let onAgents = ShellState(page: 1)
        #expect(onAgents.page == 1 && onAgents.pager.page == 1)
        #expect(onAgents.agentsBuilt && !onAgents.assistantBuilt, "助手 built behind Agents at launch")
        onAgents.select(0)
        #expect(onAgents.assistantBuilt)
        let onAssistant = ShellState()
        #expect(onAssistant.assistantBuilt && !onAssistant.agentsBuilt)
        let dragged = ShellState(page: 1)
        dragged.pager.dragBegan() // the page behind comes in under the finger
        #expect(dragged.assistantBuilt)
    }

    @Test func aSlideNotStartedYetIsReplaced() {
        let pager = ShellPager()
        pager.slide(to: 1) // starts on the next turn
        #expect(pager.moving)
        pager.slide(to: 0) // tapped back before it started: nothing to slide
        #expect(!pager.moving)
        #expect(pager.position == 0)
    }

    @Test func theSameSlideTwiceIsOne() {
        let pager = ShellPager()
        pager.slide(to: 1, animated: false)
        #expect(pager.position == 1)
        pager.slide(to: 1)
        #expect(!pager.moving, "a slide to where the strip rests started moving")
    }

    @Test func theStripComingToRestUnderAFingerKeepsTheMovingBar() {
        let pager = ShellPager()
        pager.fingers(down: true)
        pager.slide(to: 1)
        pager.slide(to: 0) // at rest again at once, the finger still down
        #expect(!pager.moving)
        #expect(pager.lingering, "the moving bar went from under the finger")
        pager.fingers(down: false)
        #expect(!pager.lingering, "the moving bar stayed after the finger let go")
        pager.slide(to: 1)
        pager.slide(to: 0)
        #expect(!pager.lingering, "the moving bar stayed with no finger down")
    }

    @Test func recoveryAtTheStartingPageKeepsTheMovingBarUntilLetGo() {
        let pager = ShellPager()
        pager.fingers(down: true)
        pager.dragBegan()
        pager.fingers(down: false)
        pager.fingers(down: true)
        #expect(!pager.moving)
        #expect(pager.lingering, "cancelling at the drag's start removed the switch under the new finger")
        pager.fingers(down: false)
        #expect(!pager.lingering)
    }

    @Test func anInterruptedDragSettlesBackWithoutCancellingAnOrdinaryRelease() async {
        let pager = ShellPager()
        pager.width = 402
        pager.fingers(down: true)
        pager.dragBegan()
        pager.drag(-120)
        #expect(pager.moving)
        pager.fingers(down: false)
        pager.fingers(down: true) // the next tap arrives while the abandoned drag is still held
        pager.fingers(down: false)
        let deadline = Date().addingTimeInterval(2)
        while pager.moving && Date() < deadline { try? await Task.sleep(for: .milliseconds(5)) }
        #expect(!pager.moving && pager.position == 0)

        pager.fingers(down: true)
        pager.dragBegan()
        pager.drag(-240)
        pager.letGo(velocity: 0)
        pager.fingers(down: false)
        #expect(pager.position == 1, "an ordinary release keeps its own target")
        let released = Date().addingTimeInterval(2)
        while pager.moving && Date() < released { try? await Task.sleep(for: .milliseconds(5)) }
        #expect(!pager.moving && pager.position == 1)
    }

    @Test func aLateCancelDoesNotUndoANewerSlide() {
        let pager = ShellPager()
        pager.dragBegan()
        var selections: [Int] = []
        pager.settled = { selections.append($0) }
        pager.slide(to: 1, animated: false)
        pager.cancel()
        #expect(selections.isEmpty)
        #expect(pager.position == 1 && !pager.moving)
    }

    /// A toast is the result of something just done: it floats (`ToastWindow`) and brings no link state (#1053).
    @Test func aToastLeavesTheRowAlone() {
        let model = AppModel()
        let shell = ShellState()
        shell.attach(to: model)
        defer { shell.detach(from: model) }
        let toast = Toast("已归档", style: .success)
        model.toast = toast
        #expect(model.toast == toast, "the toast is the model's to show, not taken into the row")
        #expect(shell.notices.shown == nil && shell.notices.opened == 0)
    }

    /// The link's state floats and moves nothing (#413), so it shows and goes at once, a finger down or not.
    @Test func theLinkShowsAndGoesAtOnce() {
        let n = ShellNotices()
        n.linkChanged(.connecting)
        #expect(n.shown == .link(.connecting) && n.opened == 1)
        n.linkChanged(.waitingForNetwork)
        #expect(n.shown == .link(.waitingForNetwork) && n.opened == 1, "one state to another is not a new opening")
        n.linkChanged(.online)
        #expect(n.shown == nil && n.folded == 1)
        n.linkChanged(.online)
        #expect(n.folded == 1)
    }

    /// A title in front that says the link itself (an agent's chat, Inbox, Machines) keeps the pill away: Connecting…
    /// showed twice, in the chat's title and in the pill just under it.
    @Test func aTitleSayingTheLinkKeepsThePillAway() {
        let n = ShellNotices()
        n.linkChanged(.connecting)
        #expect(n.pill == .connecting)
        n.titleShowsLink(true)
        #expect(n.pill == nil, "the pill said it again under the chat's title")
        #expect(n.shown == .link(.connecting), "the link's state itself is unchanged")
        n.titleShowsLink(true)
        n.titleShowsLink(false)
        #expect(n.pill == nil, "another title in front still says it")
        n.titleShowsLink(false)
        #expect(n.pill == .connecting, "back on a page whose title does not say it: the pill again")
        n.titleShowsLink(false)
        n.titleShowsLink(true)
        #expect(n.pill == nil, "an extra goodbye does not leave the count below zero")
        n.titleShowsLink(false)
        n.linkChanged(.online)
        #expect(n.pill == nil)
    }
}
#endif
