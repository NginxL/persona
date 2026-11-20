import Observation
import SwiftUI

/// Something `@` can pull into the prompt: a data source, an app, or the attach action.
public struct PromptSource: Identifiable, Sendable {
    public enum Icon: Sendable {
        case glyph(BUIGlyph)
        case mark(BUIAvatar, size: CGSize)
        /// An SF Symbol.
        case symbol(String)
    }

    public enum Kind: Sendable {
        case source
        /// Picking it asks the host to add an attachment instead of inserting a mention.
        case attach
        /// Shows a Connect / Connected action.
        case connectable
    }

    public var id: String
    public var name: String
    public var detail: String
    public var icon: Icon
    public var kind: Kind
    /// What picking it types into the draft; `nil` types "@name ".
    public var insert: String?

    public init(id: String, name: String, detail: String, icon: Icon, kind: Kind = .source, insert: String? = nil) {
        self.id = id
        self.name = name
        self.detail = detail
        self.icon = icon
        self.kind = kind
        self.insert = insert
    }
}

/// A `/` command.
public struct PromptCommand: Identifiable, Sendable {
    /// The name, unless one was given: a user's saved prompt may share a name with a command and stays a row of its own.
    public var id: String
    /// Including the slash, e.g. "/compare".
    public var name: String
    public var detail: String
    /// What picking it types into the draft; `nil` types the name and a space.
    public var insert: String?

    public init(_ name: String, detail: String, insert: String? = nil, id: String? = nil) {
        self.id = id ?? name
        self.name = name
        self.detail = detail
        self.insert = insert
    }
}

/// A model in the picker.
public struct PromptModel: Identifiable, Sendable {
    public var id: String
    public var name: String
    public var tag: String

    public init(id: String, name: String, tag: String) {
        self.id = id
        self.name = name
        self.tag = tag
    }
}

/// The `@word` or `/word` being typed at the end of a draft.
public struct PromptToken: Equatable, Sendable {
    public enum Kind: Sendable { case mention, command }
    public var kind: Kind
    public var query: String
    /// Offset of the `@` or `/` in the draft.
    public var start: Int

    /// Matches `(^|\s)([@/])([\w-]*)$` like the original.
    public static func parse(_ draft: String) -> PromptToken? {
        let chars = Array(draft)
        var i = chars.count
        while i > 0, chars[i - 1].isLetter || chars[i - 1].isNumber || chars[i - 1] == "_" || chars[i - 1] == "-" {
            i -= 1
        }
        guard i > 0, chars[i - 1] == "@" || chars[i - 1] == "/" else { return nil }
        let triggerAt = i - 1
        guard triggerAt == 0 || chars[triggerAt - 1].isWhitespace else { return nil }
        return PromptToken(
            kind: chars[triggerAt] == "@" ? .mention : .command,
            query: String(chars[i...]).lowercased(),
            start: triggerAt
        )
    }
}

/// Everything a prompt bar changes. Own one per composer; drive it from your app (or a
/// script) and the bar renders whatever it holds.
@MainActor
@Observable
public final class PromptBarModel {
    public var text: String
    public var attachments: [String]
    public var selectedModelID: String
    public var isListening = false
    public var connectedSourceIDs: Set<String> = []
    /// The row the menu highlight sits on; `nil` hides the highlight.
    public var highlightedRow: Int?
    public var isModelMenuOpen = false
    /// The `+` button's source menu, open without typing `@`.
    public var isSourceMenuOpen = false
    /// Set by Escape-like dismissal; cleared by the next edit.
    public var isMenuDismissed = false

    public init(text: String = "", attachments: [String] = [], selectedModelID: String) {
        self.text = text
        self.attachments = attachments
        self.selectedModelID = selectedModelID
    }

    public var token: PromptToken? { isMenuDismissed ? nil : PromptToken.parse(text) }

    public var canSend: Bool {
        !text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || !attachments.isEmpty
    }
}

/// A composer with attach, `@` sources, `/` commands, a model picker, dictation and send.
/// Menus grow up from the composer's top edge; a long draft moves above the controls.
/// Picking `sweepModelID` plays a one-shot rainbow sweep across the composer.
public struct PromptBar: View {
    public enum Shape: String, CaseIterable, Sendable {
        /// 14 pt corners.
        case rounded
        /// Fully round, easing to 24 pt when the draft wraps or attachments show.
        case pill
    }

    public enum Layout: Sendable {
        /// The original: one row, the draft moving above the controls once it wraps.
        case adaptive
        /// The draft always on top, the controls in a toolbar under it.
        case stacked
    }

    /// How a host fits the bar to its own app. The defaults are the original's.
    public struct Options {
        public var layout: Layout = .adaptive
        /// What `+` opens. Empty: `+` opens `sources`, like the original.
        public var attachSources: [PromptSource] = []
        /// `@` and `/` buttons in the toolbar (`.stacked`): they type the trigger and open its menu.
        public var showsMentionButton = false
        public var showsCommandButton = false
        public var showsDictation = true
        /// The controls' side; the original's 28 is a desktop size.
        public var controlSize: CGFloat = 28
        /// The draft's type size before `typeScale`.
        public var inputSize: CGFloat = 13
        /// Corners of the `.rounded` shape.
        public var cornerRadius: CGFloat = 14
        /// Off: no fill, shadow or border; the host draws the composer's surface.
        public var drawsSurface = true
        /// The host holds something sendable the bar does not see (photos, quoted context).
        public var canSendExtra = false
        /// The host's draft: the field edits it directly (the model follows it for the menus), so nothing writes
        /// into the field while an input method is composing.
        public var text: Binding<String>?
        public var selection: Binding<TextSelection?>?
        /// The host's focus for the field, so it can raise or drop the keyboard; the bar's own otherwise.
        public var focus: FocusState<Bool>.Binding?
        /// Shown instead of send while there is nothing to send, e.g. while an agent works.
        public var onStop: (() -> Void)?
        /// Replaces the bar's own listening toggle; the host sets `model.isListening`.
        public var onDictation: (() -> Void)?
        /// Direct model action instead of opening the catalog menu (the main assistant conversation's model sheet).
        public var modelAction: (() -> Void)?
        /// The effective model shown on that action button.
        public var modelActionTitle: String?
        /// A picked attach source (`kind == .attach`).
        public var onAttachSource: ((PromptSource) -> Void)?
        /// Return true when the host handled the pick; otherwise the bar types the source's `insert`.
        public var onPickSource: ((PromptSource) -> Bool)?
        /// Return true when the host handled the pick; otherwise the bar types the command's `insert`.
        public var onPickCommand: ((PromptCommand) -> Bool)?

        public init() {}
    }

    @Bindable var model: PromptBarModel
    var shape: Shape
    var sources: [PromptSource]
    var commands: [PromptCommand]
    var models: [PromptModel]
    var placeholder: String
    var sweepModelID: String?
    var onSend: ((String) -> Void)?
    var onAttach: (() -> Void)?
    var options: Options

    @State private var sweepID = 0
    @State private var containerWidth: CGFloat = 0
    @State private var draftWidth: CGFloat = 0
    @State private var modelButtonWidth: CGFloat = 0
    @FocusState private var inputFocused: Bool
    @Environment(\.buiTheme) private var theme
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    public init(
        model: PromptBarModel,
        shape: Shape = .rounded,
        sources: [PromptSource] = [],
        commands: [PromptCommand] = [],
        models: [PromptModel] = [],
        placeholder: String = "Write a message…",
        sweepModelID: String? = nil,
        options: Options = Options(),
        onSend: ((String) -> Void)? = nil,
        onAttach: (() -> Void)? = nil
    ) {
        self.model = model
        self.shape = shape
        self.sources = sources
        self.commands = commands
        self.models = models
        self.placeholder = placeholder
        self.sweepModelID = sweepModelID
        self.options = options
        self.onSend = onSend
        self.onAttach = onAttach
    }

    private enum Menu { case mention, command, attach }

    private var menu: Menu? {
        if model.isSourceMenuOpen { return options.attachSources.isEmpty ? .mention : .attach }
        switch model.token?.kind {
        case .mention: return .mention
        case .command: return .command
        case nil: return nil
        }
    }

    private var query: String { model.isSourceMenuOpen ? "" : model.token?.query ?? "" }

    private var sourceRows: [PromptSource] {
        if menu == .attach { return options.attachSources }
        return query.isEmpty ? sources : sources.filter { $0.name.lowercased().contains(query) || $0.detail.lowercased().contains(query) }
    }

    private var commandRows: [PromptCommand] {
        commands.filter { $0.name.dropFirst().lowercased().hasPrefix(query) }
    }

    private var rowCount: Int { menu == .command ? commandRows.count : menu == nil ? 0 : sourceRows.count }

    private var size: CGFloat { options.controlSize }

    private var focus: FocusState<Bool>.Binding { options.focus ?? $inputFocused }

    private var draft: Binding<String> { options.text ?? $model.text }

    /// The bar's own edits (a pick, a trigger, send) go to the host's draft too.
    private func setText(_ text: String) {
        model.text = text
        if let host = options.text, host.wrappedValue != text { host.wrappedValue = text }
    }

    /// Wrapped text moves above the controls: when it has a newline or no longer fits inline.
    private var wide: Bool {
        if options.layout == .stacked { return true }
        let inline = containerWidth - 12 - size * 3 - modelButtonWidth - 4 * 4
        return model.text.contains("\n") || draftWidth + 8 > inline
    }

    private var currentModel: PromptModel? { models.first { $0.id == model.selectedModelID } }

    private var canSend: Bool { model.canSend || options.canSendExtra }

    private var composerRadius: CGFloat {
        switch shape {
        case .rounded: options.cornerRadius
        case .pill: (wide || !model.attachments.isEmpty) ? 24 : 100
        }
    }

    public var body: some View {
        VStack(spacing: 0) {
            // Menus hang above the composer: a zero-height, bottom-aligned slot lets them
            // overflow upward without taking layout space, 8 pt over the composer's top edge.
            ZStack(alignment: .bottomLeading) {
                if let menu {
                    sourceOrCommandMenu(menu)
                        .transition(.scale(scale: 0.95, anchor: .bottom).combined(with: .opacity))
                }
                if model.isModelMenuOpen {
                    modelMenu
                        .padding(.leading, modelMenuX)
                        .transition(.scale(scale: 0.95, anchor: .bottomLeading).combined(with: .opacity))
                }
            }
            .fixedSize(horizontal: false, vertical: true)
            .padding(.bottom, 8)
            .frame(maxWidth: .infinity, alignment: .bottomLeading)
            .frame(height: 0, alignment: .bottom)
            .zIndex(1)
            composer
        }
        .animation(BUICurve.outStrong.animation(duration: 0.18), value: menu)
        .animation(BUICurve.outStrong.animation(duration: 0.18), value: model.isModelMenuOpen)
        .onGeometryChange(for: CGFloat.self) { $0.size.width } action: { containerWidth = $0 }
        .onChange(of: model.selectedModelID) { _, id in
            if id == sweepModelID, !reduceMotion { sweepID += 1 }
        }
        .onChange(of: model.text) { model.isMenuDismissed = false }
        .onChange(of: options.text?.wrappedValue, initial: true) { _, text in
            if let text, text != model.text { model.text = text }
        }
        .onChange(of: menu) { model.highlightedRow = nil }
    }

    /// `w-44` plus 8 pt, since SF Pro sets model names wider than Inter.
    private static let modelMenuWidth: CGFloat = 184

    /// The model menu sits over its trigger but stays inside the composer's width.
    private var modelMenuX: CGFloat {
        let leading = 1 + (options.showsMentionButton ? 1 : 0) + (options.showsCommandButton ? 1 : 0)
        let triggerX = wide
            ? 6 + (size + 4) * CGFloat(leading)
            : containerWidth - 6 - size * 2 - 4 * 2 - modelButtonWidth
        return max(0, min(triggerX, containerWidth - Self.modelMenuWidth))
    }

    // MARK: Composer

    private var composer: some View {
        let radius = composerRadius
        let pill = shape == .pill
        return VStack(alignment: .leading, spacing: 6) {
            if !model.attachments.isEmpty {
                BUIFlowLayout(spacing: 6) {
                    ForEach(Array(model.attachments.enumerated()), id: \.offset) { index, file in
                        attachmentChip(file, at: index, pill: pill)
                    }
                }
                .padding(.top, 2)
                .padding(.horizontal, pill ? 4 : 2)
            }
            controls
        }
        .padding(6)
        .background {
            RainbowSweep(trigger: sweepID)
        }
        .clipShape(.rect(cornerRadius: radius, style: .continuous))
        .background {
            if options.drawsSurface {
                RoundedRectangle(cornerRadius: radius, style: .continuous)
                    .fill(theme.colors.surface)
                    .shadow(color: Color(light: 0, dark: 0, lightAlpha: 0.035, darkAlpha: 0.2), radius: 1, y: 1)
                    .shadow(color: Color(light: 0, dark: 0, lightAlpha: 0.035, darkAlpha: 0.2), radius: 10, y: 5)
            }
        }
        .overlay {
            if options.drawsSurface {
                RoundedRectangle(cornerRadius: radius, style: .continuous)
                    .strokeBorder(focus.wrappedValue ? theme.colors.lineStrong : theme.colors.line, lineWidth: 1)
            }
        }
        .animation(BUIMotion.standard, value: radius)
        .background(alignment: .topLeading) {
            // Measures the draft on one line to decide when it moves above the controls.
            if options.layout == .adaptive {
                Text(model.text.isEmpty ? " " : model.text)
                    .buiFont(BUITextStyle(size: options.inputSize, lineHeight: 18 / 13))
                    .fixedSize()
                    .hidden()
                    .onGeometryChange(for: CGFloat.self) { $0.size.width } action: { draftWidth = $0 }
            }
        }
    }

    @ViewBuilder
    private var controls: some View {
        if wide {
            VStack(alignment: .leading, spacing: 6) {
                input
                HStack(alignment: .bottom, spacing: 4) {
                    plusButton
                    if options.showsMentionButton { triggerButton("@", label: theme.strings.mention, kind: .mention) }
                    if options.showsCommandButton { triggerButton("/", label: theme.strings.commands, kind: .command) }
                    if !models.isEmpty || options.modelAction != nil { modelButton }
                    Spacer(minLength: 0)
                    if options.showsDictation { micButton }
                    sendOrStopButton
                }
            }
        } else {
            HStack(alignment: .bottom, spacing: 4) {
                plusButton
                input
                if !models.isEmpty || options.modelAction != nil { modelButton }
                if options.showsDictation { micButton }
                sendOrStopButton
            }
        }
    }

    @ViewBuilder
    private var field: some View {
        let prompt = model.isListening ? theme.strings.listening : placeholder
        if let selection = options.selection {
            TextField(prompt, text: draft, selection: selection, axis: .vertical)
        } else {
            TextField(prompt, text: draft, axis: .vertical)
        }
    }

    private var input: some View {
        field
            .buiFont(BUITextStyle(size: options.inputSize, lineHeight: 18 / 13))
            .foregroundStyle(theme.colors.ink)
            .lineLimit(1...(options.layout == .stacked ? 6 : 5))
            .focused(focus)
            .padding(.horizontal, options.layout == .stacked ? 6 : 4)
            .padding(.vertical, 5)
            .frame(maxWidth: .infinity, minHeight: size, alignment: .leading)
            .accessibilityLabel(theme.strings.prompt)
    }

    private var controlShape: AnyShape {
        shape == .pill ? AnyShape(Capsule()) : AnyShape(RoundedRectangle(cornerRadius: 8, style: .continuous))
    }

    private var plusButton: some View {
        Button {
            model.isModelMenuOpen = false
            model.isSourceMenuOpen.toggle()
            // The attach menu does not need the keyboard; the source menu filters as you type.
            if options.attachSources.isEmpty { focus.wrappedValue = true }
        } label: {
            BUIIcon(.plus, size: 16)
                .frame(width: size, height: size)
                .contentShape(controlShape)
        }
        .buttonStyle(ControlButtonStyle(shape: controlShape, active: model.isSourceMenuOpen))
        .accessibilityLabel(theme.strings.attach)
    }

    /// `@` or `/`: types the trigger at the end of the draft, which opens its menu; again closes it.
    private func triggerButton(_ trigger: String, label: String, kind: PromptToken.Kind) -> some View {
        let active = model.token?.kind == kind && !model.isSourceMenuOpen
        return Button {
            model.isModelMenuOpen = false
            model.isSourceMenuOpen = false
            if active, let token = model.token {
                setText(String(model.text.prefix(token.start)))
            } else {
                let needsSpace = !(model.text.last?.isWhitespace ?? true)
                setText(model.text + (needsSpace ? " " : "") + trigger)
            }
            focus.wrappedValue = true
        } label: {
            Text(trigger)
                .buiFont(BUITextStyle(size: 15, weight: .medium))
                .frame(width: size, height: size)
                .contentShape(controlShape)
        }
        .buttonStyle(ControlButtonStyle(shape: controlShape, active: active))
        .accessibilityLabel(label)
    }

    private var modelButton: some View {
        Button {
            if let action = options.modelAction {
                model.isSourceMenuOpen = false
                model.isModelMenuOpen = false
                action()
            } else {
                model.isSourceMenuOpen = false
                model.isModelMenuOpen.toggle()
            }
        } label: {
            HStack(spacing: 4) {
                Text(options.modelActionTitle ?? currentModel?.name ?? "Model")
                    .buiFont(BUITextStyle(size: 12, weight: .medium))
                    .foregroundStyle(theme.colors.ink2)
                    .lineLimit(1)
                BUIIcon(.chevronDown, size: 11, lineWidth: 2.4)
                    .foregroundStyle(theme.colors.ink3)
            }
            .padding(.horizontal, 6)
            .frame(height: size)
            .contentShape(controlShape)
        }
        .buttonStyle(ControlButtonStyle(shape: controlShape, active: options.modelAction == nil && model.isModelMenuOpen))
        .fixedSize()
        .onGeometryChange(for: CGFloat.self) { $0.size.width } action: { modelButtonWidth = $0 }
        .accessibilityLabel(theme.strings.chooseModel)
        .accessibilityValue(options.modelActionTitle ?? currentModel?.name ?? "")
    }

    private var micButton: some View {
        Button {
            if let onDictation = options.onDictation { onDictation() } else { model.isListening.toggle() }
        } label: {
            Group {
                if model.isListening {
                    LevelBars().foregroundStyle(theme.colors.accentInk)
                } else {
                    BUIIcon(BUIGlyph("M12 2a3 3 0 0 0-3 3v7a3 3 0 0 0 6 0V5a3 3 0 0 0-3-3zM19 10v2a7 7 0 0 1-14 0v-2M12 19v3"), size: 15)
                }
            }
            .frame(width: size, height: size)
            .background(model.isListening ? theme.colors.accentTint : .clear, in: controlShape)
            .contentShape(controlShape)
        }
        .buttonStyle(ControlButtonStyle(shape: controlShape, active: false))
        .accessibilityLabel(model.isListening ? theme.strings.stopDictation : theme.strings.startDictation)
    }

    @ViewBuilder
    private var sendOrStopButton: some View {
        if let onStop = options.onStop, !canSend {
            Button(action: onStop) {
                RoundedRectangle(cornerRadius: 2.5, style: .continuous)
                    .fill(.white)
                    .frame(width: 10, height: 10)
                    .frame(width: size, height: size)
                    .background(theme.colors.red, in: controlShape)
            }
            .buttonStyle(ShrinkStyle())
            .accessibilityLabel(theme.strings.stop)
        } else {
            sendButton
        }
    }

    private var sendButton: some View {
        let enabled = canSend
        return Button {
            send()
        } label: {
            BUIIcon(.arrowUp, size: 16, lineWidth: 2.4)
                .foregroundStyle(enabled ? theme.colors.surface : theme.colors.ink2)
                .frame(width: size, height: size)
                .background(enabled ? theme.colors.ink : theme.colors.lineStrong, in: controlShape)
                .animation(.easeInOut(duration: 0.2), value: enabled)
        }
        .buttonStyle(ShrinkStyle())
        .disabled(!enabled)
        .keyboardShortcut(.return, modifiers: .command)
        .accessibilityLabel(theme.strings.send)
    }

    private func attachmentChip(_ file: String, at index: Int, pill: Bool) -> some View {
        let chipShape = pill ? AnyShape(Capsule()) : AnyShape(RoundedRectangle(cornerRadius: theme.radii.chip, style: .continuous))
        return HStack(spacing: 6) {
            BUIIcon(ToolCall.Kind.read.glyph, size: 12)
            Text(file)
                .lineLimit(1)
                .frame(maxWidth: 144, alignment: .leading)
                .fixedSize()
            Button {
                _ = withAnimation(BUIMotion.standard) { model.attachments.remove(at: index) }
            } label: {
                BUIIcon(.close, size: 10, lineWidth: 2.5)
                    .foregroundStyle(theme.colors.ink3)
                    .frame(width: 24, height: 24)
                    .contentShape(chipShape)
            }
            .buttonStyle(.plain)
            .padding(.vertical, -4)
            .accessibilityLabel(theme.strings.remove(file))
        }
        .buiFont(BUITextStyle(size: 11.5))
        .foregroundStyle(theme.colors.ink2)
        .padding(.leading, 6)
        .padding(.trailing, 4)
        .frame(height: 26)
        .background(theme.colors.field, in: chipShape)
        .overlay(chipShape.stroke(theme.colors.line, lineWidth: 1))
        .buiEntrance(.popIn, duration: 0.2)
    }

    // MARK: Menus

    private func sourceOrCommandMenu(_ menu: Menu) -> some View {
        VStack(alignment: .leading, spacing: 0) {
            ZStack(alignment: .top) {
                if let row = model.highlightedRow, row < rowCount {
                    RoundedRectangle(cornerRadius: 6, style: .continuous)
                        .fill(theme.colors.hover)
                        .frame(height: 36)
                        .offset(y: CGFloat(row) * 36)
                        .frame(maxHeight: .infinity, alignment: .top)
                        .transition(.opacity)
                }
                VStack(spacing: 0) {
                    if menu == .command {
                        ForEach(Array(commandRows.enumerated()), id: \.element.id) { index, command in
                            menuRow(index: index, name: command.name, detail: command.detail) {
                                pick(command: command)
                            } leading: {
                                EmptyView()
                            } trailing: {
                                EmptyView()
                            }
                        }
                    } else {
                        ForEach(Array(sourceRows.enumerated()), id: \.element.id) { index, source in
                            menuRow(index: index, name: source.name, detail: source.detail) {
                                pick(source: source)
                            } leading: {
                                sourceIcon(source.icon)
                            } trailing: {
                                if source.kind == .connectable {
                                    let connected = model.connectedSourceIDs.contains(source.id)
                                    Button(connected ? theme.strings.connected : theme.strings.connect) {
                                        if connected { model.connectedSourceIDs.remove(source.id) } else { model.connectedSourceIDs.insert(source.id) }
                                    }
                                    .buttonStyle(.plain)
                                    .buiFont(BUITextStyle(size: 12, weight: .medium))
                                    .foregroundStyle(connected ? theme.colors.green : theme.colors.accentInk)
                                }
                            }
                        }
                    }
                    if rowCount == 0 {
                        Text(theme.strings.noMatches(query))
                            .buiFont(.caption)
                            .foregroundStyle(theme.colors.ink3)
                            .padding(.horizontal, 8)
                            .frame(maxWidth: .infinity, minHeight: 36, alignment: .leading)
                    }
                }
            }
            .animation(BUICurve.outStrong.animation(duration: 0.22), value: model.highlightedRow)
            if menu != .attach {
                Text(menu == .mention ? theme.strings.sourcesHint : theme.strings.commandsHint)
                    .buiFont(BUITextStyle(size: 11))
                    .foregroundStyle(theme.colors.ink3)
                    .padding(.horizontal, 8)
                    .padding(.top, 6)
                    .padding(.bottom, 4)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .overlay(alignment: .top) { Rectangle().fill(theme.colors.line).frame(height: 1) }
                    .padding(.top, 4)
            }
        }
        .padding(4)
        .buiSurface(radius: theme.radii.card, elevation: .raised)
    }

    private func menuRow<Leading: View, Trailing: View>(
        index: Int,
        name: String,
        detail: String,
        action: @escaping () -> Void,
        @ViewBuilder leading: () -> Leading,
        @ViewBuilder trailing: () -> Trailing
    ) -> some View {
        Button(action: action) {
            HStack(spacing: 10) {
                leading()
                Text(name)
                    .buiFont(BUITextStyle(size: 12.5, weight: .medium))
                    .foregroundStyle(theme.colors.ink)
                    .lineLimit(1)
                    .fixedSize()
                Text(detail)
                    .buiFont(.caption)
                    .foregroundStyle(theme.colors.ink3)
                    .lineLimit(1)
                    .truncationMode(.middle)
                    .frame(maxWidth: .infinity, alignment: .leading)
                trailing()
            }
            .padding(.horizontal, 8)
            .frame(height: 36)
            .contentShape(.rect(cornerRadius: 6))
        }
        .buttonStyle(MenuRowStyle(index: index, highlighted: $model.highlightedRow))
        .accessibilityLabel(name)
        .accessibilityHint(detail)
    }

    @ViewBuilder
    private func sourceIcon(_ icon: PromptSource.Icon) -> some View {
        switch icon {
        case .glyph(let glyph):
            BUIIcon(glyph, size: 15, lineWidth: 1.8)
                .foregroundStyle(theme.colors.ink2)
                .frame(width: 22, height: 22)
        case .mark(let avatar, let size):
            avatar.view(width: size.width, height: size.height)
                .frame(width: 22, height: 22)
        case .symbol(let name):
            Image(systemName: name)
                .font(.system(size: 13, weight: .medium))
                .foregroundStyle(theme.colors.ink2)
                .frame(width: 22, height: 22)
        }
    }

    private var modelMenu: some View {
        VStack(spacing: 0) {
            ForEach(Array(models.enumerated()), id: \.element.id) { index, option in
                Button {
                    model.selectedModelID = option.id
                    model.isModelMenuOpen = false
                } label: {
                    HStack(spacing: 8) {
                        Text(option.name)
                            .buiFont(BUITextStyle(size: 12.5, weight: .medium))
                            .foregroundStyle(theme.colors.ink)
                            .lineLimit(1)
                            .frame(maxWidth: .infinity, alignment: .leading)
                        Text(option.tag)
                            .buiFont(BUITextStyle(size: 11))
                            .foregroundStyle(theme.colors.ink3)
                            .lineLimit(1)
                        BUIIcon(.check, size: 13, lineWidth: 2.5)
                            .foregroundStyle(theme.colors.ink)
                            .opacity(option.id == model.selectedModelID ? 1 : 0)
                    }
                    .padding(.horizontal, 8)
                    .frame(height: 30)
                    .contentShape(.rect(cornerRadius: 6))
                }
                .buttonStyle(PressHighlightStyle(radius: 6, color: theme.colors.hover))
                .accessibilityAddTraits(option.id == model.selectedModelID ? .isSelected : [])
                .id(index)
            }
        }
        .padding(4)
        .frame(width: Self.modelMenuWidth)
        .buiSurface(radius: theme.radii.card, elevation: .raised)
    }

    // MARK: Actions

    private func pick(source: PromptSource) {
        let prefix = model.token.map { String(model.text.prefix($0.start)) } ?? model.text
        switch source.kind {
        case .attach:
            onAttach?()
            options.onAttachSource?(source)
            if model.token != nil { setText(prefix) }
        case .source, .connectable:
            if options.onPickSource?(source) == true {
                if model.token != nil { setText(prefix) }
            } else {
                setText(prefix + (source.insert ?? "@\(source.name) "))
                focus.wrappedValue = true
            }
        }
        model.isSourceMenuOpen = false
    }

    private func pick(command: PromptCommand) {
        let prefix = model.token.map { String(model.text.prefix($0.start)) } ?? model.text
        if options.onPickCommand?(command) == true {
            setText(prefix)
            return
        }
        setText(prefix + (command.insert ?? "\(command.name) "))
        focus.wrappedValue = true
    }

    private func send() {
        guard canSend else { return }
        onSend?(model.text.trimmingCharacters(in: .whitespacesAndNewlines))
        setText("")
        model.attachments = []
        model.isSourceMenuOpen = false
        model.isModelMenuOpen = false
    }
}

/// Composer controls: `ink-3`, filling `hover` and turning `ink` while pressed or active,
/// shrinking to 94 %.
private struct ControlButtonStyle: ButtonStyle {
    var shape: AnyShape
    var active: Bool
    @Environment(\.buiTheme) private var theme

    func makeBody(configuration: Configuration) -> some View {
        let on = configuration.isPressed || active
        configuration.label
            .foregroundStyle(on ? theme.colors.ink : theme.colors.ink3)
            .background(on ? theme.colors.hover : .clear, in: shape)
            .scaleEffect(configuration.isPressed ? 0.94 : 1)
            .animation(.easeOut(duration: 0.15), value: configuration.isPressed)
    }
}

private struct ShrinkStyle: ButtonStyle {
    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .scaleEffect(configuration.isPressed ? 0.94 : 1)
            .animation(.easeOut(duration: 0.15), value: configuration.isPressed)
    }
}

/// Moves the menu's gliding highlight to the row under the finger.
private struct MenuRowStyle: ButtonStyle {
    var index: Int
    @Binding var highlighted: Int?

    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .onChange(of: configuration.isPressed) { _, pressed in
                if pressed { highlighted = index }
            }
    }
}

/// Dictation level meter: three 2.5 pt bars bouncing (`eq-bounce`, 900 ms, staggered 150 ms).
private struct LevelBars: View {
    @State private var start = Date()
    private static let bounce = BUIKeyframes([(0, 0.35), (0.5, 1), (1, 0.35)], curve: .easeInOut)

    var body: some View {
        TimelineView(.animation(minimumInterval: 1 / 30)) { context in
            let t = context.date.timeIntervalSince(start)
            HStack(spacing: 2.5) {
                ForEach(0..<3, id: \.self) { i in
                    Capsule()
                        .frame(width: 2.5, height: 14)
                        .scaleEffect(y: Self.bounce.value(elapsed: t, duration: 0.9, delay: Double(i) * 0.15, rest: 1))
                }
            }
        }
        .frame(height: 14)
    }
}

/// A one-shot rainbow band sweeping left to right behind the composer's controls
/// (the original runs a WebGL shader for this).
private struct RainbowSweep: View {
    var trigger: Int
    @State private var progress: CGFloat = 0
    @State private var visible = false

    private static let colors: [Color] = [
        Color(hex: 0xFF453A), Color(hex: 0xFF9F0A), Color(hex: 0xFFD60A), Color(hex: 0x32D74B),
        Color(hex: 0x64D2FF), Color(hex: 0x0A84FF), Color(hex: 0xBF5AF2),
    ]

    var body: some View {
        GeometryReader { proxy in
            let w = proxy.size.width
            LinearGradient(colors: Self.colors, startPoint: .leading, endPoint: .trailing)
                .frame(width: w * 0.7)
                .blur(radius: 14)
                .offset(x: -w * 0.7 + (w * 1.7) * progress)
                .opacity(visible ? 0.55 : 0)
        }
        .allowsHitTesting(false)
        .onChange(of: trigger) {
            progress = 0
            visible = true
            // easeOutExpo over 570 ms, then an 80 ms outro.
            withAnimation(BUICurve(0.16, 1, 0.3, 1).animation(duration: 0.57)) { progress = 1 }
            withAnimation(.easeOut(duration: 0.08).delay(0.5)) { visible = false }
        }
    }
}
