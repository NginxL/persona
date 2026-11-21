import SwiftUI
import PersonaWire

/// The subscription accounts the hubs know (docs/server/usage-windows.md), shared by Machines' Subscription Usage card on the phone and the
/// Mac's bars at the foot of the sections column. Empty until a hub answers, and when none has any (nothing is drawn).
@Observable
final class UsageWindowsStore {
    static let shared = UsageWindowsStore()

    private(set) var accounts: [UsageAccount] = []
    /// When the last load ended (any answer or none), for `loadIfOld`.
    @ObservationIgnored private var loadedAt: Date?

    /// Every group's accounts (each hub merges its own machines' and gateways'), every group at once (#4705). A group
    /// that fails, or is not asked because its link is down, keeps its last answer; one before this route answers none.
    func load(_ model: AppModel) async {
        let multi = model.isMultiHub
        let ids = model.groupIDs
        let apis = Dictionary(ids.compactMap { g in model.api(g).map { (g, $0) } }, uniquingKeysWith: { a, _ in a })
        let asked = HubFanOut.asked(ids.filter { apis[$0] != nil }) { model.socket($0)?.looksConnected == true }
        var answers: [String: [UsageAccount]] = [:]
        var cancelled = false
        await HubFanOut.run(asked, fetch: { g in try await apis[g]!.usageWindows() }) { g, result in
            switch result {
            case .success(let list): answers[g] = multi ? list.map { var a = $0; a.id = g + "|" + a.id; return a } : list
            case .failure(let error): if error.isCancellation { cancelled = true }
            }
        }
        if cancelled || Task.isCancelled { return }
        loadedAt = .now
        // In the groups' order, as before; a group without an answer keeps what it had.
        let all = ids.flatMap { g in answers[g] ?? accounts.filter { !multi || $0.id.hasPrefix(g + "|") } }
        if all != accounts { accounts = all }
    }

    /// One load when the last is older than `maxAge` (or there was none): for a line that shows the numbers in passing
    /// (ultracode's quota under its switch), not a page that keeps them current (`poll`).
    func loadIfOld(_ model: AppModel, maxAge: TimeInterval = 120) async {
        guard Self.isOld(loadedAt, maxAge: maxAge) else { return }
        await load(model)
    }

    /// `loadIfOld` until cancelled, looking every poll interval: for a page that shows the numbers in passing and can
    /// stay open a long time (a chat whose menus carry ultracode's quota), run only while it is in front. It loads
    /// nothing more while Machines or the Mac's bars already poll.
    func keepFresh(_ model: AppModel, maxAge: TimeInterval = 120) async {
        while !Task.isCancelled {
            await loadIfOld(model, maxAge: maxAge)
            try? await Task.sleep(for: UsageWindows.pollInterval)
        }
    }

    /// A last load at `loadedAt` is `maxAge` old or more (or there was none).
    nonisolated static func isOld(_ loadedAt: Date?, maxAge: TimeInterval, now: Date = .now) -> Bool {
        guard let loadedAt else { return true }
        return now.timeIntervalSince(loadedAt) >= maxAge
    }

    /// One group's accounts: all of them with one group of hubs, else the ones `load` marked as that group's.
    func accounts(group: String, multi: Bool) -> [UsageAccount] {
        multi ? accounts.filter { $0.id.hasPrefix(group + "|") } : accounts
    }

    /// Asks again every minute until cancelled (the caller runs it only while it is on screen and the app in front).
    func poll(_ model: AppModel) async {
        while !Task.isCancelled {
            await load(model)
            try? await Task.sleep(for: UsageWindows.pollInterval)
        }
    }
}

/// A bar: the track, the used part (caution colour from 80%) and, with `expected`, a tick where an even pace would be.
struct UsageMeter: View {
    var used: Double
    var expected: Double?
    var tall = false

    var body: some View {
        let height = tall ? Theme.Size.barWide : Theme.Size.bar
        GeometryReader { geo in
            ZStack(alignment: .leading) {
                Capsule().fill(Theme.Fill.chip)
                Capsule().fill(UsageWindows.isWarn(used) ? Theme.caution : Theme.accent)
                    .frame(width: geo.size.width * UsageWindows.fraction(used))
                if let expected {
                    Capsule().fill(Color.secondary)
                        .frame(width: Theme.Size.rule, height: height + Theme.Space.xs)
                        .offset(x: geo.size.width * UsageWindows.fraction(expected) - Theme.Size.rule / 2)
                }
            }
        }
        .frame(height: height)
        .animation(Theme.Motion.state, value: used)
        .accessibilityHidden(true)
    }
}

/// One account, window by window: its name and the sample's age, plan and sources, what the gateway could not read,
/// then a row per window (length, used, pace, reset, bar).
struct UsageAccountBlock: View {
    var account: UsageAccount
    var now: Date

    var body: some View {
        let a = account
        let from = UsageWindows.sourceNames(a)
        let plan = UsageWindows.planNote(a)
        let problem = UsageWindows.problemText(a)
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) {
                    Text(a.name).font(Theme.Font.secondaryStrong).foregroundStyle(Color.primary).lineLimit(1)
                    Spacer(minLength: Theme.Space.s)
                    Text(UsageWindows.ageLabel(a, now: now)).font(Theme.Font.label.monospacedDigit())
                        .foregroundStyle(Color.secondary).lineLimit(1)
                }
                let note = ([plan] + [from.joined(separator: " · ")]).filter { !$0.isEmpty }.joined(separator: " · ")
                if !note.isEmpty {
                    Text(note).font(Theme.Font.label).foregroundStyle(Color.secondary).lineLimit(1)
                        .accessibilityLabel(from.isEmpty ? note : Self.sourceLabel(plan: plan, from: from))
                }
                if !problem.isEmpty {
                    Text(problem).font(Theme.Font.label).foregroundStyle(Theme.cautionText).fixedSize(horizontal: false, vertical: true)
                }
            }
            ForEach(a.windows, id: \.key) { w in UsageWindowRow(name: a.name, window: w, stale: a.stale, now: now) }
        }
        .opacity(a.stale ? 0.6 : 1)
        .accessibilityElement(children: .contain)
    }

    /// The note for VoiceOver: the plan, then where the numbers came from, as one sentence.
    static func sourceLabel(plan: String, from: [String]) -> String {
        let names = from.joined(separator: "、")
        return plan.isEmpty ? String(localized: "Source: \(names)") : String(localized: "\(plan), source: \(names)")
    }
}

/// A window: 5h / 7d (and the model it is for), used, the pace when it is off, the reset, a bar with the pace's tick.
struct UsageWindowRow: View {
    var name: String
    var window: UsageWindow
    /// The account's reading is out of date: no pace is judged from it (#12411).
    var stale = false
    var now: Date

    var body: some View {
        let w = window
        let pace = UsageWindows.pace(w, stale: stale, now: now)
        let used = UsageWindows.percentText(w.usedPercent)
        let reset = UsageWindows.resetLabel(w.resetsAt, now: now)
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            if let m = w.model {
                Text(m).font(Theme.Font.label).foregroundStyle(Color.secondary).lineLimit(1)
            }
            HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) {
                Text(w.label).font(Theme.Font.label).foregroundStyle(Color.secondary).lineLimit(1)
                    .scaledFrame(width: Theme.Size.wellXS, alignment: .leading)
                Text(used).font(Theme.Font.labelStrong.monospacedDigit())
                    .foregroundStyle(UsageWindows.isWarn(w.usedPercent) ? Theme.cautionText : Color.primary).lineLimit(1)
                if let pace, pace.state != .on {
                    Text(UsageWindows.paceLabel(pace.state)).font(Theme.Font.label)
                        .foregroundStyle(pace.state == .over ? Theme.cautionText : Theme.positive).lineLimit(1)
                }
                Spacer(minLength: Theme.Space.s)
                Text(reset).font(Theme.Font.label.monospacedDigit()).foregroundStyle(.tertiary).lineLimit(1)
            }
            UsageMeter(used: w.usedPercent, expected: pace?.expected, tall: true)
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel([name, w.label, w.model, String(localized: "\(used) used"), pace.flatMap { $0.state == .on ? nil : UsageWindows.paceLabel($0.state) }, reset]
            .compactMap { $0 }.filter { !$0.isEmpty }.joined(separator: String(localized: ", ", comment: "Separator between the parts of a VoiceOver label")))
    }
}

/// Every live account, one after another, divided; the ages and countdowns tick each second while it is on screen.
struct UsageAccountsList: View {
    var accounts: [UsageAccount]

    var body: some View {
        TimelineView(.periodic(from: .now, by: 1)) { ctx in
            let live = UsageWindows.live(accounts, now: ctx.date)
            VStack(alignment: .leading, spacing: Theme.Space.m) {
                ForEach(Array(live.enumerated()), id: \.element.id) { i, a in
                    if i > 0 { Divider() }
                    UsageAccountBlock(account: a, now: ctx.date)
                }
            }
        }
    }
}

#if !os(macOS)
/// Machines' Subscription Usage (docs/server/usage-windows.md「UI」): the accounts' 5h / 7d windows under the machines' cards. Not drawn while
/// the hubs have none. The Mac has the same at the foot of its sections column (`MacUsageFooter`).
struct UsageWindowsCard: View {
    var store = UsageWindowsStore.shared

    var body: some View {
        let live = UsageWindows.live(store.accounts)
        if !live.isEmpty {
            VStack(alignment: .leading, spacing: Theme.Space.s) {
                Text("Subscription Usage").font(Theme.Font.sectionHeader).foregroundStyle(.secondary).lineLimit(1)
                    .padding(.horizontal, Theme.Space.xs)
                    .accessibilityAddTraits(.isHeader)
                UsageAccountsList(accounts: store.accounts)
                    .card()
            }
            .padding(.top, Theme.Space.s)
            .accessibilityIdentifier("machines.usage")
        }
    }
}
#endif
