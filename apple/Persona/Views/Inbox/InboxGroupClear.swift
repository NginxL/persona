import Foundation

/// A group title's pull-down in a section with a section-wide action (#10422; docs/assistant/inbox.md "Group titles"):
/// FYI's Mark All Seen and Collapsed's Clear All, for the group's items or for the whole section's. The group titles
/// stick while their rows scroll by, so the section's own button is often out of sight; this one is where you are.
/// Needs You and In Progress have none: their items need the owner (or the AI is on them), and a batch there is how
/// things get missed. Pure, so its words and counts are tested without a list.
nonisolated struct InboxGroupClear: Equatable {
    struct Choice: Equatable, Identifiable {
        /// "group" or "section".
        var id: String
        var title: String
        var items: [InboxItem]
    }

    /// The button's words: the section's own action (Mark All Seen, Clear All).
    var label: String
    /// The group's items, then the whole section's.
    var choices: [Choice]
    /// With a filter or a search on, what "all" covers, in Clean up all's words ("Lark · @Me", "Search “report”"): the
    /// menu says it under each choice. Nil for the whole Inbox.
    var scope: String?

    /// The pull-down of `group` in `tier`, whose items under the filters and search are `section`; nil where the section
    /// has no section-wide action, or where the group is the whole section (its title is then the section's, and the
    /// section's own button acts at once).
    static func make(_ tier: InboxItem.Tier, group: InboxGroups.Group, section: [InboxItem], filters: [String], query: String) -> InboxGroupClear? {
        guard let label = InboxPage.clearTitle(tier), !group.items.isEmpty, section.count > group.items.count,
              let mine = title(tier, count: group.items.count, in: group.title),
              let all = title(tier, count: section.count, in: tier.title) else { return nil }
        let searching = !query.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
        return InboxGroupClear(label: label,
                               choices: [Choice(id: "group", title: mine, items: group.items), Choice(id: "section", title: all, items: section)],
                               scope: filters.contains { !$0.isEmpty } || searching ? InboxBulk.scope(filters: filters, query: query) : nil)
    }

    /// A choice's words: the section's verb, how many and where ("Mark All 4 in “Assistant” Seen").
    static func title(_ tier: InboxItem.Tier, count: Int, in name: String) -> String? {
        switch tier {
        case .action, .working: nil
        case .fyi: String(localized: "Mark All \(count) in “\(name)” Seen")
        case .noise: String(localized: "Clear All \(count) in “\(name)”")
        }
    }
}
