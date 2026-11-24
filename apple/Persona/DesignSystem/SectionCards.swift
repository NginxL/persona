import SwiftUI

extension View {
    /// A list sectioned by state, each section one card (DESIGN_SYSTEM 3.4 "To-dos list": lists sectioned by state wear
    /// section cards, streams stay plain): the system's inset grouped sections, the Agents list's container, so the
    /// system draws the cards and every shape a row takes in use (a swipe, Select, a long press) in each OS's own form.
    /// Rows keep the system's cells, never `themedRow()`, whose clear background would erase the cards in Clay, Island
    /// and Lark; the page behind is `themedPage()`'s. One place to choose the container, so a style that draws its own
    /// (BeautifulUI's card per row, with the To-dos switch-on) changes it here, through `StyleSpec`, not in a page.
    func sectionCards() -> some View {
        listStyle(.insetGrouped)
    }
}
