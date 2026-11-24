import SwiftUI

/// `SidebarNav`'s pieces for a host that lays out its own sidebar (a drawer with its own sections and row states):
/// a row, a group label and the search field.

/// A sidebar row: a symbol, the label with an optional line under it (a chat's last message), the host's views at the
/// end (a time, a state mark, a count); the current row keeps a fill. A row under another (`indent`) is set in so its
/// symbol stands where the row above's label starts.
public struct BUISidebarRow<Trailing: View>: View {
    var symbol: String?
    var label: String
    var detail: String?
    var isActive: Bool
    /// How many rows deep it is set in.
    var indent: Int
    var action: () -> Void
    var trailing: Trailing
    @Environment(\.buiTheme) private var theme

    /// The symbol's square and the gap after it, at the type scale: where the label starts, and one step of `indent`.
    private static func symbolSide(_ scale: CGFloat) -> CGFloat { 20 * scale }
    private static var gap: CGFloat { 8 } // a generic type has no static stored properties

    public init(_ label: String, symbol: String? = nil, detail: String? = nil, isActive: Bool = false, indent: Int = 0,
                action: @escaping () -> Void, @ViewBuilder trailing: () -> Trailing) {
        self.label = label
        self.symbol = symbol
        self.detail = detail
        self.isActive = isActive
        self.indent = indent
        self.action = action
        self.trailing = trailing()
    }

    public var body: some View {
        if indent > 0 {
            // The symbol's column and the gap after it, as the row draws them below.
            row.padding(.leading, CGFloat(indent) * (Self.symbolSide(theme.typeScale) + Self.gap))
        } else {
            row
        }
    }

    private var row: some View {
        Button(action: action) {
            HStack(spacing: Self.gap) {
                if let symbol {
                    Image(systemName: symbol)
                        .font(.system(size: 15 * theme.typeScale * 0.9, weight: .medium))
                        .foregroundStyle(isActive ? theme.colors.ink : theme.colors.ink2)
                        .frame(width: Self.symbolSide(theme.typeScale), height: Self.symbolSide(theme.typeScale))
                }
                VStack(alignment: .leading, spacing: 2) {
                    Text(label)
                        .buiFont(BUITextStyle(size: 14, weight: .medium, lineHeight: 1.19))
                        .foregroundStyle(isActive ? theme.colors.ink : theme.colors.ink2)
                        .lineLimit(1)
                    if let detail, !detail.isEmpty {
                        Text(detail)
                            .buiFont(BUITextStyle(size: 12.5, lineHeight: 1.19))
                            .foregroundStyle(theme.colors.ink3)
                            .lineLimit(1)
                    }
                }
                Spacer(minLength: 0)
                trailing
            }
            .padding(.horizontal, 8)
            .padding(.vertical, detail == nil ? 0 : 6)
            .frame(minHeight: 32 * max(1, theme.typeScale * 0.9), alignment: .leading)
            .background(isActive ? theme.colors.hover2 : .clear, in: .rect(cornerRadius: 8, style: .continuous))
            .contentShape(.rect(cornerRadius: 8))
        }
        .buttonStyle(RowPressScaleStyle())
        .accessibilityAddTraits(isActive ? .isSelected : [])
    }
}

public extension BUISidebarRow where Trailing == EmptyView {
    init(_ label: String, symbol: String? = nil, detail: String? = nil, isActive: Bool = false, action: @escaping () -> Void) {
        self.init(label, symbol: symbol, detail: detail, isActive: isActive, action: action) { EmptyView() }
    }
}

/// A sidebar group's label ("Chats", Today, Pin), with an optional control at its end.
public struct BUISidebarHeading<Accessory: View>: View {
    var title: String
    var accessory: Accessory
    @Environment(\.buiTheme) private var theme

    public init(_ title: String, @ViewBuilder accessory: () -> Accessory) {
        self.title = title
        self.accessory = accessory()
    }

    public var body: some View {
        HStack(spacing: 6) {
            Text(title).buiFont(BUITextStyle(size: 12.5, weight: .medium, lineHeight: 1.19))
            Spacer(minLength: 0)
            accessory
        }
        .foregroundStyle(theme.colors.ink3)
        .padding(.horizontal, 8)
        .accessibilityAddTraits(.isHeader)
    }
}

public extension BUISidebarHeading where Accessory == EmptyView {
    init(_ title: String) {
        self.init(title) { EmptyView() }
    }
}

/// The sidebar's search field, open: the magnifier, the field, and × once something is typed.
public struct BUISearchField: View {
    var prompt: String
    @Binding var text: String
    var focused: FocusState<Bool>.Binding?
    var clearLabel: String
    @Environment(\.buiTheme) private var theme

    public init(_ prompt: String, text: Binding<String>, focused: FocusState<Bool>.Binding? = nil, clearLabel: String = "Clear") {
        self.prompt = prompt
        _text = text
        self.focused = focused
        self.clearLabel = clearLabel
    }

    public var body: some View {
        HStack(spacing: 6) {
            BUIIcon(.magnifier, size: 15).foregroundStyle(theme.colors.ink3)
            field
                .buiFont(BUITextStyle(size: 13, weight: .medium, lineHeight: 1.19))
                .foregroundStyle(theme.colors.ink)
                .submitLabel(.search)
            if !text.isEmpty {
                Button { text = "" } label: { BUIIcon(.close, size: 12, lineWidth: 2) }
                    .buttonStyle(IconButtonStyle(size: 28))
                    .accessibilityLabel(clearLabel)
            }
        }
        .padding(.leading, 8)
        .padding(.trailing, 2)
        .frame(minHeight: 34 * max(1, theme.typeScale * 0.9))
        .background(theme.colors.field, in: .rect(cornerRadius: 8, style: .continuous))
        .overlay { RoundedRectangle(cornerRadius: 8, style: .continuous).strokeBorder(theme.colors.line, lineWidth: 1) }
    }

    @ViewBuilder private var field: some View {
        if let focused {
            TextField(prompt, text: $text).focused(focused)
        } else {
            TextField(prompt, text: $text)
        }
    }
}
