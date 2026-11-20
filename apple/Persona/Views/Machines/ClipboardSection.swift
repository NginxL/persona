import SwiftUI

/// Clipboard on a computer's page (#3064), while its hub reports clipboard support: the per-computer switch (off by
/// default), then Send Clipboard (reads the phone's; iOS may ask "Allow Paste?"), the system Paste button beside it (no
/// prompt), and Get Clipboard (the computer's onto the phone, shown below). After the first paste the user allowed, a
/// one-time hint says how to stop iOS asking every time.
struct ClipboardSection: View {
    @Environment(AppModel.self) private var app
    @Environment(\.displayScale) private var displayScale
    let ref: MachineRef
    let machine: Machine
    var listed: MachineClipboard
    var online: Bool
    @State private var model: ClipboardModel?

    var body: some View {
        let state = model?.state(listed) ?? listed
        VStack(alignment: .leading, spacing: Theme.Space.m) {
            Text("Clipboard").font(Theme.Font.heading).padding(.horizontal, Theme.Space.xs)
            VStack(alignment: .leading, spacing: Theme.Space.s) {
                Toggle(isOn: Binding(get: { state.usable }, set: { on in Task { await setEnabled(on) } })) {
                    VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                        Text("Share Clipboard with Phone").font(Theme.Font.body) // a switch's title, not a button: wraps at large sizes
                        Text("Off by default. When on, you can send this phone's clipboard to this computer and get the computer's here. Nothing is kept on the hub.")
                            .font(Theme.Font.label).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                    }
                }
                .disabled(model?.busy == .saving || !online || !state.supported)
                .accessibilityIdentifier("clipboard-switch")
                if !state.supported {
                    line(state.why.map { String(localized: "Not available: \($0)") }
                         ?? String(localized: "Not available on this computer"), ok: false)
                } else if state.enabled {
                    Divider()
                    if online { actions } else { line(String(localized: "Machine is offline. Available once it's connected."), ok: false) }
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .card()
            if model?.showsPasteHint == true, state.usable { pasteHint }
        }
        .task {
            if model == nil { model = ClipboardModel(deps: Self.deps(app, ref)) }
            await model?.refresh(machineID: ref.id)
            #if DEBUG
            let d = UserDefaults.standard
            if let kind = d.string(forKey: "showClipboardFetched") {
                await model?.preview(fetched: Self.screenshotContent(kind), hint: d.bool(forKey: "showClipboardHint"), scale: displayScale)
            } else if d.bool(forKey: "showClipboardHint") {
                await model?.preview(fetched: nil, hint: true)
            }
            // `-showClipboardFetch YES`: runs Get Clipboard against the (mock) hub on opening (screenshots).
            if d.bool(forKey: "showClipboardFetch"), state.usable, online { await fetch() }
            #endif
        }
        .onChange(of: listed) { _, new in model?.synchronize(new) }
    }

    static func deps(_ app: AppModel, _ ref: MachineRef) -> ClipboardModel.Deps {
        ClipboardModel.Deps(api: { app.api(ref.group) }, pasteboard: .live, updated: { state in
            guard var m = app.machine(ref) else { return }
            m.clipboard = state
            app.applyMachine(m)
        }, hints: .live())
    }

    #if DEBUG
    /// A self-contained picture or text preview for screenshot launches, without a hub or an asset in the app bundle.
    private static func screenshotContent(_ kind: String) -> ClipboardContent? {
        switch kind {
        case "text":
            return .text("Clipboard preview")
        case "image":
            let size = CGSize(width: 480, height: 300)
            let format = UIGraphicsImageRendererFormat()
            format.scale = 1
            let data = UIGraphicsImageRenderer(size: size, format: format).pngData { context in
                UIColor.systemTeal.setFill()
                context.fill(CGRect(origin: .zero, size: size))
            }
            return .image(ClipboardImage(mime: "image/png", bytes: data))
        default:
            return nil
        }
    }
    #endif

    // MARK: Actions

    private var actions: some View {
        VStack(alignment: .leading, spacing: Theme.Space.m) {
            ViewThatFits(in: .horizontal) {
                HStack(spacing: Theme.Space.s) { sendButton; getButton }
                VStack(alignment: .leading, spacing: Theme.Space.s) { sendButton; getButton }
            }
            // Side by side, or the words under the button at the largest text sizes.
            ViewThatFits(in: .horizontal) {
                HStack(spacing: Theme.Space.s) { pasteButton; pasteNote.fixedSize() }
                VStack(alignment: .leading, spacing: Theme.Space.xs) { pasteButton; pasteNote }
            }
            if let fetched = model?.fetched { preview(fetched) }
        }
    }

    private var pasteButton: some View {
        SystemPasteButton { content in await send(content) }
            .disabled(model?.busy != nil)
    }

    private var pasteNote: some View {
        Text("Sends without the paste prompt").font(Theme.Font.label).foregroundStyle(.secondary)
            .fixedSize(horizontal: false, vertical: true)
    }

    private var sendButton: some View {
        Button {
            Task { await sendPasteboard() }
        } label: {
            busyLabel(String(localized: "Send Clipboard"), symbol: Theme.Icon.clipboardSend, busy: model?.busy == .sending)
        }
        .themedButton(prominent: true)
        .disabled(model?.busy != nil)
        .accessibilityIdentifier("clipboard-send")
    }

    private var getButton: some View {
        Button {
            Task { await fetch() }
        } label: {
            busyLabel(String(localized: "Get Clipboard"), symbol: Theme.Icon.clipboardGet, busy: model?.busy == .fetching)
        }
        .themedButton()
        .disabled(model?.busy != nil)
        .accessibilityIdentifier("clipboard-get")
    }

    private func busyLabel(_ title: String, symbol: String, busy: Bool) -> some View {
        HStack(spacing: Theme.Space.xs) {
            if busy { ProgressView().controlSize(.small) } else { ThemedIcon(symbol) }
            Text(title).lineLimit(1).minimumScaleFactor(0.8)
        }
        .font(Theme.Font.labelStrong)
        .frame(maxWidth: .infinity)
    }

    /// What Get Clipboard put on the phone: the text (a few lines), or the image.
    @ViewBuilder private func preview(_ content: ClipboardContent) -> some View {
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            Label {
                Text("Now on this phone's clipboard").font(Theme.Font.label).foregroundStyle(.secondary)
            } icon: {
                ThemedIcon(Theme.Icon.success).foregroundStyle(Theme.positive)
            }
            .font(Theme.Font.label)
            switch content.kind {
            case .image:
                if let image = model?.fetchedPicture {
                    Image(uiImage: image).resizable().scaledToFit()
                        .clipShape(.rect(cornerRadius: Theme.Radius.control, style: .continuous))
                        .overlay(RoundedRectangle(cornerRadius: Theme.Radius.control, style: .continuous)
                            .strokeBorder(Theme.cardStroke, lineWidth: Theme.Size.hairline)) // a white picture on a white card
                        .frame(maxWidth: .infinity, maxHeight: Theme.Size.box200, alignment: .leading)
                        .accessibilityLabel(Text("Image from the computer's clipboard"))
                }
            default:
                Text(content.text ?? "").font(Theme.Font.secondary).lineLimit(6).textSelection(.enabled)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(Theme.Space.s)
                    .background(Theme.Fill.well, in: .rect(cornerRadius: Theme.Radius.control, style: .continuous))
            }
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("clipboard-preview")
    }

    /// iOS asks before every paste unless Settings › Persona › Paste from Other Apps is Allow.
    private var pasteHint: some View {
        NoticeBanner(text: String(localized: "Asked every time? In Settings, set Paste from Other Apps to Allow."), tone: .neutral,
                     symbol: Theme.Icon.clipboard, lineLimit: nil) {
            // Side by side, or one under the other at the largest text sizes: never cut short.
            ViewThatFits(in: .horizontal) {
                HStack(spacing: Theme.Space.l) { hintButtons }
                VStack(alignment: .leading, spacing: 0) { hintButtons }
            }
        }
        .accessibilityIdentifier("clipboard-hint")
    }

    @ViewBuilder private var hintButtons: some View {
        Button {
            model?.dismissPasteHint()
            if let url = URL(string: UIApplication.openSettingsURLString) { UIApplication.shared.open(url) }
        } label: {
            hintAction(String(localized: "Open Settings"))
        }
        .accessibilityIdentifier("clipboard-hint-settings")
        Button {
            withAnimation(Theme.Motion.layout) { model?.dismissPasteHint() }
        } label: {
            hintAction(String(localized: "Don't Show Again"))
        }
        .foregroundStyle(.secondary)
        .accessibilityIdentifier("clipboard-hint-dismiss")
    }

    /// A hint button's label: one line, and at least a finger's height to hit (section 6.3 of the design spec), here
    /// only, so the other notices keep their look.
    private func hintAction(_ title: String) -> some View {
        Text(title)
            .scaledFrame(minHeight: Theme.Size.tapTarget)
            .contentShape(.rect)
    }

    private func line(_ text: String, ok: Bool) -> some View {
        Label {
            Text(text).font(Theme.Font.secondary).foregroundStyle(ok ? Color.primary : Color.secondary)
                .fixedSize(horizontal: false, vertical: true)
        } icon: {
            ThemedIcon(ok ? Theme.Icon.success : Theme.Icon.error).foregroundStyle(ok ? Theme.positive : Theme.caution)
        }
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("clipboard-status")
    }

    // MARK: Calls

    private func setEnabled(_ on: Bool) async {
        guard let model, !(await model.setEnabled(on, machineID: ref.id)), let outcome = model.outcome else { return }
        Haptics.error()
        app.toast = Toast(on ? String(localized: "Couldn't turn on: \(outcome.message(machine: machine.name))")
                             : String(localized: "Couldn't turn off: \(outcome.message(machine: machine.name))"), style: .error)
    }

    private func sendPasteboard() async {
        guard let model else { return }
        report(await model.sendPasteboard(machineID: ref.id))
    }

    private func send(_ content: ClipboardContent?) async {
        guard let model else { return }
        report(await model.send(content, machineID: ref.id))
    }

    private func fetch() async {
        guard let model else { return }
        report(await model.fetch(machineID: ref.id, scale: displayScale))
    }

    private func report(_ outcome: ClipboardOutcome) {
        Self.report(outcome, machine: machine.name, ref: ref, app: app)
    }

    /// A result as a toast; a send remembers the computer, offered first next time (`ClipboardOffer`).
    static func report(_ outcome: ClipboardOutcome, machine name: String, ref: MachineRef, app: AppModel) {
        if outcome.isFailure { Haptics.error() } else { Haptics.success() }
        if case .sent = outcome { UserDefaults.standard.set(ref.path, forKey: ClipboardOffer.lastMachineKey) }
        app.toast = Toast(outcome.message(machine: name), style: outcome.isFailure ? .error : .success)
    }
}
