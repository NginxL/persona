import SwiftUI

/// Persona's mark wherever the app signs a picture (#9468, #9508). One component for every place, in two sizes.
/// `.mark` is the small signature at the foot of an exported picture: the assistant's face, its name and "(Persona)"
/// after it, quiet. `.banner` is what a screenshot shows where a chat's top bar is: the face and the name at the
/// leading edge, GitHub's mark and `repository` at the trailing one (#10172, `PersonaBannerRow`), large or slim, with
/// the repository wherever it can be.
struct PersonaBrandLockup: View {
    enum Size {
        case mark, banner
    }

    @Environment(AppModel.self) private var model
    var size: Size = .mark
    /// The name shown unless given: the banner's is whose page it is, as the top bar says it (`AppModel.assistantTitleName`,
    /// #12243: with one assistant the current one's, not the persona's); the mark's the assistant's (`assistantShownName`).
    var name: String?

    var body: some View {
        let shown = name ?? (size == .banner ? model.assistantTitleName : model.assistantShownName)
        switch size {
        case .mark:
            HStack(spacing: Theme.Space.xs) {
                PersonaAvatar(size: Theme.Size.badge)
                Text(verbatim: shown)
                    .font(Theme.Font.labelStrong)
                    .foregroundStyle(.secondary)
                // Its name already says Persona: not "Persona (Persona)".
                if Self.showsProduct(after: shown) {
                    Text("(Persona) [Brand Lockup]")
                        .font(Theme.Font.label)
                        .foregroundStyle(.tertiary)
                }
            }
            .lineLimit(1)
            .accessibilityElement(children: .combine)
        case .banner:
            // The repository is why the banner is there (a screenshot carries it away): the large row with it, then
            // the slim one with it (a folded header, a narrow page, large text), and only then GitHub's mark alone.
            ViewThatFits(in: [.horizontal, .vertical]) {
                PersonaBannerRow(name: shown, slim: false, showsRepository: true)
                PersonaBannerRow(name: shown, slim: true, showsRepository: true)
                PersonaBannerRow(name: shown, slim: true, showsRepository: false)
            }
            // A bar's text (6.2): it grows with the text size up to xxxLarge, never past the bar.
            .dynamicTypeSize(...Theme.Shell.topBarLargestTypeSize)
        }
    }

    /// The product's own name, never translated.
    static let product = "Persona"

    /// Where Persona's source is (#10172): GitHub's owner and name, as the screenshot banner shows them. Never
    /// translated; the only place the app spells it out for display.
    static let repository = "nwverse/persona"

    /// "(Persona)" follows the name unless the name already is Persona's.
    static func showsProduct(after name: String) -> Bool {
        name.trimmingCharacters(in: .whitespaces).caseInsensitiveCompare(product) != .orderedSame
    }
}

/// The screenshot banner's row (#10172): the assistant's face and name at the leading edge, GitHub's mark and
/// `PersonaBrandLockup.repository` at the trailing one, one type level below the name in the secondary color (the
/// mark too), centred on one line. A picture, not a link: the banner takes no touches.
///
/// `PersonaBrandLockup` offers it large with the repository, slim with the repository, then slim with the mark alone
/// (`ViewThatFits`, which compares ideal sizes). With the repository the name asks for no more than its first
/// `nameLeast` characters and the ellipsis: a long name is cut at its tail where the row has room and the repository
/// stays whole; the repository goes only where not even the slim row has that much room.
struct PersonaBannerRow: View {
    var name: String
    var slim: Bool
    var showsRepository: Bool

    /// How much of the name the repository waits for: its first characters, before the ellipsis.
    static let nameLeast = 2

    var body: some View {
        HStack(spacing: Theme.Space.m) {
            HStack(spacing: Theme.Space.s) {
                PersonaAvatar(size: slim ? Theme.Size.wellXS : Theme.Size.avatarM)
                BannerName(name: name, least: showsRepository ? Self.nameLeast : nil)
                    .font(slim ? Theme.Font.heading : Theme.Font.display)
                    .foregroundStyle(.primary)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
            HStack(spacing: slim ? Theme.Space.xs : Theme.Space.sm) {
                BrandMark(Theme.Brand.github, side: slim ? Theme.Size.markM : Theme.Size.iconS)
                if showsRepository {
                    Text(verbatim: PersonaBrandLockup.repository)
                        .font(slim ? Theme.Font.secondaryStrong : Theme.Font.title)
                }
            }
            .foregroundStyle(.secondary)
            .fixedSize()
        }
        .lineLimit(1)
    }
}

/// The name, cut at its tail. With `least`, its ideal width (what `ViewThatFits` compares) is at most that of its first
/// `least` characters and the ellipsis; offered a width, it draws as much of itself as fits.
private struct BannerName: View {
    var name: String
    var least: Int?

    var body: some View {
        if let least, name.count > least {
            IdealCappedByLast {
                Text(verbatim: name).truncationMode(.tail)
                Text(verbatim: String(name.prefix(least)) + "\u{2026}").hidden()
            }
        } else {
            Text(verbatim: name).truncationMode(.tail)
        }
    }
}

/// The first view, drawn; its ideal width at most the last one's, which is laid out with it and hidden. Offered a
/// width, the first view's own answer.
private struct IdealCappedByLast: Layout {
    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        guard let view = subviews.first, let cap = subviews.last else { return .zero }
        let size = view.sizeThatFits(proposal)
        guard proposal.width == nil else { return size }
        return CGSize(width: min(size.width, cap.sizeThatFits(.unspecified).width), height: size.height)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        for view in subviews { view.place(at: bounds.origin, proposal: ProposedViewSize(bounds.size)) }
    }
}
