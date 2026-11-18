#if canImport(ActivityKit)
import ActivityKit
#endif
import SwiftUI
import UserNotifications

/// Settings › Notifications: which pushes this iPhone gets. The hub keeps the choice per device (notify.go), so a second phone or
/// an iPad can choose differently; the system switch (allowed at all, sounds, lock screen) stays in iOS Settings.
/// Reading the prefs is `NotificationSettingsModel`'s: only the rows the hub keeps wait for it, the system-settings
/// buttons work without the hub, and a failed read says why with Retry (#1860).
struct NotificationSettingsView: View {
    @Environment(AppModel.self) private var model
    @State private var page: NotificationSettingsModel?
    @State private var system: UNAuthorizationStatus?
    #if os(iOS)
    @State private var activitiesAllowed = ActivityAuthorizationInfo().areActivitiesEnabled
    #endif
    /// The quiet-hours request in flight: an older one's answer does not undo a newer choice.
    @State private var quietRequest = 0
    /// The hub's low-distraction switch (#8157); nil until read, and on a hub without it (the section stays away).
    @State private var lowDistraction: Bool?
    /// The low-distraction write in flight: an older one's answer does not undo a newer choice.
    @State private var lowDistractionRequest = 0
    /// `-openIslandSettings YES` / `-openUrgentCallSettings YES` (DEBUG): the page opened for screenshots.
    @State private var debugPage: DebugPage?
    private enum DebugPage: Hashable { case island, urgentCalls }

    #if os(iOS)
    /// Urgent calls need LiveCommunicationKit, whose Mac build lacks symbols the iPhone app uses (#9948).
    static func offersUrgentCalls(isiOSAppOnMac: Bool = CallPlatform.isiOSAppOnMac) -> Bool {
        !isiOSAppOnMac
    }
    #endif

    var body: some View {
        SettingsForm {
            if let error = page?.error {
                Section {
                    LoadProblem(text: error)
                    Button { Task { await page?.load() } } label: { Label("Retry", systemImage: Theme.Icon.retry).lineLimit(1) }
                        .disabled(page?.loading == true)
                        .accessibilityIdentifier("notify.retry")
                }
            }

            if systemOff {
                Section {
                    Label {
                        VStack(alignment: .leading, spacing: Theme.Space.xs) {
                            Text("Notifications are off").font(Theme.Font.heading)
                            Text("The settings below take effect only after you allow Persona to send notifications in Settings.").font(Theme.Font.caption).foregroundStyle(.secondary)
                        }
                    } icon: {
                        ThemedIcon(Theme.Icon.muted).foregroundStyle(Theme.caution)
                    }
                    openSystemSettings
                }
            }

            // Whether any of what follows can reach this device while Persona is closed (#5998).
            BackgroundPushSection(status: pushStatus)

            Section {
                SettingsSwitchRow(title: String(localized: "When You're Needed"),
                                  detail: String(localized: "Questions, plan approvals, tool permissions, a terminal waiting for input"),
                                  symbol: "exclamationmark.bubble", tint: StatusPalette.needsYou.dot, iconColor: StatusPalette.needsYou.dot,
                                  isOn: binding(\.input))
                .disabled(prefs == nil)
            } footer: {
                #if os(macOS)
                Text("On permission and plan notifications you can Allow/Approve or Deny/Reject directly (hover over the notification and click Options).")
                #else
                Text("On permission and plan notifications you can Allow/Approve (after unlocking) or Deny/Reject directly.")
                #endif
            }

            Section {
                Picker(selection: binding(\.done)) {
                    Text("Off [Notification Settings View b3253d]").tag(NotifyPrefs.Done.off)
                    Text("Long Tasks Only").tag(NotifyPrefs.Done.long)
                    Text("Always [Notification Settings View 30ec51]").tag(NotifyPrefs.Done.always)
                } label: {
                    Label("When an Agent Finishes", systemImage: Theme.Icon.success)
                }
                #if os(macOS)
                .pickerStyle(.radioGroup) // a Mac form's one-of-several (#1221)
                #else
                .pickerStyle(.inline)
                #endif
                .labelsHidden() // the section header says it
                .disabled(prefs == nil)
            } header: {
                Text("When an Agent Finishes")
            } footer: {
                Text(doneFooter)
            }

            // This device's quiet hours (#1863); a hub whose prefs have no `quiet` has none.
            if let quiet = prefs?.quiet { quietHoursSection(quiet) }

            // Live Activities (Lock Screen and Dynamic Island) are an iPhone's: a Mac has neither (docs/apple/mac.md).
            #if os(iOS)
            Section {
                SettingsSwitchRow(title: String(localized: "Lock Screen and Dynamic Island"),
                                  detail: String(localized: "One Live Activity sums up agents that need you, are working, or are done"),
                                  symbol: Theme.Icon.persona, tint: Theme.brand, iconColor: Theme.brand, isOn: binding(\.liveActivity))
                .disabled(prefs == nil)
                SettingsSwitchRow(title: String(localized: "Show Details on Lock Screen"),
                                  detail: String(localized: "When off, the Lock Screen shows only counts and “A permission is waiting”, not agent names or content"),
                                  symbol: Theme.Icon.view, tint: Theme.Hue.gray, iconColor: .secondary, isOn: binding(\.lockDetails))
                .disabled(prefs?.liveActivity != true)
                NavigationLink {
                    IslandSettingsView(island: binding(\.island))
                } label: {
                    LabeledContent {
                        Text((prefs?.island ?? IslandPrefs()).preset.title).lineLimit(1)
                    } label: {
                        Label("Dynamic Island", systemImage: "capsule.fill")
                    }
                }
                .disabled(prefs?.liveActivity != true)
                if !activitiesAllowed {
                    Label {
                        Text("Live Activities for Persona are turned off in Settings").font(Theme.Font.caption)
                    } icon: {
                        ThemedIcon(Theme.Icon.errorFilled).foregroundStyle(Theme.caution)
                    }
                    Button {
                        UIApplication.shared.open(URL(string: UIApplication.openSettingsURLString)!)
                    } label: {
                        Label("Open Settings", systemImage: "gear").lineLimit(1)
                    }
                }
            } header: {
                Text("Live Activities")
            } footer: {
                Text("On the Lock Screen you can Allow/Approve (after unlocking) or Deny directly. The Dynamic Island appears only when unlocked and always shows details.")
            }
            #endif

            // Urgent calls (docs/assistant/urgent-call.md): the assistant may ring this iPhone; off by default.
            #if os(iOS)
            if Self.offersUrgentCalls() {
                Section {
                    NavigationLink {
                        UrgentCallSettingsView()
                    } label: {
                        Label("Urgent Calls", systemImage: Theme.Icon.voiceChannel)
                    }
                    .accessibilityIdentifier("notify.urgentCalls")
                } footer: {
                    Text("When something can't wait, Persona can ring this iPhone like a phone call.")
                }
            }
            #endif

            // The hub's switch, for every device (#8157): after this device's own rows.
            if let lowDistraction { lowDistractionSection(lowDistraction) }

            // The Mac had the button twice while notifications were off (#1224 A5): the warning above has it then.
            #if os(macOS)
            if !systemOff { systemSettingsSection }
            #else
            systemSettingsSection
            #endif
        }
        .themedPage()
        .detailTitle(String(localized: "Notifications"))
        .navigationBarTitleDisplayMode(.inline)
        .overlay { if page?.spinning != false { ProgressView() } }
        .task { await load() }
        .task { await loadLowDistraction() }
        .task { await refreshPush() } // the hub's way to this device now, not the one kept since launch (#1859)
        .onChange(of: model.isLive) { _, live in
            if live { Task { await page?.reloadIfNeeded() } } // the hub is back: read what failed
        }
        #if DEBUG && os(iOS)
        // `-openIslandSettings YES` (with `-openNotificationSettings YES -tab settings`): Dynamic Island.
        // One destination for both: a second navigationDestination(isPresented:) on the same view never opened.
        .navigationDestination(item: $debugPage) { page in
            switch page {
            case .island: IslandSettingsView(island: binding(\.island))
            case .urgentCalls:
                if Self.offersUrgentCalls() { UrgentCallSettingsView() }
            }
        }
        .task {
            // `-openUrgentCallSettings YES` (with `-openNotificationSettings YES -tab settings`): Urgent Calls.
            let d = UserDefaults.standard
            let urgentCalls = d.bool(forKey: "openUrgentCallSettings") && Self.offersUrgentCalls()
            let page: DebugPage? = d.bool(forKey: "openIslandSettings") ? .island : urgentCalls ? .urgentCalls : nil
            if let page {
                try? await Task.sleep(for: .milliseconds(600))
                debugPage = page
            }
        }
        #endif
        .onReceive(NotificationCenter.default.publisher(for: UIApplication.didBecomeActiveNotification)) { _ in
            Task {
                system = await UNUserNotificationCenter.current().notificationSettings().authorizationStatus
                await page?.reloadIfNeeded()
                await refreshPush()
            }
            #if os(iOS)
            activitiesAllowed = ActivityAuthorizationInfo().areActivitiesEnabled
            #endif
        }
    }

    /// Allowed for a while (App Clips): iOS only.
    private static func ephemeral(_ status: UNAuthorizationStatus) -> Bool {
        #if os(iOS) || os(visionOS)
        status == .ephemeral
        #else
        false
        #endif
    }

    /// Notifications are not allowed for the app (the system's switch): the page says so first.
    private var systemOff: Bool {
        guard let system else { return false }
        return system != .authorized && system != .provisional && !Self.ephemeral(system)
    }

    /// Background push at the hub this page's settings go to (#5998).
    private var pushStatus: PushStatus {
        var health = PushHealthStore.shared.health
        #if DEBUG
        // `-showPushHealth refused | moved | unreachable | relay | direct | failed | unknown | unchecked`: a sample, for screenshots.
        if let sample = UserDefaults.standard.string(forKey: "showPushHealth") { health = .sample(sample, group: model.hub?.group ?? "") }
        #endif
        return health.status(group: model.hub?.group ?? "", allowed: system.map { _ in !systemOff })
    }

    private var systemSettingsSection: some View {
        Section {
            openSystemSettings
        } footer: {
            Text("Change sounds, Lock Screen display, and Focus in Settings.")
        }
    }

    private var openSystemSettings: some View {
        Button {
            UIApplication.shared.open(URL(string: UIApplication.openNotificationSettingsURLString)!)
        } label: {
            Label("Notification Settings", systemImage: "gear").lineLimit(1)
        }
    }

    private var doneFooter: String {
        let delay = NotifyPrefs.duration(prefs?.doneDelaySeconds)
        let long = NotifyPrefs.duration(prefs?.longTurnSeconds)
        return String(localized: "After finishing, Persona waits \(delay) and skips the notification if you've already looked on your computer or in the app. It notifies once, after background tasks and subagents are all done. A “long task” is a turn that ran \(long) or longer; quick answers are best read where you asked.")
    }

    /// Low-distraction mode (#8157): the hub's push grading switch (docs/server/server.md "Push grading"), the same one
    /// persona_settings `notifications.low_distraction` changes after a card. It is the hub's, not this device's: the
    /// footer says so and what still comes.
    private func lowDistractionSection(_ on: Bool) -> some View {
        Section {
            SettingsSwitchRow(title: String(localized: "Low-Distraction Mode"),
                              detail: String(localized: "Only what needs you notifies you; the rest waits for the briefing"),
                              symbol: Theme.Icon.muted, tint: Theme.Hue.orange, iconColor: Theme.Hue.orange,
                              accessibilityID: "notify.lowDistraction",
                              isOn: Binding { on } set: { saveLowDistraction($0) })
        } footer: {
            Text("For all your devices. When on, questions, approvals, decisions only you can make, your reminders and the main conversation's notices still notify you; task results, “Please verify” requests and other conversations' notices wait for the briefing and Held Pushes, and finished agents stay silent.")
        }
    }

    /// Reads the switch; a hub without it (404) or one that does not answer shows no section.
    private func loadLowDistraction() async {
        guard let api = model.api, !model.isDemo else { return }
        let request = lowDistractionRequest
        let on = try? await api.pushLnMode().enabled
        guard request == lowDistractionRequest else { return }
        lowDistraction = on
    }

    /// Shown at once; the hub's answer then stands, and a refusal puts the switch back and says why.
    private func saveLowDistraction(_ on: Bool) {
        guard let api = model.api, let before = lowDistraction, before != on else { return }
        lowDistraction = on
        lowDistractionRequest += 1
        let request = lowDistractionRequest
        Task {
            do {
                let saved = try await api.setPushLnMode(on)
                guard request == lowDistractionRequest else { return }
                lowDistraction = saved.enabled
                Haptics.success()
            } catch {
                guard request == lowDistractionRequest else { return }
                lowDistraction = before
                model.show(error)
            }
        }
    }

    @ViewBuilder private func quietHoursSection(_ quiet: QuietHours) -> some View {
        Section {
            SettingsSwitchRow(title: String(localized: "Quiet Hours"),
                              detail: quiet.enabled ? nil : String(localized: "Off: notifications come at any time"),
                              symbol: Theme.Icon.quietHours, tint: Theme.Hue.indigo, iconColor: Theme.Hue.indigo,
                              accessibilityID: "notify.quiet", isOn: quietBinding(quiet, \.enabled))
            if quiet.enabled {
                DatePicker(String(localized: "From [Notification Settings View 5d2f81]"), selection: clockBinding(quiet, \.start),
                           displayedComponents: .hourAndMinute)
                    .accessibilityIdentifier("notify.quiet.start")
                DatePicker(String(localized: "Until [Notification Settings View 9a07c3]"), selection: clockBinding(quiet, \.end),
                           displayedComponents: .hourAndMinute)
                    .accessibilityIdentifier("notify.quiet.end")
            }
        } footer: {
            if let zone = quiet.elsewhere() {
                Text("During these hours you're not told when an agent finishes (the morning's Today card sums it up), and things that need you arrive without a sound. Reminders you asked the assistant for still come as usual. A chat bot's nudges about unanswered cards also wait while any of your devices is in its quiet hours. They're in \(zone), where they were set; changing them here puts them in this device's time zone.")
            } else {
                Text("During these hours you're not told when an agent finishes (the morning's Today card sums it up), and things that need you arrive without a sound. Reminders you asked the assistant for still come as usual. A chat bot's nudges about unanswered cards also wait while any of your devices is in its quiet hours. In this device's time zone.")
            }
        }
    }

    private func quietBinding(_ quiet: QuietHours, _ path: WritableKeyPath<QuietHours, Bool>) -> Binding<Bool> {
        Binding { quiet[keyPath: path] } set: { value in
            var next = quiet
            next[keyPath: path] = value
            saveQuiet(next)
        }
    }

    /// A "HH:MM" of the quiet hours as a time today, for the picker.
    private func clockBinding(_ quiet: QuietHours, _ path: WritableKeyPath<QuietHours, String>) -> Binding<Date> {
        Binding {
            let minutes = QuietHours.minutes(quiet[keyPath: path]) ?? 0
            return Calendar.current.date(bySettingHour: minutes / 60, minute: minutes % 60, second: 0, of: .now) ?? .now
        } set: { date in
            let c = Calendar.current.dateComponents([.hour, .minute], from: date)
            var next = quiet
            next[keyPath: path] = QuietHours.clock((c.hour ?? 0) * 60 + (c.minute ?? 0))
            guard next != quiet else { return }
            saveQuiet(next)
        }
    }

    /// Shown at once like a control's change (`NotificationSettingsModel.change`), but only `quiet` goes, with this
    /// device's time zone, through its own endpoint.
    private func saveQuiet(_ quiet: QuietHours) {
        guard let api = model.api, prefs != nil else { return }
        var quiet = quiet
        quiet.tz = TimeZone.current.identifier
        page?.showQuiet(quiet)
        quietRequest += 1
        let request = quietRequest
        Task {
            do {
                let saved = try await api.setQuietHours(device: PushRegistrar.deviceID, quiet)
                guard request == quietRequest else { return }
                page?.quietSaved(saved)
                model.savedNotifyPrefs(saved)
            } catch {
                guard request == quietRequest else { return }
                page?.quietFailed()
                model.show(error)
            }
        }
    }

    private func binding<V>(_ path: WritableKeyPath<NotifyPrefs, V>) -> Binding<V> {
        Binding {
            (prefs ?? NotifyPrefs())[keyPath: path]
        } set: { value in
            page?.change { $0[keyPath: path] = value } // in order, over the hub's last answer (#1913)
        }
    }

    /// The prefs as the page shows them: the hub's last answer with the changes still waiting (nil until read).
    private var prefs: NotifyPrefs? { page?.prefs }

    /// Background Push's route at this page's hub, read again (#1859).
    private func refreshPush() async {
        guard let api = model.api, let group = model.hub?.group, !model.isDemo else { return }
        await PushRegistrar.shared.refreshRoute(group: group) { try await api.deviceTokens(timeout: 15) }
    }

    private func load() async {
        system = await UNUserNotificationCenter.current().notificationSettings().authorizationStatus
        if page == nil {
            let api = { [model] in model.api }
            page = NotificationSettingsModel(deps: .init(
                load: {
                    guard let api = api() else { throw APIError(status: 0, message: String(localized: "Not connected to the hub yet")) }
                    return try await api.notifyPrefs(device: PushRegistrar.deviceID)
                },
                save: { next in
                    guard let api = api() else { throw APIError(status: 0, message: String(localized: "Not connected to the hub yet")) }
                    return try await api.setNotifyPrefs(device: PushRegistrar.deviceID, next)
                },
                applied: { LiveActivityController.shared.settings($0) },
                saved: { Haptics.success() },
                failed: { [model] in model.show($0) }))
        }
        await page?.load()
    }

}
