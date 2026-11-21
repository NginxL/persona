import SwiftUI

/// Settings › Connectors › This iPhone › iPhone Control: the guided setup that lets the assistant operate this iPhone
/// (#8199, docs/extensions/iphone.md "Guided setup"). An overview of the five steps, then one screen per step that
/// moves on by itself when the hub says it is done, then the page the user comes back to: this iPhone's consent,
/// Try It, and "Ask Me Before Every Step". Every state comes from the hub, never from what was tapped.
struct IPhoneSetupView: View {
    @Environment(AppModel.self) private var app
    @Environment(\.scenePhase) private var phase
    @State private var setup = IPhoneSetupModel()
    /// Past the overview: the current step's screen.
    @State private var started = false

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: Theme.Space.l) {
                if !setup.loaded {
                    ProgressView().frame(maxWidth: .infinity).padding(.top, Theme.Space.xxl)
                } else if let step = setup.current {
                    if started {
                        stepPage(step)
                    } else {
                        overview
                    }
                } else {
                    finish
                }
            }
            .padding(.horizontal, Theme.Space.page)
            .padding(.bottom, Theme.Space.section)
            .frame(maxWidth: .infinity, alignment: .leading)
            .animation(Theme.Motion.standard, value: setup.current)
        }
        .themedGroupedPage()
        .navigationTitle(IPhoneSetupText.navTitle)
        .navigationBarTitleDisplayMode(.inline)
        .safeAreaInset(edge: .bottom) { bottomBar }
        .task(id: app.config) { await setup.load() }
        // Only while the page is in front and something waits on the cable, the phone or a job.
        .tabActiveTask(id: setup.polls && phase == .active) {
            guard phase == .active else { return }
            while setup.polls, !Task.isCancelled {
                try? await Task.sleep(for: .seconds(2))
                await setup.load()
            }
        }
        .onChange(of: setup.error) { _, e in
            if let e { app.toast = Toast(e, style: .error) }
        }
    }

    // MARK: Overview

    private var overview: some View {
        VStack(alignment: .leading, spacing: Theme.Space.l) {
            hero(Theme.Icon.phoneControl, tint: Theme.accent)
            Text(IPhoneSetupText.title).font(Theme.Font.display)
            Text("Through one of your computers, the assistant can see this iPhone’s screen, open apps, tap and type.")
                .font(Theme.Font.body)
            if setup.iosMajor < 27 {
                iosNote
            } else {
                Text("You need a cable and a Mac that’s on. About 5 minutes.")
                    .font(Theme.Font.secondary).foregroundStyle(.secondary)
            }
            VStack(spacing: 0) {
                ForEach(IPhoneSetupFlow.overview(setup.input), id: \.0) { step, state in
                    overviewRow(step, state)
                    if step != .allow { Divider() }
                }
            }
            .card(padding: 0)
            safety
        }
    }

    private func overviewRow(_ step: IPhoneSetupStep, _ state: IPhoneSetupState) -> some View {
        HStack(spacing: Theme.Space.m) {
            if state == .done {
                ThemedIcon(Theme.Icon.successFilled).foregroundStyle(Theme.positive)
            } else {
                Text("\(step.rawValue + 1)").font(Theme.Font.secondaryStrong.monospacedDigit())
                    .foregroundStyle(state == .later ? Color.secondary : Theme.accent)
            }
            Text(IPhoneSetupText.stepTitle(step)).font(Theme.Font.body)
                .foregroundStyle(state == .later ? Color.secondary : Color.primary)
            Spacer(minLength: Theme.Space.s)
            if state != .later { stateChip(state) }
        }
        .padding(.horizontal, Theme.Space.l)
        .padding(.vertical, Theme.Space.m)
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("iphone-setup-row-\(step.rawValue + 1)")
    }

    // MARK: A step's screen

    @ViewBuilder private func stepPage(_ step: IPhoneSetupStep) -> some View {
        progress(step)
        switch step {
        case .turnOn: turnOnStep
        case .computer: computerStep
        case .pair: pairStep
        case .developerMode: developerStep
        case .allow: allowStep
        }
    }

    private func progress(_ step: IPhoneSetupStep) -> some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            HStack(spacing: Theme.Space.sm) {
                ForEach(IPhoneSetupStep.allCases) { s in
                    Capsule()
                        .fill(s.rawValue <= step.rawValue ? Theme.accent : Theme.Fill.chip)
                        .frame(height: Theme.Size.barWide)
                }
            }
            .accessibilityHidden(true)
            Text(IPhoneSetupText.stepOf(step)).font(Theme.Font.caption).foregroundStyle(.secondary)
        }
        .padding(.top, Theme.Space.s)
    }

    private var turnOnStep: some View {
        VStack(alignment: .leading, spacing: Theme.Space.l) {
            hero(Theme.Icon.pluginPower, tint: Theme.accent)
            Text(IPhoneSetupText.stepTitle(.turnOn)).font(Theme.Font.title)
            Text("Phone control is off. Turn it on so your computers can reach your iPhones.").font(Theme.Font.body)
            stateCard(.needsYou, String(localized: "Off for now [iPhone Setup]"))
            details(String(localized: "Turns on the built-in extension “Phone control” on your hub. You can turn it off again in Settings › Extensions."))
        }
    }

    private var computerStep: some View {
        let chosen = setup.computer
        return VStack(alignment: .leading, spacing: Theme.Space.l) {
            Text(IPhoneSetupText.stepTitle(.computer)).font(Theme.Font.title)
            Text("The assistant reaches this iPhone through this computer. Pick a Mac that’s usually on.").font(Theme.Font.body)
            if setup.computers.isEmpty {
                stateCard(.needsYou, String(localized: "None of your computers can do this yet. Connect a Mac with Persona Link, then come back."))
            } else {
                VStack(spacing: 0) {
                    ForEach(setup.computers) { c in
                        computerRow(c, chosen: chosen?.id == c.id)
                        if c.id != setup.computers.last?.id { Divider() }
                    }
                }
                .card(padding: 0)
            }
            if let c = chosen, let job = c.job("install") {
                if job.running {
                    stateCard(.working, job.step ?? String(localized: "Installing the iPhone helper…"))
                } else if job.state == "failed" {
                    stateCard(.needsYou, job.error ?? String(localized: "Couldn’t install the iPhone helper."))
                }
            }
            details(String(localized: "Turns on “Allow the assistant to operate iPhones through this computer” on that computer’s page, and installs the iPhone helper there when it isn’t yet."))
        }
    }

    private func computerRow(_ c: IPhoneSetupComputer, chosen: Bool) -> some View {
        Button {
            Task { await setup.choose(c.id) }
        } label: {
            HStack(spacing: Theme.Space.m) {
                ThemedIcon(c.mac ? Theme.Icon.laptop : Theme.Icon.server)
                    .foregroundStyle(c.unusable == nil ? Theme.accent : Color.secondary)
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text(c.name).font(Theme.Font.body).foregroundStyle(c.unusable == nil ? Color.primary : Color.secondary)
                    Text(IPhoneSetupText.computerLine(c)).font(Theme.Font.label).foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
                Spacer(minLength: Theme.Space.s)
                if setup.busy == .choosing(c.id) {
                    ProgressView()
                } else if chosen && c.turnedOn {
                    ThemedIcon(Theme.Icon.successFilled).foregroundStyle(Theme.accent)
                }
            }
            .padding(.horizontal, Theme.Space.l)
            .padding(.vertical, Theme.Space.m)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .disabled(c.unusable != nil || setup.busy != nil)
        .accessibilityIdentifier("iphone-setup-computer-\(c.id)")
    }

    private var pairStep: some View {
        let name = setup.computer?.name ?? ""
        return VStack(alignment: .leading, spacing: Theme.Space.l) {
            HStack(spacing: Theme.Space.m) {
                hero(Theme.Icon.phone, tint: Theme.accent)
                hero(Theme.Icon.laptop, tint: Theme.accent)
            }
            Text(IPhoneSetupText.stepTitle(.pair)).font(Theme.Font.title)
            Text("Connect this iPhone to \(name) with a cable, then unlock it.").font(Theme.Font.body)
            switch IPhoneSetupFlow.pairPhase(setup.input) {
            case .waitingForCable:
                stateCard(.needsYou, String(localized: "Waiting for the cable…"))
            case .onCable:
                stateCard(.working, String(localized: "Pairing with this iPhone…"))
            case .pairing(let code):
                stateCard(.working, IPhoneSetupText.pairing(code))
            case .failed(let why):
                stateCard(.needsYou, why.isEmpty ? String(localized: "Couldn’t pair.") : why)
            case .done:
                stateCard(.done, String(localized: "Paired"))
            }
            details(String(localized: "\(name) keeps a key that lets it reach this iPhone over your network afterwards. The key never leaves that computer. Keep the cable in until setup is done."))
        }
    }

    private var developerStep: some View {
        let name = setup.computer?.name ?? ""
        let phase = IPhoneSetupFlow.devPhase(setup.input)
        return VStack(alignment: .leading, spacing: Theme.Space.l) {
            hero(Theme.Icon.build, tint: Theme.accent)
            Text(IPhoneSetupText.stepTitle(.developerMode)).font(Theme.Font.title)
            switch phase {
            case .turnOn, .done:
                Text("A computer can see the screen and tap only with Developer Mode on. Your iPhone restarts once.").font(Theme.Font.body)
                VStack(spacing: 0) {
                    ForEach(Array(IPhoneSetupText.developerSteps.enumerated()), id: \.offset) { n, line in
                        HStack(alignment: .firstTextBaseline, spacing: Theme.Space.m) {
                            Text("\(n + 1)").font(Theme.Font.secondaryStrong.monospacedDigit()).foregroundStyle(Theme.accent)
                            Text(line).font(Theme.Font.secondary).fixedSize(horizontal: false, vertical: true)
                            Spacer(minLength: 0)
                        }
                        .padding(.horizontal, Theme.Space.l)
                        .padding(.vertical, Theme.Space.m)
                        if n < IPhoneSetupText.developerSteps.count - 1 { Divider() }
                    }
                }
                .card(padding: 0)
                stateCard(.needsYou, String(localized: "Developer Mode is off on this iPhone."))
            case .connecting:
                developerOn
                stateCard(.working, String(localized: "Connecting to this iPhone…"))
            case .prepare(let auto):
                developerOn
                if setup.computer?.noXcode == true {
                    stateCard(.needsYou, String(localized: "\(name) needs Xcode first. Install it from the App Store on that Mac, then come back."))
                } else if auto {
                    stateCard(.working, String(localized: "\(name) is getting this iPhone ready…"))
                } else {
                    stateCard(.needsYou, String(localized: "\(name) puts a small developer component from Apple on this iPhone. Nothing is reinstalled or erased. It goes away when the iPhone restarts, and the computer adds it again by itself."))
                }
                details(String(localized: "Apple’s developer disk image. Each time, Apple’s signing server makes it for this iPhone, so Apple receives this iPhone’s chip ID."))
            case .preparing(let step):
                developerOn
                stateCard(.working, step ?? String(localized: "\(name) is getting this iPhone ready…"))
            case .prepareFailed(let reason, let developerModeOn):
                if developerModeOn { developerOn }
                stateCard(.needsYou, IPhoneControlText.prepareFailure(reason))
            case .problem(let note):
                developerOn
                stateCard(.needsYou, note.isEmpty ? String(localized: "This iPhone can’t be reached right now.") : note)
            }
        }
    }

    private var developerOn: some View {
        Label {
            Text("Developer Mode is on").font(Theme.Font.body)
        } icon: {
            ThemedIcon(Theme.Icon.successFilled).foregroundStyle(Theme.positive)
        }
        .padding(Theme.Space.l)
        .frame(maxWidth: .infinity, alignment: .leading)
        .card(padding: 0)
    }

    private var allowStep: some View {
        VStack(alignment: .leading, spacing: Theme.Space.l) {
            hero(Theme.Icon.phoneControl, tint: Theme.accent)
            Text(IPhoneSetupText.stepTitle(.allow)).font(Theme.Font.title)
            Text("Only you can allow this, and only here on this iPhone.").font(Theme.Font.body)
            switch setup.match {
            case .this(let me, _):
                consentCard(me)
            case .choose(let alike):
                Text("Several paired iPhones look like this one. Pick this iPhone.")
                    .font(Theme.Font.secondary).foregroundStyle(.secondary)
                VStack(spacing: 0) {
                    ForEach(alike) { d in
                        HStack(spacing: Theme.Space.m) {
                            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                                Text(d.device.name).font(Theme.Font.body).lineLimit(1)
                                Text(IPhoneConsentText.macs(d)).font(Theme.Font.label).foregroundStyle(.secondary)
                            }
                            Spacer(minLength: Theme.Space.s)
                            Button { setup.pick(d.id) } label: { Text("This One").lineLimit(1) }
                                .themedButton()
                                .accessibilityIdentifier("iphone-pick-\(d.id)")
                        }
                        .padding(.horizontal, Theme.Space.l)
                        .padding(.vertical, Theme.Space.m)
                    }
                }
                .card(padding: 0)
            case .none:
                stateCard(.needsYou, String(localized: "Your computers don’t list this iPhone right now. Keep it near the computer and unlocked."))
            }
            if setup.iosMajor < 27 { iosNote }
            safety
        }
    }

    private func consentCard(_ me: IPhoneSeen) -> some View {
        VStack(alignment: .leading, spacing: Theme.Space.m) {
            HStack(spacing: Theme.Space.m) {
                ThemedIcon(Theme.Icon.phone).foregroundStyle(Theme.accent)
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text(me.device.name.isEmpty ? String(localized: "iPhone") : me.device.name).font(Theme.Font.body).lineLimit(1)
                    Text(IPhoneSetupText.deviceLine(me, through: setup.computer?.name)).font(Theme.Font.label).foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
            Divider()
            Toggle(isOn: Binding(get: { me.consented }, set: { on in Task { await setup.setAllowed(on) } })) {
                Text("Allow the Assistant to Operate This iPhone").font(Theme.Font.body)
            }
            .disabled(setup.busy == .allowing)
            .accessibilityIdentifier("iphone-consent")
        }
        .card()
    }

    // MARK: Finish

    private var finish: some View {
        VStack(alignment: .leading, spacing: Theme.Space.l) {
            hero(Theme.Icon.successFilled, tint: Theme.positive)
            Text("All Set").font(Theme.Font.display)
            Text("The assistant can operate this iPhone through \(setup.computer?.name ?? "").").font(Theme.Font.body)
            if setup.iosMajor < 27 { iosNote }
            if setup.busy == .trying {
                stateCard(.working, String(localized: "Taking a screenshot, then opening Weather…"))
            } else if let tried = setup.tried {
                tryResult(tried)
            }
            if case .this(let me, _) = setup.match {
                consentCard(me)
            }
            Toggle(isOn: Binding(get: { setup.askEvery }, set: { on in Task { await setup.setAskEvery(on) } })) {
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text("Ask Me Before Every Step").font(Theme.Font.body)
                    Text("Off: only paying, deleting, sending messages and passwords ask you.")
                        .font(Theme.Font.label).foregroundStyle(.secondary)
                }
            }
            .disabled(setup.busy == .asking)
            .card()
            .accessibilityIdentifier("iphone-ask-every")
            safety
        }
    }

    private func tryResult(_ r: IPhoneTryResult) -> some View {
        VStack(alignment: .leading, spacing: Theme.Space.m) {
            HStack(spacing: Theme.Space.m) {
                ThemedIcon(r.screenshot != nil ? Theme.Icon.successFilled : Theme.Icon.errorFilled)
                    .foregroundStyle(r.screenshot != nil ? Theme.positive : Theme.caution)
                Text(r.screenshot != nil ? String(localized: "Took a screenshot") : String(localized: "No screenshot")).font(Theme.Font.body)
                Spacer(minLength: Theme.Space.s)
                if let image = setup.triedImage {
                    Image(uiImage: image).resizable().scaledToFit()
                        .scaledFrame(height: Theme.Size.avatarXL)
                        .clipShape(.rect(cornerRadius: Theme.Radius.thumbnail))
                        .accessibilityLabel(Text("Screenshot of this iPhone"))
                }
            }
            HStack(spacing: Theme.Space.m) {
                ThemedIcon(r.opened ? Theme.Icon.successFilled : Theme.Icon.errorFilled)
                    .foregroundStyle(r.opened ? Theme.positive : Theme.caution)
                Text(r.opened ? String(localized: "Opened Weather") : String(localized: "Didn’t open Weather")).font(Theme.Font.body)
            }
            if let e = r.error, !e.isEmpty {
                Text(e).font(Theme.Font.label).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            } else if r.opened {
                Text("Now ask the assistant, for example “Turn on Low Power Mode on my iPhone”.")
                    .font(Theme.Font.label).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            }
        }
        .card()
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("iphone-try-result")
    }

    // MARK: The bottom bar

    @ViewBuilder private var bottomBar: some View {
        if setup.loaded {
            VStack(spacing: Theme.Space.s) {
                if let step = setup.current {
                    if !started {
                        primary(setup.current == .turnOn ? String(localized: "Set Up [iPhone Setup]") : String(localized: "Continue"), id: "iphone-setup-start") {
                            started = true
                        }
                    } else {
                        stepButtons(step)
                    }
                } else {
                    primary(String(localized: "Try It"), id: "iphone-try", busy: setup.busy == .trying) { Task { await setup.tryIt() } }
                }
            }
            .padding(.horizontal, Theme.Space.page)
            .padding(.vertical, Theme.Space.s)
            .background(Theme.groupedPage) // what scrolls under it stays readable
        }
    }

    @ViewBuilder private func stepButtons(_ step: IPhoneSetupStep) -> some View {
        switch step {
        case .turnOn:
            primary(String(localized: "action.turnOn", defaultValue: "Turn On"), id: "iphone-setup-turn-on", busy: setup.busy == .turningOn) {
                Task { await setup.turnOn() }
            }
        case .computer:
            // A Mac picked before that isn't ready now (its switch off, or no helper): use it again in one tap.
            if let c = setup.computer, !(c.turnedOn && c.helperInstalled), c.job("install")?.running != true {
                primary(String(localized: "Use \(c.name)"), id: "iphone-setup-use", busy: setup.busy == .choosing(c.id)) {
                    Task { await setup.choose(c.id) }
                }
            }
        case .pair:
            switch IPhoneSetupFlow.pairPhase(setup.input) {
            case .pairing:
                secondary(String(localized: "Cancel"), id: "iphone-setup-cancel") { Task { await setup.cancelPair() } }
            case .failed:
                primary(String(localized: "Retry"), id: "iphone-setup-retry", busy: setup.busy == .pairing) { Task { await setup.retryPair() } }
            default:
                EmptyView()
            }
        case .developerMode:
            switch IPhoneSetupFlow.devPhase(setup.input) {
            case .prepare(auto: false) where setup.computer?.noXcode != true:
                primary(String(localized: "Continue"), id: "iphone-setup-prepare", busy: setup.busy == .preparing) { Task { await setup.prepare() } }
            case .prepareFailed:
                primary(String(localized: "Retry"), id: "iphone-setup-prepare") { Task { await setup.prepare() } }
            case .turnOn, .problem, .prepare:
                secondary(String(localized: "Check Again [Browser Section 953cf0]"), id: "iphone-setup-recheck") { Task { await setup.load() } }
            default:
                EmptyView()
            }
        case .allow:
            EmptyView()
        }
    }

    private func primary(_ title: String, id: String, busy: Bool = false, _ run: @escaping () -> Void) -> some View {
        Button(action: run) {
            Group {
                if busy { ProgressView() } else { Text(title).buttonLabelLine() }
            }
            .frame(maxWidth: .infinity)
        }
        .themedButton(prominent: true)
        .disabled(setup.busy != nil)
        .accessibilityIdentifier(id)
    }

    private func secondary(_ title: String, id: String, _ run: @escaping () -> Void) -> some View {
        Button(action: run) {
            Text(title).buttonLabelLine().frame(maxWidth: .infinity)
        }
        .themedButton()
        .disabled(setup.busy != nil)
        .accessibilityIdentifier(id)
    }

    // MARK: Pieces

    private func hero(_ symbol: String, tint: Color) -> some View {
        ThemedIcon(symbol)
            .font(Theme.Font.display).imageScale(.large)
            .foregroundStyle(tint.gradient)
            .accessibilityHidden(true)
    }

    private func stateChip(_ state: IPhoneSetupState) -> some View {
        switch state {
        case .done: Chip(text: StateName.done.text, look: .tint(Theme.positive))
        case .working: Chip(text: StateName.taskWorking, look: .tint(Theme.accent))
        case .needsYou, .later: Chip(text: StateName.needsYou.text, look: .review(.caution))
        }
    }

    private func stateCard(_ state: IPhoneSetupState, _ text: String) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: Theme.Space.m) {
            if state == .working { ProgressView() }
            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                stateChip(state)
                Text(text).font(Theme.Font.secondary).fixedSize(horizontal: false, vertical: true)
            }
            Spacer(minLength: 0)
        }
        .card()
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("iphone-setup-state")
    }

    private func details(_ text: String) -> some View {
        DisclosureGroup {
            Text(text).font(Theme.Font.label).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                .frame(maxWidth: .infinity, alignment: .leading)
        } label: {
            Text("Details").font(Theme.Font.secondary)
        }
        .accessibilityIdentifier("iphone-setup-details")
    }

    private var safety: some View {
        Label {
            Text("Paying, deleting, sending messages and typing passwords always ask you first. You can turn this off here at any time.")
                .font(Theme.Font.label).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
        } icon: {
            ThemedIcon(Theme.Icon.permissions).foregroundStyle(.secondary)
        }
    }

    private var iosNote: some View {
        Label {
            Text("This iPhone has iOS \(setup.ios), so the assistant can look at the screen and open apps, but can’t tap or type. That needs iOS 27 or later.")
                .font(Theme.Font.secondary).fixedSize(horizontal: false, vertical: true)
        } icon: {
            ThemedIcon(Theme.Icon.error).foregroundStyle(Theme.caution)
        }
        .card()
        .accessibilityIdentifier("iphone-setup-ios-note")
    }
}

/// The guide's words that more than one place shows (the overview, a step's screen, the entry row).
nonisolated enum IPhoneSetupText {
    static var navTitle: String { String(localized: "iPhone Control") }
    static var title: String { String(localized: "Let the Assistant Operate This iPhone") }

    static func stepTitle(_ step: IPhoneSetupStep) -> String {
        switch step {
        case .turnOn: String(localized: "Turn On Phone Control")
        case .computer: String(localized: "Choose a Computer")
        case .pair: String(localized: "Connect and Pair")
        case .developerMode: String(localized: "Turn On Developer Mode")
        case .allow: String(localized: "Allow This iPhone")
        }
    }

    static func stepOf(_ step: IPhoneSetupStep) -> String {
        String(localized: "Step \(step.rawValue + 1) of \(IPhoneSetupStep.allCases.count)")
    }

    static var developerSteps: [String] {
        [String(localized: "Open Settings › Privacy & Security › Developer Mode (near the bottom)"),
         String(localized: "Turn it on and tap Restart"),
         String(localized: "After the restart, unlock, tap Turn On and enter your passcode"),
         String(localized: "Open Persona again: setup picks up here")]
    }

    /// The pairing job's wait, by what it waits for at the phone.
    static func pairing(_ code: String?) -> String {
        switch code {
        case "trust_pending": String(localized: "On this iPhone, tap Trust, then enter your passcode.")
        case "locked": String(localized: "Unlock this iPhone so pairing can go on.")
        default: String(localized: "Pairing with this iPhone…")
        }
    }

    /// A computer's line: what it is and whether it can be used.
    static func computerLine(_ c: IPhoneSetupComputer) -> String {
        switch c.unusable {
        case .notMac: return String(localized: "Not available yet: this needs a Mac with Xcode")
        case .offline: return String(localized: "Mac · offline")
        case nil:
            if c.noXcode { return String(localized: "Mac · needs Xcode from the App Store") }
            return String(localized: "Mac · online")
        }
    }

    /// This iPhone's line: model, iOS and the computer it goes through.
    static func deviceLine(_ d: IPhoneSeen, through: String?) -> String {
        var parts = [String]()
        if let ios = d.device.ios { parts.append(String(localized: "iOS \(ios)")) }
        if let through, !through.isEmpty { parts.append(String(localized: "through \(through)")) }
        return parts.joined(separator: " · ")
    }

    /// The entry row's line: how far setup is.
    static func summary(_ i: IPhoneSetupInput, computer: String?) -> String {
        guard IPhoneSetupFlow.current(i) != nil else {
            return String(localized: "On · through \(computer ?? "")")
        }
        let done = IPhoneSetupFlow.doneCount(i)
        if done == 0 { return String(localized: "Not set up · about 5 minutes") }
        return String(localized: "\(done) of \(IPhoneSetupStep.allCases.count) steps done")
    }
}

/// Settings › Connectors › This iPhone's first section: the way into the guided setup, saying how far it is.
struct IPhoneSetupEntry: View {
    @Environment(AppModel.self) private var app
    @State private var setup = IPhoneSetupModel()

    var body: some View {
        Section {
            NavigationLink(value: SettingsPage.iphoneSetup) {
                HStack(spacing: Theme.Space.m) {
                    IconTile(Theme.Icon.phoneControl, tint: Theme.accent)
                    VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                        Text(IPhoneSetupText.title)
                        if setup.loaded {
                            Text(IPhoneSetupText.summary(setup.input, computer: setup.computer?.name))
                                .font(Theme.Font.label).foregroundStyle(.secondary)
                        }
                    }
                }
                .padding(.vertical, Theme.Space.xxs)
            }
            .accessibilityIdentifier("iphone-setup-entry")
        } header: {
            Text("Control of This iPhone")
        } footer: {
            Text("Through one of your computers, the assistant can see this iPhone’s screen, open apps, tap and type.")
        }
        .task(id: app.config) { await setup.load(computers: false) }
    }
}
