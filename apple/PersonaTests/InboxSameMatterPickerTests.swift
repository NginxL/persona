import Foundation
import Testing
@testable import Persona

/// Same matter as… offers only eligible open matters, ordered and searched without touching view layout.
struct InboxSameMatterPickerTests {
    private func item(_ id: String, manager: String? = "manager-a", title: String? = nil) -> InboxItem {
        var item = InboxItem(id: id, source: "example", kind: "group", account: "example", accountName: "Example",
                             title: title ?? id, preview: "", count: 1)
        item.manager = manager
        return item
    }

    private func candidates(_ items: [InboxItem], current: InboxItem, children: [InboxItem] = [], query: String = "") -> [String] {
        InboxSameMatter.candidates(items: items, item: current, children: children, query: query).map(\.id)
    }

    @Test func candidatesKeepOnlyOpenItems() {
        let current = item("current")
        let open = item("open")
        var done = item("done")
        done.doneAt = Date(timeIntervalSince1970: 10)
        #expect(candidates([current, open, done], current: current) == ["open"])
    }

    @Test func candidatesExcludeSelfAndDirectChildren() {
        let current = item("current")
        let child = item("child")
        let other = item("other")
        #expect(candidates([other, child, current], current: current, children: [child]) == ["other"])
    }

    @Test func candidatesStayWithinTheCurrentManager() {
        let current = item("current", manager: "manager-a")
        let same = item("same", manager: "manager-a")
        let other = item("other", manager: "manager-b")
        let unowned = item("unowned", manager: nil)
        #expect(candidates([same, other, unowned], current: current) == ["same"])
    }

    @Test func aStampedThreadItemSelectsTheSameMergedListManager() {
        let currentInList = item("current", manager: "manager-a")
        let same = item("same", manager: "manager-a")
        let defaultManager = item("default", manager: nil)
        let unstamped = InboxTree(node: InboxParent(kind: "inbox", id: currentInList.id),
                                  item: item("current", manager: nil))
        let tree = InboxLine.stamping(unstamped, manager: currentInList.manager)
        let opened = tree.item ?? currentInList
        #expect(candidates([currentInList, same, defaultManager], current: opened, children: tree.children) == ["same"])
    }

    @Test func candidatesSortNewestFirstWithDateFallbacksAndIDTieBreak() {
        let current = item("current")
        var updated = item("newest")
        updated.updatedAt = Date(timeIntervalSince1970: 300)
        updated.startedAt = Date(timeIntervalSince1970: 1)

        var started = item("started")
        started.startedAt = Date(timeIntervalSince1970: 200)

        var older = item("older")
        older.updatedAt = Date(timeIntervalSince1970: 100)
        older.startedAt = Date(timeIntervalSince1970: 999) // `updatedAt` takes precedence over a newer start.

        let a = item("a")
        let b = item("b")
        #expect(candidates([b, older, a, started, updated], current: current) == ["newest", "started", "older", "a", "b"])
    }

    @Test func candidatesSearchTitlesCaseInsensitively() {
        let current = item("current")
        let match = item("match", title: "Quarterly PLAN")
        let other = item("other", title: "Something else")
        #expect(candidates([match, other], current: current, query: "plan") == ["match"])
        #expect(candidates([match, other], current: current, query: "  plan\n") == ["match"])
        #expect(candidates([match, other], current: current, query: "   ") == ["match", "other"])
    }

    @Test func candidatesSearchPlacesCaseInsensitively() {
        let current = item("current")
        var match = item("match", title: "One")
        match.`where` = "Project ROOM"
        let other = item("other", title: "Two")
        #expect(candidates([match, other], current: current, query: "room") == ["match"])
    }

    @Test func candidatesDoNotSearchSenderOrPreview() {
        let current = item("current")
        var sender = item("sender")
        sender.sender = "Needle"
        var preview = item("preview")
        preview.preview = "Needle"
        #expect(candidates([sender, preview], current: current, query: "needle").isEmpty)
    }

    @Test func locationLineJoinsDifferentPlaceAndSender() {
        var candidate = item("item")
        candidate.`where` = "Project room"
        candidate.sender = "Alex"
        candidate.preview = "Latest text is not a location"
        #expect(InboxSameMatter.locationLine(candidate) == "Project room · Alex")
    }

    @Test func locationLineSaysAnEqualPlaceAndSenderOnce() {
        var candidate = item("item")
        candidate.`where` = "Alex"
        candidate.sender = "Alex"
        #expect(InboxSameMatter.locationLine(candidate) == "Alex")
    }

    @Test func locationLineFallsBackToTheOneNonemptyValue() {
        var place = item("place")
        place.`where` = "Project room"
        place.sender = ""
        #expect(InboxSameMatter.locationLine(place) == "Project room")

        var sender = item("sender")
        sender.`where` = ""
        sender.sender = "Alex"
        #expect(InboxSameMatter.locationLine(sender) == "Alex")
    }

    @Test func locationLineIsNilWithoutPlaceOrSender() {
        var candidate = item("item")
        candidate.preview = "Latest text is never a fallback"
        #expect(InboxSameMatter.locationLine(candidate) == nil)
        candidate.`where` = ""
        candidate.sender = ""
        #expect(InboxSameMatter.locationLine(candidate) == nil)
        candidate.`where` = "  "
        candidate.sender = "\n"
        #expect(InboxSameMatter.locationLine(candidate) == nil)
    }
}
