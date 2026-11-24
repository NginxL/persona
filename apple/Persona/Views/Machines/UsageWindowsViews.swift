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
        #if DEBUG
        // Simulator screenshots: made-up accounts, no hub needed.
        if ProcessInfo.processInfo.arguments.contains("-showUsageCardsDemo") {
            accounts = Self.demoAccounts()
            loadedAt = .now
            return
        }
        #endif
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

    #if DEBUG
    /// Made-up accounts of two providers (example names only), for `-showUsageCardsDemo`.
    private static func demoAccounts() -> [UsageAccount] {
        let now = Date.now
        func w(_ label: String, _ minutes: Int, _ used: Double, _ left: TimeInterval, protect: Double? = nil) -> UsageWindow {
            UsageWindow(label: label, windowMinutes: minutes, usedPercent: used, resetsAt: now.addingTimeInterval(left), protectPercent: protect)
        }
        func a(_ id: String, _ provider: String, _ name: String, _ plan: String, _ windows: [UsageWindow], ago: TimeInterval = 240) -> UsageAccount {
            UsageAccount(id: id, provider: provider, name: name, plan: plan, windows: windows, sampledAt: now.addingTimeInterval(-ago), sources: ["Example gateway"])
        }
        return [
            a("c1", "claude", "Example Max 1", "Max", [w("5h", 300, 3, 4 * 3600, protect: 98), w("7d", 10080, 45, 4 * 86400, protect: 99)]),
            a("c2", "claude", "Example Max 2", "Max", [w("5h", 300, 62, 2 * 3600, protect: 90), w("7d", 10080, 85, 2 * 86400, protect: 95)], ago: 600),
            a("c3", "claude", "Example Ultra", "Ultra", [w("5h", 300, 3, 5 * 3600), w("7d", 10080, 55, 4 * 86400 + 3600)]),
            a("x1", "codex", "Example Team 1", "Team", [w("5h", 300, 0, 4 * 3600), w("7d", 10080, 52, 3 * 86400)], ago: 3600),
            a("x2", "codex", "Example Team 2", "Team", [w("5h", 300, 0, 4 * 3600), w("7d", 10080, 83, 18 * 3600)], ago: 3000),
        ]
    }
    #endif

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
/// With `protect` (#13173) the window's protection line too: a thin caution notch at that percent, a hatched zone from
/// it to the end and a "Protect N%" flag above it.
struct UsageMeter: View {
    var used: Double
    var expected: Double?
    var tall = false
    var protect: Double?

    var body: some View {
        let height = tall ? Theme.Size.barWide : Theme.Size.bar
        VStack(spacing: 0) {
            if let protect { flag(protect) }
            bar(height: height)
        }
        .animation(Theme.Motion.state, value: used)
        .accessibilityHidden(true)
    }

    private func bar(height: CGFloat) -> some View {
        GeometryReader { geo in
            ZStack(alignment: .leading) {
                Capsule().fill(Theme.Fill.chip)
                Capsule().fill(UsageWindows.isWarn(used) ? Theme.caution : Theme.accent)
                    .frame(width: geo.size.width * UsageWindows.fraction(used))
                if let protect {
                    let at = geo.size.width * UsageWindows.fraction(protect)
                    UsageProtectZone().stroke(Theme.caution.opacity(0.6), lineWidth: Theme.Size.hairline)
                        .frame(width: max(0, geo.size.width - at)).clipped()
                        .offset(x: at)
                }
                if let expected {
                    Capsule().fill(Color.secondary)
                        .frame(width: Theme.Size.rule, height: height + Theme.Space.xs)
                        .offset(x: geo.size.width * UsageWindows.fraction(expected) - Theme.Size.rule / 2)
                }
                if let protect {
                    Capsule().fill(Theme.caution)
                        .frame(width: Theme.Size.rule, height: height + Theme.Space.s)
                        .offset(x: geo.size.width * UsageWindows.fraction(protect) - Theme.Size.rule / 2)
                }
            }
            .clipShape(Rectangle())
        }
        .frame(height: height)
    }

    /// The flag over the line: its text centred on the notch, kept inside the bar's width.
    private func flag(_ protect: Double) -> some View {
        GeometryReader { geo in
            Text("Protect \(Int(protect.rounded()))%", comment: "Usage bar: the flag on a window's protection line, the percent it sits at")
                .font(Theme.Font.label).foregroundStyle(Theme.cautionText).lineLimit(1).fixedSize()
                .position(x: min(max(geo.size.width * UsageWindows.fraction(protect), Theme.Space.xl * 2), geo.size.width - Theme.Space.xl),
                          y: Theme.Size.protectFlag / 2)
        }
        .frame(height: Theme.Size.protectFlag)
    }
}

/// Diagonal hatching for the zone past a protection line: stroke it.
nonisolated struct UsageProtectZone: Shape {
    func path(in rect: CGRect) -> Path {
        var p = Path()
        var x = -rect.height
        while x < rect.width {
            p.move(to: CGPoint(x: x, y: rect.height))
            p.addLine(to: CGPoint(x: x + rect.height, y: 0))
            x += Theme.Space.xs
        }
        return p
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
            ForEach(UsageWindows.cardWindows(a), id: \.key) { w in UsageWindowRow(name: a.name, window: w, stale: a.stale, now: now) }
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
            UsageMeter(used: w.usedPercent, expected: pace?.expected, tall: true, protect: w.protectPercent)
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel([name, w.label, w.model, String(localized: "\(used) used"), w.protectPercent.map { String(localized: "Protect \(Int($0.rounded()))%") }, pace.flatMap { $0.state == .on ? nil : UsageWindows.paceLabel($0.state) }, reset]
            .compactMap { $0 }.filter { !$0.isEmpty }.joined(separator: String(localized: ", ", comment: "Separator between the parts of a VoiceOver label")))
    }
}

/// One account type's card (#13173): its name, how many accounts and when the newest was read, then every account of it
/// in 7-day-reset order, divided.
struct UsageProviderCard: View {
    var group: UsageProviderGroup
    var now: Date

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.m) {
            HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) {
                Text(UsageWindows.providerTitle(group.provider)).font(Theme.Font.sectionHeader).foregroundStyle(Color.primary).lineLimit(1)
                Text("\(group.accounts.count) accounts", comment: "Usage card: how many accounts of this type").font(Theme.Font.label)
                    .foregroundStyle(Color.secondary).lineLimit(1)
                Spacer(minLength: Theme.Space.s)
                Text(UsageWindows.sampleAge(group.sampledAt, now: now)).font(Theme.Font.label.monospacedDigit())
                    .foregroundStyle(.tertiary).lineLimit(1)
            }
            .accessibilityElement(children: .combine)
            .accessibilityAddTraits(.isHeader)
            ForEach(Array(group.accounts.enumerated()), id: \.element.id) { i, a in
                if i > 0 { Divider() }
                UsageAccountBlock(account: a, now: now)
            }
            let rows = UsageWindows.timelineRows(group.accounts, now: now)
            if !rows.isEmpty {
                Divider()
                UsageResetTimelineChart(rows: rows, now: now)
            }
        }
        .card()
        .accessibilityIdentifier("usage.card.\(group.provider)")
    }
}

/// The account types the owner chose to leave out of the cards, on this device. The hub's own `usage.hidden_providers`
/// is written only from the admin console (a phone's credentials do not open it), so this is a display filter of the
/// phone: it can hide more, never show what the hub left out.
@Observable
final class UsageProviderFilter {
    static let shared = UsageProviderFilter()
    private static let key = "usage.hiddenProviders"

    private(set) var hidden: Set<String>

    init(defaults: UserDefaults = .standard) {
        hidden = Set(defaults.stringArray(forKey: Self.key) ?? [])
    }

    func setShown(_ shown: Bool, provider: String, defaults: UserDefaults = .standard) {
        if shown { hidden.remove(provider) } else { hidden.insert(provider) }
        defaults.set(hidden.sorted(), forKey: Self.key)
    }
}

/// Every live account, grouped into one card per account type; the ages and countdowns tick each second while it is on screen.
struct UsageAccountsList: View {
    var accounts: [UsageAccount]
    var hidden: Set<String> = []

    var body: some View {
        TimelineView(.periodic(from: .now, by: 1)) { ctx in
            let live = UsageWindows.live(accounts, now: ctx.date)
            VStack(alignment: .leading, spacing: Theme.Space.m) {
                ForEach(UsageWindows.groups(live, hidden: hidden)) { g in
                    UsageProviderCard(group: g, now: ctx.date)
                }
            }
        }
    }
}

/// Machines' Subscription Usage (docs/server/usage-windows.md「UI」): the accounts' 5h / 7d windows under the machines' cards. Not drawn while
/// the hubs have none.
struct UsageWindowsCard: View {
    var store = UsageWindowsStore.shared
    var filter = UsageProviderFilter.shared

    var body: some View {
        let live = UsageWindows.live(store.accounts)
        if !live.isEmpty {
            VStack(alignment: .leading, spacing: Theme.Space.s) {
                HStack {
                    Text("Subscription Usage").font(Theme.Font.sectionHeader).foregroundStyle(.secondary).lineLimit(1)
                        .accessibilityAddTraits(.isHeader)
                    Spacer(minLength: Theme.Space.s)
                    Menu {
                        ForEach(UsageWindows.providers(live), id: \.self) { p in
                            Toggle(UsageWindows.providerTitle(p), isOn: Binding(get: { !filter.hidden.contains(p) }, set: { filter.setShown($0, provider: p) }))
                        }
                    } label: {
                        ThemedIcon(filter.hidden.isEmpty ? Theme.Icon.filter : Theme.Icon.filterActive)
                    }
                    .accessibilityLabel(String(localized: "Account types shown"))
                    .accessibilityIdentifier("machines.usage.filter")
                }
                .padding(.horizontal, Theme.Space.xs)
                UsageAccountsList(accounts: store.accounts, hidden: filter.hidden)
            }
            .padding(.top, Theme.Space.s)
            .accessibilityIdentifier("machines.usage")
        }
    }
}
