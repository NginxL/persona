#if os(iOS)
import SwiftUI

/// Settings › This Device › Alarms and Timers (#12805, docs/apple/phone-tools.md "Alarms and timers page"): every alarm and
/// timer Persona set through AlarmKit, which the Clock app doesn't list, so they can be seen before they ring. Delete on
/// each (confirmed), Pause and Resume on a timer. The list follows AlarmKit while the page is in front and stops when it
/// leaves. Opening it never asks for the permission; without it the page says why and its button asks (or opens Settings
/// once refused). iOS 26 and later, as the alarm tools. Who it serves: anyone who asked Persona for an alarm or a timer.
struct AlarmsPage: View {
    @Environment(AppModel.self) private var model
    @State private var page = AlarmsPageModel(deps: .forPage)
    /// The one Delete asks about.
    @State private var deleting: AlarmEntry?

    var body: some View {
        SettingsForm {
            content
        }
        .themedGroupedPage()
        .detailTitle(String(localized: "Alarms and Timers"))
        // While the page is in front: leaving it, its tab or the app stops listening; coming back reads again.
        .tabActiveTask(id: page.generation) { await page.follow() }
        .accessibleConfirmationDialog(deleting.map { String(localized: "Delete “\($0.title)”?") } ?? "",
                                      isPresented: Binding(get: { deleting != nil }, set: { if !$0 { deleting = nil } }),
                                      titleVisibility: .visible, hasCancel: true, presenting: deleting) { entry in
            Button(entry.isTimer ? String(localized: "Delete Timer") : String(localized: "Delete Alarm"), role: .destructive) {
                delete(entry)
            }
            Button(String(localized: "Cancel"), role: .cancel) {}
        } message: { entry in
            Text(entry.isTimer ? String(localized: "The timer stops and won't ring.") : String(localized: "This alarm won't ring again."))
        }
    }

    @ViewBuilder private var content: some View {
        if let access = page.access {
            if !access.isUsable {
                permission(asked: access != .notDetermined)
            } else if let entries = page.entries {
                if entries.isEmpty { empty } else { lists }
            }
        }
    }

    // MARK: States

    private var empty: some View {
        Section {
            EmptyStateView(symbol: Theme.Icon.alarmClock, title: String(localized: "No Alarms or Timers"),
                           message: String(localized: "Alarms and timers Persona sets for you show up here. Only Persona's are listed: the ones in the Clock app aren't."))
                .listRowBackground(Color.clear)
                .accessibilityIdentifier("alarms.empty")
        }
    }

    /// Without the permission: why, and the way to it (asked here when never asked, else Settings, as Settings › Meetings).
    private func permission(asked: Bool) -> some View {
        Section {
            EmptyStateView(symbol: Theme.Icon.alarmClock,
                           title: asked ? String(localized: "No Access to Alarms") : String(localized: "Allow Alarms"),
                           message: asked
                               ? String(localized: "Persona can't show or change its alarms and timers until you allow it to use alarms in Settings.")
                               : String(localized: "Allow Persona to use alarms, and the alarms and timers it sets for you show up here. They ring even in silent mode or Focus."),
                           action: asked
                               ? (String(localized: "Open Settings"), openSettings)
                               : (String(localized: "Allow Alarms"), ask),
                           tint: asked ? Theme.caution : nil)
                .listRowBackground(Color.clear)
                .accessibilityIdentifier(asked ? "alarms.denied" : "alarms.notAsked")
        }
    }

    @ViewBuilder private var lists: some View {
        if !page.timers.isEmpty {
            Section {
                ForEach(page.timers) { row($0) }
            } header: {
                Text("Timers [Alarms Page]")
            } footer: {
                if page.alarms.isEmpty { note }
            }
        }
        if !page.alarms.isEmpty {
            Section {
                ForEach(page.alarms) { row($0) }
            } header: {
                Text("Alarms [Alarms Page]")
            } footer: {
                note
            }
        }
    }

    private var note: some View {
        Text("Only alarms and timers Persona set are listed here; the ones in the Clock app aren't.")
    }

    private func row(_ entry: AlarmEntry) -> some View {
        AlarmsPageRow(entry: entry, pause: { setPaused(entry, true) }, resume: { setPaused(entry, false) })
            .swipeActions(edge: .trailing, allowsFullSwipe: false) {
                Button { deleting = entry } label: {
                    Label(String(localized: "Delete"), systemImage: Theme.Icon.delete)
                }
                .tint(Theme.danger)
            }
            .contextMenu {
                if entry.canPause {
                    Button { setPaused(entry, true) } label: { Label(String(localized: "Pause [Persona Alarm]"), systemImage: Theme.Icon.pause) }
                } else if entry.canResume {
                    Button { setPaused(entry, false) } label: { Label(String(localized: "Resume [Persona Alarm]"), systemImage: Theme.Icon.run) }
                }
                Button(role: .destructive) { deleting = entry } label: {
                    Label(String(localized: "Delete…"), systemImage: Theme.Icon.delete)
                }
            }
    }

    // MARK: Actions

    private func delete(_ entry: AlarmEntry) {
        if page.delete(entry) {
            Haptics.tap()
        } else {
            Haptics.error()
            model.toast = Toast(String(localized: "Couldn't delete “\(entry.title)”. Try again."), style: .error)
        }
    }

    private func setPaused(_ entry: AlarmEntry, _ paused: Bool) {
        if page.setPaused(entry, paused) {
            Haptics.tap()
        } else {
            Haptics.error()
            model.toast = Toast(paused ? String(localized: "Couldn't pause “\(entry.title)”. Try again.")
                                       : String(localized: "Couldn't resume “\(entry.title)”. Try again."), style: .error)
        }
    }

    /// Asked only from the button: the system shows its prompt once.
    private func ask() {
        Task { await page.requestAccess() }
    }

    private func openSettings() {
        if let url = URL(string: UIApplication.openSettingsURLString) { UIApplication.shared.open(url) }
    }
}

/// One alarm or timer: its time (a timer's time left, counting live), its state, its name and how it repeats or how long
/// it is; a counting or paused timer has Pause or Resume beside it.
private struct AlarmsPageRow: View {
    let entry: AlarmEntry
    var pause: () -> Void
    var resume: () -> Void

    var body: some View {
        HStack(spacing: Theme.Space.m) {
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) {
                    digits
                        .font(Theme.Font.title)
                        .monospacedDigit()
                        .lineLimit(1)
                    Chip(text: entry.stateText, look: look, compact: true)
                }
                Text(entry.title).font(Theme.Font.body).lineLimit(1)
                Text(entry.detail()).font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(1)
            }
            .accessibilityElement(children: .combine)
            Spacer(minLength: Theme.Space.s)
            if entry.canPause {
                control(String(localized: "Pause [Persona Alarm]"), Theme.Icon.pause, pause)
            } else if entry.canResume {
                control(String(localized: "Resume [Persona Alarm]"), Theme.Icon.run, resume)
            }
        }
        .padding(.vertical, Theme.Space.xxs)
        .accessibilityIdentifier(entry.isTimer ? "alarms.timer" : "alarms.alarm")
    }

    @ViewBuilder private var digits: some View {
        if let time = entry.timeText() {
            Text(time)
        } else {
            switch entry.face {
            case .counting(let ends)?:
                // Counts itself down, no timer of the page's (P9).
                Text(timerInterval: ends.addingTimeInterval(-(entry.length ?? Self.longest))...ends, countsDown: true)
            case .still(let left)?:
                Text(AlarmEntry.clockText(left))
            case nil:
                EmptyView()
            }
        }
    }

    /// The longest timer the tools set (24 h): the countdown's range when its length is not known.
    private static let longest: TimeInterval = 86_400

    private var look: Chip.Look {
        switch entry.state {
        case .scheduled, .paused: .neutral
        case .countdown: .tint(Theme.accent)
        case .alerting: .review(.caution)
        }
    }

    private func control(_ title: String, _ symbol: String, _ run: @escaping () -> Void) -> some View {
        Button(action: run) {
            Label { Text(title).lineLimit(1) } icon: { ThemedIcon(symbol) }
                .labelStyle(.iconOnly)
        }
        .themedButton()
        .buttonBorderShape(.circle)
        .controlSize(.large) // a 44 pt target at least (DESIGN_SYSTEM 6.3)
        .accessibilityIdentifier(entry.canPause ? "alarms.pause" : "alarms.resume")
    }
}
#endif
