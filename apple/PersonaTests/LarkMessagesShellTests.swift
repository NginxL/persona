import SwiftUI
import Testing
@testable import Persona

@MainActor struct LarkMessagesShellTests {
    @Test func messagesLaunchBuildsOnlyItsPageAndKeepsSettingsOnIt() {
        let model = AppModel()
        model.selectedTab = .feishu
        let shell = ShellState(page: 2, pageCount: 3)
        shell.attach(to: model)
        defer { shell.detach(from: model) }
        #expect(shell.page == 2 && shell.pager.page == 2)
        #expect(shell.feishuBuilt && !shell.assistantBuilt && !shell.agentsBuilt)
        shell.feishuAtRoot = false
        #expect(!shell.atRoot)
        model.selectedTab = .settings
        #expect(model.shellSettings && model.selectedTab == .feishu && shell.page == 2)
        model.selectedTab = .chats
        #expect(shell.page == 1 && shell.atRoot)
        model.selectedTab = .feishu
        #expect(shell.page == 2 && !shell.atRoot)
    }

    @Test func disablingMessagesReturnsToAssistantAndRejectsStaleRoutes() {
        let model = AppModel()
        model.selectedTab = .feishu
        let shell = ShellState(page: 2, pageCount: 3)
        shell.attach(to: model)
        defer { shell.detach(from: model) }
        shell.setPageCount(2)
        #expect(shell.page == 0 && shell.pager.page == 0 && model.selectedTab == .assistant)
        model.selectedTab = .feishu
        #expect(shell.page == 0 && model.selectedTab == .assistant)
        shell.setPageCount(3)
        model.selectedTab = .feishu
        #expect(shell.page == 2 && model.selectedTab == .feishu)
    }

    @Test func thirdPageCanReturnToEitherNeighbor() {
        let pager = ShellPager()
        pager.setPageCount(3)
        pager.width = 400
        var selected: [Int] = []
        pager.settled = { selected.append($0) }
        pager.slide(to: 1, animated: false)
        pager.dragBegan()
        pager.drag(-280)
        pager.letGo(velocity: -500)
        #expect(pager.page == 2)
        pager.dragBegan()
        pager.drag(280)
        pager.letGo(velocity: 500)
        #expect(pager.page == 1)
        pager.dragBegan()
        pager.drag(280)
        pager.letGo(velocity: 500)
        #expect(pager.page == 0)
        #expect(selected == [2, 1, 0])
    }

    @Test func reversingAPartialMiddlePageDragDoesNotSkipToTheOtherNeighbor() {
        let pager = ShellPager()
        pager.setPageCount(3)
        pager.width = 400
        pager.slide(to: 1, animated: false)
        pager.dragBegan()
        pager.drag(-160)
        pager.letGo(velocity: 500)
        #expect(pager.page == 1)
        pager.dragBegan()
        pager.drag(160)
        pager.letGo(velocity: -500)
        #expect(pager.page == 1)
    }

    @Test func removingIntegrationDoesNotLeaveAnInvisiblePage() {
        let pager = ShellPager()
        pager.setPageCount(3)
        pager.slide(to: 2, animated: false)
        pager.setPageCount(2)
        #expect(pager.position == 1)
        #expect(!pager.moving)
        pager.slide(to: 2, animated: false)
        #expect(pager.page == 1)
    }

    @Test func cancelledDragAndLastPageOverscrollStayOnExistingPages() {
        let pager = ShellPager()
        pager.setPageCount(3)
        pager.width = 400
        pager.slide(to: 2, animated: false)
        pager.dragBegan()
        pager.drag(-250)
        pager.letGo(velocity: -500)
        #expect(pager.position == 2)
        pager.dragBegan()
        pager.drag(250)
        pager.cancel()
        #expect(pager.position == 2)
    }
}
