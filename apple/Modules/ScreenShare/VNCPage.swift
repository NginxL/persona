import SwiftUI
#if os(iOS) && canImport(PersonaScreenKit)
import PersonaScreenKit
#endif

/// A Mac's VNC on the whole screen (#1343), where it can be operated: its picture, the touches as the mode says (Mouse /
/// Trackpad), the clipboard both ways, the keyboard with a row of the Mac's shortcuts, Help, and Picture in Picture back to picture in
/// picture. When the agent whose chat opened it waits on the user, 「我完成了」 hands the Mac back (closing does too).
/// On an iPhone a button next to the clipboard turns the page on its side and back (#6196).
struct VNCPage: View {
    @Environment(AppModel.self) private var model
    let ref: MachineRef
    var unlock = false
    var from: SessionKey?
    var onClose: () -> Void
    @State private var session: RFBSession?
    @State private var mode: VNCMode = .mouse
    @State private var help = false
    @State private var typing = false
    @State private var unlocked = false
    /// Which of its ways each rail is in on an iPhone on its side (`fittingRail`, #5289).
    @State private var topRailFit = 0
    @State private var bottomRailFit = 0
    @State private var screenView: VNCScreenUIView?
    /// The picture is zoomed in (a pinch, or ⋯ › Zoom In): ⋯ offers Zoom to Fit instead of Zoom In.
    @State private var zoomed = false
    @State private var modifiers = VNCModifiers()
    /// The display shown alone (`VNCDisplay.id`, kept per Mac, #80); nil: all of them.
    @State private var display: String?
    @State private var pipLog = false
    /// The picture channel for this Mac (#3954): hardware video or VNC. Loaded from the per-Mac pref on appear.
    @State private var picture: ScreenPicture = .vnc
    /// The video channel's quality bias (#3954, interface for #6396), per Mac.
    @State private var quality: ScreenQuality = .auto
    /// Whether the connection-stats overlay is shown (#3954), per Mac, off by default.
    @State private var statsOn = false
    /// The recent round trips to this Mac's hub, timed while the overlay is shown and the app is in front (#6558).
    @State private var hubTrips = HubRoundTrips()
    /// The app is in front: the round trip to the hub is timed only then.
    @State private var appActive = true
    /// The mobile-data question shows (#6396, `ScreenMobileDataCard`).
    @State private var mobileDataCard = false
    /// `-showScreenFallbackDemo`'s reason, for screenshots of the fallback line (#3954); the real one is `fallback`.
    @State private var fallbackDemo: ScreenVideoFallback?
    /// The not-allowed line's "How to turn it on" is showing its one sentence.
    @State private var fallbackHelp = false
    /// The smooth scrolling line's alert is up (#7775).
    @State private var smoothScrollingHelp = false
    /// The app went to the background with the page up (not just Control Center or an alert over it).
    @State private var wentBackground = false
    /// Phone Screen (#9314): this page's portrait display on the Mac and the window sheet.
    @State private var phone: PhoneScreenLink?
    @State private var phoneWindows = false
    /// The display's shape (#9500): the picture's size in points with the keyboard down, read until the display is up and
    /// then kept. Only the phone turning reads it again; the keyboard, the shortcut row and the lines under the bars,
    /// which change the picture's height for a while, never do, so the Mac never rebuilds the display for them.
    @State private var phoneShape: PhoneShape?
    /// The system keyboard is on screen (it can come up without the Keyboard button, from the key catcher), and the
    /// picture's last size, read again for the shape once the keyboard is down (the phone may have turned meanwhile).
    @State private var keyboardUp = false
    @State private var pictureSize = CGSize.zero
    /// When the app came back from the background with the connection not (yet) known to be down: a drop reported
    /// within `returnWindow` is the suspension's, so it reconnects once by itself (#4243).
    @State private var returnedAt: Date?
    private static let returnWindow: TimeInterval = 5
    /// When the app came back with the connection still saying live, and which connection was asked: the whole screen
    /// is asked for, and no frame within `frameWait` means it died quietly while suspended, so it reconnects once (#4380).
    @State private var askedFrameAt: Date?
    @State private var askedConnection = 0
    /// Long enough for a whole Retina frame over the hub's relay on a slow network: a healthy connection is not dropped.
    private static let frameWait: Duration = .seconds(10)

    private var name: String { model.machine(ref)?.name ?? ref.id }
    /// The agent that opened it waits on the user: there is something to hand back.
    private var waiting: Bool { from.flatMap { model.session($0) }?.state == .needsYou }
    @Environment(\.verticalSizeClass) private var verticalSize
    /// An iPhone on its side (#3075): the bars become rails down either side, so the Mac's picture gets the whole height.
    private var railed: Bool { verticalSize == .compact }

    var body: some View {
        // One layout either way round: the picture (and the keyboard's catcher) stay the same views as the phone turns.
        let layout = railed ? AnyLayout(HStackLayout(spacing: 0)) : AnyLayout(VStackLayout(spacing: 0))
        layout {
            topBar
            VStack(spacing: 0) {
                TimelineView(.periodic(from: .now, by: 1)) { ctx in
                    if let a = model.computerActivities[ref], a.shown(at: ctx.date) { stepLine(a) }
                }
                if let f = fallback { fallbackBanner(f) }
                if session?.askedForSmoothScrolling == true, model.screenInput(ref)?.granted != true { smoothScrollingLine }
                #if os(iOS) && canImport(PersonaScreenKit)
                if mobileDataCard || session?.video.askMobileData == true {
                    ScreenMobileDataCard(hotspot: session?.video.onHotspot == true) { choice in
                        withAnimation(Theme.Motion.standard) {
                            mobileDataCard = false
                            if let s = session { s.video.chooseMobileData(choice) } else { ScreenMobileData.choose(choice) }
                        }
                    } close: {
                        withAnimation(Theme.Motion.standard) {
                            mobileDataCard = false
                            session?.video.dismissMobileData()
                        }
                    }
                    .transition(.opacity)
                }
                #endif
                ZStack {
                    Color.black
                    if let s = session { content(s) }
                    // Over the picture, not above it: a line that came and went would change the picture's shape, and
                    // with it the display's on the Mac.
                    if activeMode == .phone, let line = phoneLine {
                        phoneStatusLine(line)
                            .padding(.vertical, Theme.Space.xs)
                            .background(Theme.Fill.scrim)
                            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
                    }
                    if statsOn, let stats = currentStats {
                        ScreenStatsOverlay(stats: stats) { statsOn = false; ScreenStatsPref.set(false, for: ref) }
                            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topTrailing)
                    }
                }
                .onGeometryChange(for: CGSize.self) { $0.size } action: { size in
                    pictureSize = size
                    readPhoneShape(size)
                }
                // Over the keyboard while it is up (the bars ride above it).
                if typing, let s = session { VNCShortcutBar(session: s, modifiers: modifiers) }
            }
            bottomBar
        }
        .background(Color.black.ignoresSafeArea())
        .environment(\.colorScheme, .dark)
        .devScreen("screen")
        .onAppear {
            session = VNC.hold(ref)
            mode = VNCMode.of(ref)
            #if os(iOS)
            // Back from the small window (Restore): the Phone Screen it kept on the Mac, its window still there.
            if phone == nil { phone = VNCPiP.shared.takePhone(ref) ?? PhoneScreenLink(ref: ref) }
            #elseif os(visionOS)
            if phone == nil { phone = PhoneScreenLink(ref: ref) }
            #endif
            display = VNCDisplayChoice.of(ref)
            // Picture defaults to video on a Mac whose node offers it (it falls back to VNC by itself when it can't show);
            // a saved choice always wins.
            picture = ScreenPicture.of(ref) ?? (videoAdvertised ? .video : .vnc)
            quality = ScreenQuality.of(ref)
            statsOn = ScreenStatsPref.of(ref)
            #if DEBUG
            let args = ProcessInfo.processInfo.arguments
            // `-showScreenVideoDemo` stands in for a node that advertises the video channel (videoAdvertised), and picks
            // the video channel, so a screenshot shows the Picture item (Quality also needs a helper with tiers).
            if args.contains("-showScreenVideoDemo") { picture = .video }
            if args.contains("-showScreenStatsDemo") { statsOn = true }
            if args.contains("-showScreenFallbackDemo") { fallbackDemo = .lost }
            // `-showScreenMobileData`: the mobile-data question, for screenshots (#6396).
            #if os(iOS) && canImport(PersonaScreenKit)
            if args.contains("-showScreenMobileData") { mobileDataCard = true }
            #endif
            #endif
            #if os(iOS) && canImport(PersonaScreenKit)
            session?.video.setQuality(quality) // before the stream opens: its first tier follows the choice (#6396)
            session?.video.want(videoWant)
            #endif
        }
        #if os(iOS) && canImport(PersonaScreenKit)
        .onChange(of: videoWant) { _, want in session?.video.want(want) }
        #endif
        #if os(iOS)
        .task(id: hubProbing) { await probeHub() }
        .onReceive(NotificationCenter.default.publisher(for: UIApplication.didEnterBackgroundNotification)) { _ in appActive = false }
        .onReceive(NotificationCenter.default.publisher(for: UIResponder.keyboardWillShowNotification)) { _ in keyboardUp = true }
        .onReceive(NotificationCenter.default.publisher(for: UIResponder.keyboardDidHideNotification)) { _ in
            keyboardUp = false
            readPhoneShape(pictureSize)
        }
        .onReceive(NotificationCenter.default.publisher(for: UIApplication.didBecomeActiveNotification)) { _ in appActive = true }
        #endif
        .onDisappear {
            #if os(iOS)
            VNCPiP.shared.pagePicture(nil)
            // Phone Screen stays on the Mac while the small window has this Mac, which shows its portrait display; else
            // it ends (the Mac puts its windows back and lets the display go) and the small window shows all of the picture.
            let kept = phone.map { VNCPiP.shared.keepPhone($0) } ?? false
            if !kept { session?.cutSmallWindow(nil) }
            VNCPiP.shared.pageGone(ref)
            #elseif os(visionOS)
            phone?.stop() // the Mac puts its windows back and lets the display go
            #endif
            VNC.release(ref)
        }
        .onChange(of: session?.phase) { _, phase in
            #if os(iOS)
            // The small window ready (#1839): Picture in Picture starts it in the tap, and going home starts it by itself.
            if phase == .live { VNCPiP.shared.prepare(ref, from: from, automatic: true) }
            #endif
            if phase == .live, unlock, !unlocked {
                unlocked = true
                session?.unlockWithKeptPassword()
            }
            if case .closed? = phase, let at = returnedAt, Date.now.timeIntervalSince(at) < Self.returnWindow {
                returnedAt = nil
                session?.start()
            }
        }
        // Back from the background: the suspended app's connection is usually gone. Reconnect instead of waiting on
        // Reconnect being tapped; a sign-in asked for, or a connection still up, is left as it is.
        .onReceive(NotificationCenter.default.publisher(for: UIApplication.didEnterBackgroundNotification)) { _ in
            wentBackground = true
            #if os(iOS) && canImport(PersonaScreenKit)
            session?.video.hold("background", true) // the small window, if any, shows RFB's picture meanwhile
            #endif
        }
        .onReceive(NotificationCenter.default.publisher(for: UIApplication.didBecomeActiveNotification)) { _ in
            #if os(iOS) && canImport(PersonaScreenKit)
            session?.video.hold("background", false)
            #endif
            guard wentBackground, let s = session else { return }
            wentBackground = false
            if case .closed = s.phase {
                returnedAt = nil
                s.start()
            } else {
                returnedAt = .now
                if case .live = s.phase {
                    askedConnection = s.connection
                    askedFrameAt = .now
                    s.refresh()
                }
            }
        }
        .task(id: askedFrameAt) {
            guard let at = askedFrameAt else { return }
            try? await Task.sleep(for: Self.frameWait)
            guard !Task.isCancelled else { return }
            defer { askedFrameAt = nil }
            // Only the connection that was asked, still saying live: one started since (a reconnect, a sign-in) is new.
            guard let s = session, s.connection == askedConnection, case .live = s.phase else { return }
            if (s.lastFrame ?? .distantPast) < at { s.reconnect() }
        }
        // Phone Screen (#9314): on while the mode is and the screen is live, for this connection at the picture's shape.
        .task(id: phoneWant) {
            guard let phone else { return }
            // A page just opened on a Phone Screen that is on (Restore from the small window, #9659) has not read the
            // picture's shape yet: the Mac's display and window stay until it has.
            if let w = phoneWant { phone.start(pair: w.pair, size: w.size) } else if !phoneShapePending { phone.stop() }
        }
        #if os(iOS)
        // The small window shows what the page does in Phone Screen: the portrait display alone, at its own pixels.
        .onChange(of: smallWindowCut) { _, cut in session?.cutSmallWindow(cut?.rect) }
        #endif
        .onChange(of: phone?.phase) { _, phase in
            // Up for the first time with nothing on it: the window list, to pick what goes there.
            if phase == .on, phone?.placed == nil { phoneWindows = true }
        }
        .sheet(isPresented: $phoneWindows) {
            if let phone { PhoneScreenWindows(link: phone, name: name) }
        }
        .sheet(isPresented: $help) { VNCHelp() }
        #if os(iOS)
        .sheet(isPresented: $pipLog) { VNCPiPDiagnostics() }
        #endif
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("screen-drawer")
    }

    // MARK: Bars

    /// The bottom bar: a row across the page, or on an iPhone on its side a rail down it, closer together when it does
    /// not fit (the keyboard up), and scrolling when even that does not (the largest text sizes).
    @ViewBuilder private func bar<Content: View>(spacing: CGFloat, @ViewBuilder _ content: @escaping () -> Content) -> some View {
        if railed {
            fittingRail([RailWay(), RailWay(tight: true), RailWay(tight: true, scrolls: true)], spacing: spacing, fit: $bottomRailFit) { _ in
                content()
            }
        } else {
            row(spacing: spacing, content)
        }
    }

    /// A row of buttons across the page.
    private func row<Content: View>(spacing: CGFloat, @ViewBuilder _ content: () -> Content) -> some View {
        HStack(spacing: spacing) { content() }
            .padding(.horizontal, Theme.Space.page)
            .padding(.vertical, Theme.Space.s)
    }

    /// One way a rail can sit down the page's side: closer together, Picture in Picture in ⋯, scrolling.
    private struct RailWay {
        var tight = false, pipButton = true, scrolls = false
    }

    /// A rail down the page's side in the first of `ways` that fits its height, as `ViewThatFits` picks, but drawn as the
    /// same views whichever it is (#5289). Opening a menu from a rail hides the keyboard, the page gets taller and a
    /// roomier way fits; had that put other views in, the menu's morph out of its button (iOS 26) would find the button
    /// gone and stop the app. So hidden copies of the ways are measured, the one picked says so, and the rail shown only
    /// changes its spacing, what `content` does with the way, and whether it scrolls.
    private func fittingRail<Content: View>(_ ways: [RailWay], spacing: CGFloat, fit: Binding<Int>,
                                            @ViewBuilder _ content: @escaping (RailWay) -> Content) -> some View {
        let way = ways[min(max(fit.wrappedValue, 0), ways.count - 1)]
        return ViewThatFits(in: .vertical) {
            ForEach(0..<ways.count, id: \.self) { i in
                Group {
                    if ways[i].scrolls {
                        ScrollView(.vertical) { rail(spacing: spacing, tight: ways[i].tight) { content(ways[i]) } }
                    } else {
                        rail(spacing: spacing, tight: ways[i].tight) { content(ways[i]) }
                    }
                }
                .onAppear { fit.wrappedValue = i }
            }
        }
        .hidden()
        .accessibilityHidden(true)
        .overlay {
            GeometryReader { g in
                ScrollView(.vertical) {
                    rail(spacing: spacing, tight: way.tight) { content(way) }
                        .frame(height: way.scrolls ? nil : g.size.height)
                }
                .scrollDisabled(!way.scrolls)
                .scrollIndicators(.hidden)
            }
        }
    }

    /// A rail's buttons down the page's side, `tight` closer together.
    private func rail<Content: View>(spacing: CGFloat, tight: Bool, @ViewBuilder _ content: () -> Content) -> some View {
        VStack(spacing: tight ? Theme.Space.xxs : spacing) { content() }
            .padding(.horizontal, Theme.Space.s)
            .padding(.vertical, tight ? Theme.Space.xxs : Theme.Space.s)
    }

    @ViewBuilder private var topBar: some View {
        #if os(iOS)
        let pip = VNCPiP.supported
        #else
        let pip = false
        #endif
        if railed {
            // Down the leading rail, Picture in Picture under Close (#3075). Short of room (the keyboard up) the buttons
            // close up first, then (the largest text sizes) Picture in Picture goes into ⋯ so Close, Help and ⋯ stay in
            // sight, and only then does the rail scroll.
            fittingRail([RailWay(), RailWay(tight: true), RailWay(pipButton: false), RailWay(tight: true, pipButton: false),
                         RailWay(tight: true, pipButton: false, scrolls: true)], spacing: Theme.Space.s, fit: $topRailFit) { way in
                topButtons(pipButton: pip && way.pipButton, nameInRow: false)
            }
        } else {
            // One line where it fits, Picture in Picture right of Close (#3075). At the largest text sizes the name goes
            // under the buttons rather than being squeezed to a letter, and where the buttons themselves don't fit,
            // Picture in Picture goes into ⋯ as before.
            ViewThatFits(in: .horizontal) {
                topRow(pipButton: pip, nameBelow: false)
                topRow(pipButton: pip, nameBelow: true)
                topRow(pipButton: false, nameBelow: false)
                topRow(pipButton: false, nameBelow: true)
            }
        }
    }

    private func topRow(pipButton: Bool, nameBelow: Bool) -> some View {
        VStack(spacing: .zero) {
            row(spacing: Theme.Space.s) { topButtons(pipButton: pipButton, nameInRow: !nameBelow) }
            if nameBelow {
                nameAndPhase
                    .padding(.horizontal, Theme.Space.page)
                    .padding(.bottom, Theme.Space.s)
            }
        }
    }

    /// The top bar's buttons, across the page or down its leading rail; Picture in Picture next to Close or in ⋯.
    @ViewBuilder private func topButtons(pipButton: Bool, nameInRow: Bool) -> some View {
        Button { close() } label: { barIcon(Theme.Icon.close) }
            .themedButton()
            .accessibilityLabel("Close Screen")
            .accessibilityIdentifier("screen-close")
        if pipButton {
            Button { toPictureInPicture() } label: { barIcon(Theme.Icon.pictureInPicture) }
                .themedButton()
                .accessibilityLabel("Picture in Picture")
                .accessibilityIdentifier("vnc-pip")
        }
        Spacer(minLength: Theme.Space.s)
        if railed {
            // No room for the name down a rail: the dot, with the name and the phase for VoiceOver.
            Circle().fill(dot).scaledFrame(width: Theme.Size.dot, height: Theme.Size.dot)
                .accessibilityElement()
                .accessibilityLabel(name)
                .accessibilityValue(phaseLabel)
                .accessibilityIdentifier("screen-phase")
            Spacer(minLength: Theme.Space.s)
        } else if nameInRow {
            // Whatever the buttons leave, not a third of it beside the two spacers.
            nameAndPhase.layoutPriority(1)
            Spacer(minLength: Theme.Space.s)
        }
        Button { help = true } label: { barIcon(Theme.Icon.help) }
            .themedButton()
            .accessibilityLabel("Help")
            .accessibilityIdentifier("vnc-help")
        moreMenu(pipItem: !pipButton)
    }

    /// The dot, the Mac's name and the stage. Measured (`ViewThatFits`) as the dot, the name's first four letters and the
    /// stage, so a row is picked only where at least that much reads ("MacB…"); laid out, it takes the room it is given
    /// and the name is cut short, the stage kept whole.
    private var nameAndPhase: some View {
        let least = name.count > 4 ? String(name.prefix(4)) + "…" : name
        return nameLine(least).hidden().accessibilityHidden(true)
            .frame(maxWidth: .infinity)
            .overlay {
                nameLine(name)
                    .accessibilityElement(children: .combine)
                    .accessibilityIdentifier("screen-phase")
            }
    }

    private func nameLine(_ shown: String) -> some View {
        HStack(spacing: Theme.Space.xs) {
            Circle().fill(dot).scaledFrame(width: Theme.Size.dot, height: Theme.Size.dot)
            Text(shown).font(Theme.Font.bodyStrong).lineLimit(1)
            Text(phaseLabel).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1).layoutPriority(1)
        }
    }

    /// A top-bar button's glyph in one square for every button: a themed button is sized by its label, and glyphs differ
    /// in height (⋯ is a few points tall).
    private func barIcon(_ name: String) -> some View {
        ThemedIcon(name).font(Theme.Font.labelStrong).scaledFrame(width: Theme.Size.icon, height: Theme.Size.icon)
    }

    private func moreMenu(pipItem: Bool) -> some View {
        Menu {
            Picker("Mode", selection: Binding(get: { mode }, set: { mode = $0; VNCMode.set($0, for: ref) })) {
                Label("Mouse Mode", systemImage: Theme.Icon.pointer).tag(VNCMode.mouse)
                Label("Trackpad Mode", systemImage: Theme.Icon.trackpad).tag(VNCMode.trackpad)
                if phoneOffered {
                    Label("Phone Screen", systemImage: Theme.Icon.phoneScreen).tag(VNCMode.phone)
                }
            }
            .accessibilityIdentifier("vnc-mode")
            // Picture channel and its quality bias (#3954), remembered per Mac — only when this Mac's node advertises the
            // video channel, so no Mac shows a control it can't use (the switcher stays hidden until it does). Input stays
            // on RFB in both; only the picture path changes. Video falls back to VNC on its own and says why on the page.
            // Quality only once the helper says it has tiers (#6396): before that the choice would change nothing.
            if videoAdvertised {
                Picker(selection: Binding(get: { picture }, set: { picture = $0; ScreenPicture.set($0, for: ref) })) {
                    ForEach(ScreenPicture.allCases, id: \.self) { p in Label(p.label, systemImage: p.icon).tag(p) }
                } label: {
                    Label("Picture", systemImage: Theme.Icon.video)
                }
                .pickerStyle(.menu)
                .accessibilityIdentifier("screen-picture")
                if picture == .video, qualityOffered {
                    Picker(selection: Binding(get: { quality }, set: { q in
                        quality = q
                        ScreenQuality.set(q, for: ref)
                        #if os(iOS) && canImport(PersonaScreenKit)
                        session?.video.setQuality(q)
                        #endif
                    })) {
                        ForEach(ScreenQuality.allCases, id: \.self) { q in Text(q.label).tag(q) }
                    } label: {
                        Label("Quality", systemImage: Theme.Icon.pluginGauge)
                    }
                    .pickerStyle(.menu)
                    .accessibilityIdentifier("screen-quality")
                }
            }
            Toggle(isOn: Binding(get: { statsOn }, set: { statsOn = $0; ScreenStatsPref.set($0, for: ref) })) {
                Label("Connection Stats", systemImage: Theme.Icon.pluginChart)
            }
            .accessibilityIdentifier("screen-stats-toggle")
            let displays = offeredDisplays
            if displays.count > 1, activeMode != .phone {
                Picker(selection: Binding(get: { shownDisplay?.id }, set: { display = $0; VNCDisplayChoice.set($0, for: ref) })) {
                    Text("All Displays").tag(String?.none)
                    ForEach(Array(displays.enumerated()), id: \.element.id) { i, d in
                        Text("Display \(i + 1)").tag(Optional(d.id))
                    }
                } label: {
                    Label("Displays", systemImage: Theme.Icon.displays)
                }
                .pickerStyle(.menu)
                .accessibilityIdentifier("vnc-displays")
            }
            // The only zoom in Trackpad Mode, which has no pinch (#6196); one tap back to the whole screen in either.
            if zoomed {
                Button { screenView?.zoomToFit() } label: { Label("Zoom to Fit", systemImage: Theme.Icon.zoomOut) }
                    .accessibilityIdentifier("vnc-zoom-fit")
            } else {
                Button { screenView?.zoomIn() } label: { Label("Zoom In", systemImage: Theme.Icon.zoomIn) }
                    .accessibilityIdentifier("vnc-zoom-in")
            }
            Button { centerPointer() } label: { Label("Center Pointer", systemImage: Theme.Icon.pointerCenter) }
            #if os(iOS)
            // Picture in Picture itself is the button right of Close, here only when the row has no room for it.
            if pipItem, VNCPiP.supported {
                Button { toPictureInPicture() } label: { Label("Picture in Picture", systemImage: Theme.Icon.pictureInPicture) }
            }
            Button { pipLog = true } label: { Label("Picture in Picture Diagnostics", systemImage: Theme.Icon.help) }
            #elseif os(visionOS)
            if VNCPiP.supported {
                Button { toPictureInPicture() } label: { Label("Picture in Picture", systemImage: Theme.Icon.pictureInPicture) }
            }
            Button { pipLog = true } label: { Label("Picture in Picture Diagnostics", systemImage: Theme.Icon.help) }
            #endif
            #if DEBUG
            if let s = session { Text(String(format: "%.0f 帧/秒 · %.1f Mbit/s", s.fps, s.mbps) + (s.latency.map { String(format: " · %.0f 毫秒", $0) } ?? "")) } // i18n-ignore
            #endif
        } label: {
            barIcon(Theme.Icon.more)
        }
        .themedButton()
        .accessibilityLabel("More")
        .accessibilityIdentifier("vnc-more")
    }

    private var bottomBar: some View {
        bar(spacing: Theme.Space.m) {
            Menu {
                Button { Task { await pasteFromPhone(andPaste: true) } } label: { Label(Self.fromPhone, systemImage: Theme.Icon.paste) }
                Button { Task { await pasteFromPhone(andPaste: false) } } label: { Label("Copy to Mac's Clipboard Only", systemImage: Theme.Icon.clipboard) }
                Button { Task { await copyToPhone() } } label: { Label(Self.toPhone, systemImage: Theme.Icon.copy) }
            } label: {
                ThemedIcon(Theme.Icon.clipboard).font(Theme.Font.heading).scaledFrame(width: Theme.Size.icon, height: Theme.Size.icon)
            }
            .themedButton()
            .accessibilityLabel("Clipboard")
            .accessibilityIdentifier("vnc-clipboard")
            #if os(iOS)
            if AppOrientation.phone { rotateButton }
            #endif
            if activeMode == .phone {
                Button { phoneWindows = true } label: {
                    ThemedIcon(Theme.Icon.macWindows).font(Theme.Font.heading).scaledFrame(width: Theme.Size.icon, height: Theme.Size.icon)
                }
                .themedButton()
                .disabled(phone?.phase != .on)
                .accessibilityLabel("Windows")
                .accessibilityIdentifier("phone-screen-windows-button")
            }
            Spacer(minLength: Theme.Space.s)
            if waiting {
                Button { handBack() } label: {
                    if railed {
                        Label("I'm Done", systemImage: Theme.Icon.handBack).labelStyle(.iconOnly).font(Theme.Font.heading)
                            .scaledFrame(width: Theme.Size.icon, height: Theme.Size.icon)
                    } else {
                        Label("I'm Done", systemImage: Theme.Icon.handBack).font(Theme.Font.labelStrong).lineLimit(1)
                    }
                }
                .themedButton(prominent: true)
                .accessibilityIdentifier("vnc-done")
                Spacer(minLength: Theme.Space.s)
            }
            Button { typing.toggle() } label: {
                ThemedIcon(Theme.Icon.keyboard).font(Theme.Font.heading).scaledFrame(width: Theme.Size.icon, height: Theme.Size.icon)
            }
            .themedButton(prominent: typing)
            .accessibilityLabel("Keyboard")
            .accessibilityIdentifier("vnc-keyboard")
        }
        .background { if let s = session { VNCKeyCatcher(session: s, modifiers: modifiers, typing: $typing).frame(width: Theme.Size.hairline, height: Theme.Size.hairline) } }
    }

    #if os(iOS)
    /// Next to the clipboard (#6196): upright, it turns the page on its side; on its side (down the trailing rail, under
    /// the clipboard), back upright. Only on an iPhone: an iPad is not made to turn.
    private var rotateButton: some View {
        Button { ScreenWindow.shared.turn() } label: {
            ThemedIcon(railed ? Theme.Icon.rotateToPortrait : Theme.Icon.rotateToLandscape).font(Theme.Font.heading)
                .scaledFrame(width: Theme.Size.icon, height: Theme.Size.icon)
        }
        .themedButton()
        .accessibilityLabel(railed ? String(localized: "Rotate to Portrait") : String(localized: "Rotate to Landscape"))
        .accessibilityIdentifier("vnc-rotate")
    }
    #endif

    static let fromPhone = String(localized: "Paste from iPhone"), toPhone = String(localized: "Copy to iPhone")

    private func stepLine(_ a: ComputerActivity) -> some View {
        Text("Now: \(a.step)").font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1)
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.horizontal, Theme.Space.page)
            .padding(.bottom, Theme.Space.xs)
            .accessibilityIdentifier("screen-step")
    }

    /// Pixel scrolling (#7775): this connection's first two-finger scroll had the Mac's node ask macOS for Persona Input's
    /// permission, so a window is now on the Mac's screen. Under the bar, as the video's fallback line: what it is for in
    /// a line, the way through it on a tap; it goes once the Mac allows it. Scrolling works meanwhile, by notches.
    private var smoothScrollingLine: some View {
        Button { smoothScrollingHelp = true } label: {
            HStack(alignment: .firstTextBaseline, spacing: Theme.Space.xs) {
                ThemedIcon(Theme.Icon.info).font(Theme.Font.caption).foregroundStyle(Theme.caution)
                Text("To scroll more smoothly, allow Persona Input on this Mac.").font(Theme.Font.label).foregroundStyle(.secondary)
                    .lineLimit(3).fixedSize(horizontal: false, vertical: true)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.horizontal, Theme.Space.page)
            .padding(.bottom, Theme.Space.xs)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityHint(Text("How to turn it on"))
        .accessibilityIdentifier("screen-smooth-scrolling")
        .alert("Turn On Smooth Scrolling", isPresented: $smoothScrollingHelp) {
            Button("OK", role: .cancel) {}
        } message: {
            Text("In the window that just opened on this Mac, choose Open System Settings, then turn on Persona Input (Privacy & Security › Accessibility). Until then, scrolling still works.")
        }
    }

    /// A one-line reason under the bars when the video channel falls back to VNC (#3954).
    private func fallbackBanner(_ f: ScreenVideoFallback) -> some View {
        let line = HStack(alignment: .firstTextBaseline, spacing: Theme.Space.xs) {
            ThemedIcon(Theme.Icon.info).font(Theme.Font.caption).foregroundStyle(Theme.caution)
            Text(Self.fallbackLine(f)).font(Theme.Font.label).foregroundStyle(.secondary)
                .lineLimit(3).fixedSize(horizontal: false, vertical: true)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, Theme.Space.page)
        .padding(.bottom, Theme.Space.xs)
        return Group {
            if f == .notAllowed {
                // The one reason the user can fix themselves: the whole line is the way to the one sentence saying where
                // (a whole-width target, and "How to turn it on" wraps with the words at any text size).
                Button { fallbackHelp = true } label: { line.contentShape(.rect) }
                    .buttonStyle(.plain)
                    .accessibilityHint(Text("How to turn it on"))
                    .accessibilityIdentifier("screen-fallback-help")
            } else {
                line.accessibilityElement(children: .combine).accessibilityIdentifier("screen-fallback")
            }
        }
        .alert("Turn On Video for This Mac", isPresented: $fallbackHelp) {
            Button("OK", role: .cancel) {}
        } message: {
            Text(ScreenVideoFallback.howToAllow(permission: model.screenVideo(ref)?.permission, asked: model.screenVideo(ref)?.asked ?? false))
        }
    }

    /// The fallback's words; the not-allowed one ends with "How to turn it on" in the accent color.
    private static func fallbackLine(_ f: ScreenVideoFallback) -> AttributedString {
        var line = AttributedString(f.message)
        if f == .notAllowed {
            var how = AttributedString(String(localized: "How to turn it on"))
            how.foregroundColor = Theme.accent
            line += AttributedString(" ") + how
        }
        return line
    }

    /// The reading the stats overlay draws (#3954): the video channel's while its picture shows, else RFB's. A
    /// `-showScreenStatsDemo` build shows sample video-mode numbers.
    private var currentStats: ScreenStats? {
        #if DEBUG
        let args = ProcessInfo.processInfo.arguments
        if args.contains("-showScreenStatsDemo") {
            if args.contains("-showScreenX264Demo") { return .demoVideoX264 }
            if args.contains("-showScreenX264OffDemo") { return .demoVideoX264Off }
            return args.contains("-showScreenUDP") ? .demoVideoUDP : .demoVideo
        }
        #endif
        guard let s = session else { return nil }
        #if os(iOS) && canImport(PersonaScreenKit)
        let route = Self.route(s.video)
        if s.video.showing {
            let v = s.video, r = v.reading
            let tier = v.tier.map { ScreenTierText.value(quality, $0.tier) }
            let why = v.tier.flatMap { c in v.tierAt.map { ScreenTierText.reason(c.reason, at: $0, now: .now) } }
            return ScreenStats(mode: .video, path: route.path, routeRttMs: route.rtt, routeLoss: route.loss, routeReason: route.reason,
                               tier: tier, tierReason: why, bitrateMbps: r.mbps, fps: r.fps, codec: v.codec,
                               resolution: v.size.map { "\(Int($0.width))×\(Int($0.height))" }, captureMs: r.captureMs,
                               encodeMs: r.encodeMs, networkMs: r.networkMs, decodeMs: r.decodeMs, totalMs: r.totalMs,
                               encoder: v.encoder, encoderNote: v.encoderNote)
        }
        return ScreenStats(mode: .vnc, path: route.path, routeRttMs: route.rtt, routeLoss: route.loss, routeReason: route.reason,
                           bitrateMbps: s.mbps > 0 ? s.mbps : nil,
                           fps: s.fps > 0 ? s.fps : nil,
                           roundTripMs: s.latency,
                           hubRoundTripMs: hubTrips.median(at: .now))
        #else
        return ScreenStats(mode: .vnc, path: .relay,
                           bitrateMbps: s.mbps > 0 ? s.mbps : nil,
                           fps: s.fps > 0 ? s.fps : nil,
                           roundTripMs: s.latency,
                           hubRoundTripMs: hubTrips.median(at: .now))
        #endif
    }

    #if os(iOS) && canImport(PersonaScreenKit)
    /// The overlay's route (#6398): which one, its numbers, and why it last changed. Without a screen session: the hub.
    /// While the video rides a UDP flow (#7873) the row says so, with the flow's own round trip and loss.
    private static func route(_ v: ScreenVideoLink) -> (path: ScreenStats.Path, rtt: Double?, loss: Double?, reason: String?) {
        let kind = v.link?.kind ?? v.route?.to.kind ?? .relay
        let udp = v.route?.to.transport == .udp
        let path: ScreenStats.Path = switch kind {
        case .lan: udp ? .directLANUDP : .directLAN
        case .tailscale: udp ? .directTailscaleUDP : .directTailscale
        case .ipv6, .relay: .relay
        }
        let reason: String? = switch v.route?.reason {
        case .faster?: ScreenRouteReason.text(.faster)
        case .lost?: ScreenRouteReason.text(.lost)
        case .degraded?: ScreenRouteReason.text(.degraded)
        case .networkChanged?: ScreenRouteReason.text(.networkChanged)
        case .directOff?: ScreenRouteReason.text(.turnedOff)
        case .start?, nil: v.directUnavailable == "firewall" ? ScreenRouteReason.text(.firewall) : nil
        }
        if udp, kind != .relay { return (path, v.udp?.rttMs ?? v.link?.srttMs, v.udp?.loss, reason) }
        return (path, v.link?.srttMs, v.link?.packetLoss, reason)
    }
    #endif

    #if os(iOS)
    /// Whether the round trip to the hub is timed (#6558): the stats overlay shown on a live screen, with the app in
    /// front. A background app pings nothing.
    private var hubProbing: Bool { statsOn && appActive && session?.phase == .live }

    /// Pings this Mac's hub on the app's own connection to it every 2 s while `hubProbing` (the task restarts when it
    /// changes). A ping without a pong counts nothing.
    private func probeHub() async {
        guard hubProbing, let live = model.link(ref.group)?.live else { return }
        while !Task.isCancelled {
            if let rtt = await live.roundTrip() { hubTrips.add(ms: rtt * 1000, at: .now) }
            try? await Task.sleep(for: .seconds(2))
        }
    }
    #endif

    /// Why the page shows VNC while video is chosen (#3954); nil when there is nothing to explain. Only while VNC is
    /// live: while it connects or is closed, the page says so itself.
    private var fallback: ScreenVideoFallback? {
        #if DEBUG
        if let fallbackDemo { return fallbackDemo }
        #endif
        #if os(iOS) && canImport(PersonaScreenKit)
        guard picture == .video, videoAdvertised, let s = session, s.phase == .live else { return nil }
        if model.screenVideo(ref)?.permission == "missing" { return .notAllowed }
        return s.video.fallback
        #else
        return nil
        #endif
    }

    #if os(iOS) && canImport(PersonaScreenKit)
    /// What the page asks of the video channel (#3954): video chosen, a Mac that offers and allows it, and one display
    /// shown — its only one, or one chosen of several the node numbered (`display-<id>`); nil keeps VNC (all of several
    /// displays at once stay RFB's, since video carries one).
    private var videoWant: ScreenVideoLink.Want? {
        guard picture == .video, videoAdvertised, model.screenVideo(ref)?.permission != "missing" else { return nil }
        if let d = shownDisplay {
            guard d.id.hasPrefix("display-"), let id = Int(d.id.dropFirst("display-".count)) else { return nil }
            return .init(display: id, rect: d.frame)
        }
        return offeredDisplays.isEmpty ? .init(display: nil, rect: nil) : nil
    }
    #endif

    /// Whether this Mac's node advertises the hardware video channel (#3954): the Picture item appears only then, so no
    /// Mac shows a control it can't use (Quality also waits for `qualityOffered`). A `-showScreenVideoDemo` build forces
    /// it on for screenshots.
    private var videoAdvertised: Bool {
        #if DEBUG
        if ProcessInfo.processInfo.arguments.contains("-showScreenVideoDemo") { return true }
        #endif
        return model.screenVideoAdvertised(ref)
    }

    /// Whether ⋯ › Quality is offered: once this session's helper said it has quality tiers (#6396, `hello.features`)
    /// and this app has its side of them (`ScreenTiers`). Before that the choice would change nothing, so it is not shown.
    private var qualityOffered: Bool {
        #if os(iOS) && canImport(PersonaScreenKit)
        return ScreenTiers.offered(helperHasTiers: session?.video.tiers == true)
        #else
        return false
        #endif
    }

    // MARK: Content

    @ViewBuilder private func content(_ s: RFBSession) -> some View {
        switch s.phase {
        case .live:
            #if os(iOS)
            // The small window's source lies over this picture, and the picture goes into it as it starts (#6333):
            // hidden while the small window has it, also as the page comes back on Restore until it has grown back.
            // It fades as it goes: the system's copy comes over it a few frames after the start, and gone at once it
            // left a blank there until then. It is back at once, where the small window has just grown into it.
            let handedOver = model.vncPiP == ref
            VNCScreen(session: s, mode: activeMode, interactive: true, region: shownDisplay?.frame, regionID: shownDisplay?.id,
                      fillsWidth: activeMode == .phone && !railed,
                      onView: { v in DispatchQueue.main.async { screenView = v; zoomed = false } }, onZoom: { zoomed = $0 },
                      onPicture: { VNCPiP.shared.pagePicture($0) })
                .opacity(handedOver ? 0 : 1)
                .animation(handedOver ? Theme.Motion.conceal : nil, value: handedOver)
            #else
            VNCScreen(session: s, mode: activeMode, interactive: true, region: shownDisplay?.frame, regionID: shownDisplay?.id,
                      fillsWidth: activeMode == .phone && !railed,
                      onView: { v in DispatchQueue.main.async { screenView = v; zoomed = false } }, onZoom: { zoomed = $0 })
            #endif
        case .signIn(let failed):
            let form = VNCSignIn(name: name, account: Self.account(model.machine(ref)?.home), failed: failed) { account, password, remember in
                s.signIn(account: account, password: password, remember: remember)
            }
            if railed {
                // On its side the form is taller than the page (more so with the keyboard up for the password): it scrolls.
                GeometryReader { g in
                    ScrollView { form.frame(maxWidth: .infinity, minHeight: g.size.height) }
                        .scrollBounceBehavior(.basedOnSize)
                }
            } else {
                form
            }
        case .connecting:
            VStack(spacing: Theme.Space.s) {
                ProgressView()
                Text("Connecting to \(name)’s screen…").font(Theme.Font.secondary).foregroundStyle(.secondary)
            }
        case .closed(let why):
            VStack(spacing: Theme.Space.m) {
                Text(why ?? String(localized: "Disconnected.")).font(Theme.Font.secondary).foregroundStyle(.secondary).multilineTextAlignment(.center)
                Button("Reconnect") { s.start() }.themedButton(prominent: true).lineLimit(1)
            }
            .padding(Theme.Space.xl)
        }
    }

    /// The Mac's displays to choose from: the server's screens, else the node's arrangement, else none (VNCDisplays).
    private var offeredDisplays: [VNCDisplay] {
        guard let s = session else { return [] }
        // Phone Screen's arrangement is the Mac's as it answered the start, its display among them (#9314).
        let machine = activeMode == .phone && phone?.phase == .on && !(phone?.displays.isEmpty ?? true) ? phone?.displays ?? [] : model.screenDisplays(ref)
        return VNCDisplays.layout(server: s.screens, machine: machine, framebuffer: s.size)
    }

    /// The chosen display while it is there; nil shows them all. In Phone Screen, its own display once the picture has it.
    private var shownDisplay: VNCDisplay? {
        if activeMode == .phone, phone?.phase == .on, let d = phone?.display {
            return offeredDisplays.first { $0.id == "display-\(d.id)" }
        }
        guard let display else { return nil }
        return offeredDisplays.first { $0.id == display }
    }

    /// Whether this Mac offers Phone Screen (#9314): its node and Persona Input can. `-showPhoneScreenDemo` forces it
    /// for screenshots against a mock without it.
    private var phoneOffered: Bool {
        #if DEBUG
        if ProcessInfo.processInfo.arguments.contains("-showPhoneScreenDemo") { return true }
        #endif
        return model.phoneScreen(ref) != nil
    }

    /// The mode the touches follow: Phone Screen only where the Mac offers it (a kept choice on a Mac that no longer
    /// does is Mouse Mode until it does again).
    private var activeMode: VNCMode { mode == .phone && !phoneOffered ? .mouse : mode }

    /// What Phone Screen needs now: the live connection's pair and the display's shape; nil: off.
    private var phoneWant: PhoneWant? {
        guard activeMode == .phone, let s = session, s.phase == .live, let pair = s.pair, let shape = phoneShape else { return nil }
        return PhoneWant(pair: pair, size: shape.size)
    }

    /// Phone Screen is the mode but this page has not read the picture's shape yet (it has just opened).
    private var phoneShapePending: Bool { activeMode == .phone && phoneShape == nil }

    struct PhoneShape: Equatable {
        var size: CGSize
        var railed: Bool
    }

    /// The picture's size as the display's shape (#9500), when it may change: never with the keyboard up; freely until
    /// the display is up; after that only when the phone turned.
    private func readPhoneShape(_ size: CGSize) {
        guard !typing, !keyboardUp, size.width > 0, size.height > 0 else { return }
        let started = phone.map { $0.phase != .off } ?? false
        if phoneShape == nil || !started || phoneShape?.railed != railed { phoneShape = PhoneShape(size: size, railed: railed) }
    }

    struct PhoneWant: Equatable {
        var pair: String
        var size: CGSize
    }

    /// The part of the picture the small window shows (#9659): in Phone Screen, its display as the page shows it; nil: all of it
    /// (Mouse and Trackpad Mode, one display chosen or not). For this connection and framebuffer: a new one cuts again.
    private var smallWindowCut: SmallWindowCut? {
        guard activeMode == .phone, let s = session, let d = shownDisplay else { return nil }
        return SmallWindowCut(rect: d.frame, framebuffer: s.size, pair: s.pair)
    }

    struct SmallWindowCut: Equatable {
        var rect: CGRect
        var framebuffer: CGSize
        var pair: String?
    }

    /// The line under the bars while Phone Screen starts, waits for the picture or can't start.
    private var phoneLine: (text: String, busy: Bool, failed: Bool)? {
        switch phone?.phase {
        case .starting?: return (String(localized: "Setting up the phone screen on \(name)…"), true, false)
        case .failed(let why)?: return (why, false, true)
        case .on? where shownDisplay == nil: return (String(localized: "Waiting for the phone screen's picture…"), true, false)
        default: return nil
        }
    }

    private func phoneStatusLine(_ line: (text: String, busy: Bool, failed: Bool)) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: Theme.Space.xs) {
            if line.busy {
                ProgressView().controlSize(.mini)
            } else {
                ThemedIcon(Theme.Icon.info).font(Theme.Font.caption).foregroundStyle(line.failed ? Theme.caution : .secondary)
            }
            Text(line.text).font(Theme.Font.label).foregroundStyle(.secondary)
                .lineLimit(3).fixedSize(horizontal: false, vertical: true)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, Theme.Space.page)
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("phone-screen-status")
    }

    /// The Mac account's short name, from its home directory (/Users/<name>).
    static func account(_ home: String?) -> String {
        guard let home, home.hasPrefix("/Users/") else { return "" }
        return String(home.dropFirst("/Users/".count).split(separator: "/").first ?? "")
    }

    private var dot: Color {
        switch session?.phase {
        case .live: Theme.positive
        case .closed: Theme.danger
        default: Theme.caution
        }
    }

    private var phaseLabel: String {
        switch session?.phase {
        case .live: String(localized: "Connected [V N C Page 5be032]")
        case .signIn: String(localized: "Sign-in needed")
        case .closed: String(localized: "Not connected")
        default: String(localized: "Connecting")
        }
    }

    // MARK: Actions

    private func centerPointer() {
        screenView?.centerPointer()
    }

    private func toPictureInPicture() {
        VNCPiP.shared.start(ref, from: from)
        // iOS: the page goes once the small window has started (VNCPiP), or stays, saying why it could not.
    }

    private func handBack() {
        guard let from else { return }
        Task { _ = await model.send(from, text: String(localized: "I'm done on the computer. Go ahead.")) }
        onClose()
    }

    private func close() {
        if waiting { handBack() } else { onClose() }
    }

    private func pasteFromPhone(andPaste: Bool) async {
        // Off the main thread: when iOS asks Allow Paste, the read waits for the answer, and the picture and the session
        // must keep going meanwhile (#3075).
        let text = await Task.detached { UIPasteboard.general.string }.value
        // Text there that iOS would not hand over (Allow Paste not tapped, or not allowed in Settings): say so, not
        // that there is none (#3075).
        if text == nil, UIPasteboard.general.hasStrings {
            model.toast = Toast(String(localized: "Couldn't read this iPhone's clipboard: tap Allow Paste when asked, or allow Paste from Other Apps for this app in Settings"), style: .error)
            return
        }
        guard let text, !text.isEmpty, let s = session else {
            model.toast = Toast(String(localized: "No text on the clipboard"), style: .error)
            return
        }
        let ok = andPaste ? await s.paste(text) : await s.setMacClipboard(text)
        model.toast = ok ? Toast(andPaste ? String(localized: "Pasted on \(name)") : String(localized: "Copied to \(name)’s clipboard"))
            : Toast(andPaste ? String(localized: "Couldn't write to \(name)'s clipboard, so nothing was pasted") : String(localized: "Couldn't write to \(name)'s clipboard"), style: .error)
    }

    private func copyToPhone() async {
        guard let s = session, let text = await s.readMacClipboard(), !text.isEmpty else {
            model.toast = Toast(String(localized: "No text on \(name)’s clipboard"), style: .error)
            return
        }
        UIPasteboard.general.string = text
        model.toast = Toast(String(localized: "Copied \(name)’s clipboard"))
    }
}

/// The Mac's sign-in for its Screen Sharing, when this device keeps none (or the kept one was refused).
private struct VNCSignIn: View {
    let name: String
    @State var account: String
    let failed: String?
    let submit: (String, String, Bool) -> Void
    @State private var password = ""
    @State private var remember = true

    init(name: String, account: String, failed: String?, submit: @escaping (String, String, Bool) -> Void) {
        self.name = name
        _account = State(initialValue: account)
        self.failed = failed
        self.submit = submit
    }

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.m) {
            Text("Sign In to This Mac's Screen Sharing").font(Theme.Font.heading)
            Text(remember ? String(localized: "Use your macOS account and password on \(name). They go only to that Mac; if remembered, they stay in this device’s keychain and are never uploaded.")
                 : String(localized: "Use your macOS account and password on \(name). They go only to that Mac and aren’t saved."))
                .font(Theme.Font.label).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            if let failed {
                Text(failed).font(Theme.Font.label).foregroundStyle(Theme.danger).fixedSize(horizontal: false, vertical: true)
                    .accessibilityIdentifier("vnc-signin-failed")
            }
            TextField(String(localized: "Accounts"), text: $account)
                .textContentType(.username)
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
                .textFieldStyle(.roundedBorder)
                .accessibilityIdentifier("vnc-account")
            SecureField(String(localized: "Password"), text: $password)
                .textContentType(.password)
                .textFieldStyle(.roundedBorder)
                .onSubmit(go)
                .accessibilityIdentifier("vnc-password")
            Toggle(ScreenKeep.label, isOn: $remember).font(Theme.Font.secondary).accessibilityIdentifier("vnc-remember")
            Button(action: go) { Text("Connect [V N C Page a55741]").font(Theme.Font.labelStrong).lineLimit(1).frame(maxWidth: .infinity) }
                .themedButton(prominent: true)
                .disabled(account.trimmingCharacters(in: .whitespaces).isEmpty || password.isEmpty)
                .accessibilityIdentifier("vnc-connect")
        }
        .padding(Theme.Space.l)
        .card()
        .padding(Theme.Space.l)
        .frame(maxWidth: Theme.Size.box380)
    }

    private func go() {
        let a = account.trimmingCharacters(in: .whitespaces)
        guard !a.isEmpty, !password.isEmpty else { return }
        submit(a, password, remember)
        password = ""
    }
}

/// The Remember switch's label.
enum ScreenKeep {
    static let label = String(localized: "Remember on This iPhone")
}

/// Help: how the touches, the keyboard and the clipboard work, and handing the Mac back.
struct VNCHelp: View {
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            List {
                Section("Moving Around") {
                    row(Theme.Icon.pointer, String(localized: "Mouse mode (default)"), String(localized: "Tap to click there. Drag with one finger to drag. Drag with two fingers to scroll. Two-finger tap or touch and hold to right-click."))
                    row(Theme.Icon.search, String(localized: "Zoom"), String(localized: "In Mouse Mode, pinch to zoom in and out; when zoomed in, dragging with two fingers moves the view instead of scrolling. In either mode, “…” › Zoom In zooms in around the pointer and Zoom to Fit shows the whole screen again. In Trackpad Mode two fingers only scroll, and a zoomed view follows the pointer."))
                    #if os(iOS)
                    if AppOrientation.phone {
                        row(Theme.Icon.rotateToLandscape, String(localized: "Landscape"), String(localized: "Tap the rotate button next to the clipboard to turn the screen sideways for a larger view, and tap it again to turn it back upright. Once tapped, the screen keeps that orientation until you close it."))
                    }
                    #endif
                    row(Theme.Icon.displays, String(localized: "Displays"), String(localized: "When the Mac has more than one display, choose one in “…” › Displays to show only that display. Zooming, taps and the pointer then stay on it."))
                    row(Theme.Icon.phoneScreen, String(localized: "Phone Screen"), String(localized: "Where the Mac offers it, “…” › Phone Screen shows one of its windows on a display shaped like this iPhone, so text is readable without zooming. Pick the window from Windows at the bottom. Tap to click, drag with one finger to scroll, touch and hold then drag to select, and touch and hold to right-click. Switching back to another mode, or closing the screen, puts the Mac's windows back where they were."))
                }
                Section("Typing and Clipboard") {
                    row(Theme.Icon.keyboard, String(localized: "Typing"), String(localized: "Tap the keyboard in the bottom bar. The row above the keyboard has Mac shortcuts: tap ⌘ ⌥ ⌃ ⇧ to hold, tap again to release, plus Esc, Tab, arrow keys, and ⌘C ⌘V ⌘Z ⌘Tab ⌘Space. Input methods such as Chinese work as usual; the committed text is pasted over through the clipboard."))
                    row(Theme.Icon.clipboard, String(localized: "Copy and Paste"), String(localized: "Tap the clipboard in the bottom bar: “\(VNCPage.fromPhone)” puts your text on the Mac’s clipboard and pastes it; “\(VNCPage.toPhone)” brings over the text on the Mac’s clipboard."))
                }
                Section("When to Use Trackpad Mode") {
                    row(Theme.Icon.trackpad, String(localized: "Trackpad Mode"), String(localized: "Turn it on from “…” in the top-right corner. Move your finger to move the pointer, tap to click at the pointer, and tap then drag right away to drag. If the pointer drifts off, use “Center Pointer”."))
                }
                Section("Picture in Picture") {
                    #if os(iOS)
                    row(Theme.Icon.pictureInPicture, String(localized: "Picture in Picture"), String(localized: "The Picture in Picture button next to Close floats the screen over other apps and the Home Screen; tap it to come back here. Picture in Picture is view-only, and closing it disconnects."))
                    #else
                    row(Theme.Icon.pictureInPicture, String(localized: "Picture in Picture"), String(localized: "“Picture in Picture” in “…” floats the screen over other apps and the Home Screen; tap it to come back here. Picture in Picture is view-only, and closing it disconnects."))
                    #endif
                }
                Section("Handing Back the Computer") {
                    row(Theme.Icon.handBack, String(localized: "I'm Done"), String(localized: "When an agent is waiting for you to do something on the computer, tap “I'm Done” in the bottom bar when you're finished and it carries on. Closing the screen also hands it back."))
                }
            }
            .navigationTitle("Using VNC")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                SheetClose(id: "vnc-help-close") { dismiss() }
            }
        }
        .accessibilityIdentifier("vnc-help-page")
    }

    private func row(_ icon: String, _ title: String, _ text: String) -> some View {
        Label {
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                Text(title).font(Theme.Font.body)
                Text(text).font(Theme.Font.label).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            }
        } icon: {
            ThemedIcon(icon).foregroundStyle(Theme.accent)
        }
    }
}

extension RFBSession {
    /// One-Tap Unlock (#1507 a-2): a Shift wakes the lock screen and puts the cursor in its password field, then the kept
    /// password and Return; the hub only notes that it was unlocked. The pause counts from when the Shift goes out: just
    /// signed in, the keys wait out the session's first seconds (RFBInputQueue, #3075).
    func unlockWithKeptPassword() {
        guard let kept = ScreenCredentials.load(ref) else { return }
        press(Self.shift)
        pause(.milliseconds(800))
        for ch in kept.password.unicodeScalars { press(Self.keysym(ch)) }
        press(VNCKeys.enter)
        Task { _ = try? await AppModel.shared.api(ref.group)?.computerJob(machine: ref.id, "unlocking") }
    }
}
