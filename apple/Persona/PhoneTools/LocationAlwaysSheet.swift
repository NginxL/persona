#if os(iOS)
import SwiftUI

/// The offer of Always (#13200), once, the first time Persona is opened after a location read failed in the background
/// for While Using: why it shows now, the points, then Allow (prominent) and Not Now pinned under them, as the all-day
/// recording's consent sheet (docs/design/DESIGN_SYSTEM.md 3.6). Only Allow asks iOS; Not Now and a swipe down change
/// nothing, and Settings › Connectors › This iPhone keeps the way to it.
struct LocationAlwaysSheet: View {
    /// The owner's answer: true for Allow. The presenter acts on it; the sheet closes itself.
    var answer: (Bool) -> Void
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: Theme.Space.l) {
                    heading
                    VStack(alignment: .leading, spacing: Theme.Space.l) {
                        ForEach(LocationAlwaysPoint.allCases) { point in
                            row(point)
                        }
                    }
                    Text("iOS asks next. You can change it at any time in iOS Settings › Persona › Location.")
                        .font(Theme.Font.caption).foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
                .padding(.horizontal, Theme.Space.page)
                .padding(.vertical, Theme.Space.l)
                .frame(maxWidth: Theme.Size.readable, alignment: .leading)
                .frame(maxWidth: .infinity)
            }
            .themedGroupedPage()
            .safeAreaInset(edge: .bottom, spacing: 0) { footer }
            .navigationTitle(PhoneDataSource.location.title)
            .navigationBarTitleDisplayMode(.inline)
        }
        .presentationDetents([.large])
        .presentationDragIndicator(.visible)
        .accessibilityIdentifier("location-always-sheet")
    }

    private var heading: some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            ThemedIcon(Theme.Icon.currentLocation).font(Theme.Font.display).foregroundStyle(Theme.accent)
                .accessibilityHidden(true)
            Text("Location When Persona Isn't Open").font(Theme.Font.title)
                .fixedSize(horizontal: false, vertical: true)
                .accessibilityAddTraits(.isHeader)
            Text("The assistant needed your location just now, but iOS lets Persona read it only while Persona is open.")
                .font(Theme.Font.secondary).foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
        }
    }

    /// One point: its mark beside its words, one spoken element.
    private func row(_ point: LocationAlwaysPoint) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: Theme.Space.m) {
            ThemedIcon(point.symbol).font(Theme.Font.heading).foregroundStyle(Theme.accent)
                .scaledFrame(width: Theme.Size.icon)
                .accessibilityHidden(true)
            Text(point.text).font(Theme.Font.body)
                .fixedSize(horizontal: false, vertical: true)
        }
        .accessibilityElement(children: .combine)
    }

    private var footer: some View {
        VStack(spacing: Theme.Space.s) {
            Button { reply(true) } label: {
                Text("Allow").font(Theme.Font.heading).lineLimit(1)
                    .scaledFrame(maxWidth: .infinity, minHeight: Theme.Size.tapTarget)
            }
            .themedButton(prominent: true)
            .accessibilityIdentifier("location-always-allow")
            Button { reply(false) } label: {
                Text("Not Now [Location Always]").font(Theme.Font.body).lineLimit(1)
                    .scaledFrame(maxWidth: .infinity, minHeight: Theme.Size.tapTarget)
            }
            .buttonStyle(.borderless)
            .accessibilityIdentifier("location-always-not-now")
        }
        .padding(.horizontal, Theme.Space.xl)
        .padding(.vertical, Theme.Space.m)
        .frame(maxWidth: Theme.Size.readable)
        .frame(maxWidth: .infinity)
        .background(Theme.groupedPage.ignoresSafeArea(edges: .bottom))
    }

    private func reply(_ allowed: Bool) {
        answer(allowed)
        dismiss()
    }
}

extension View {
    /// The offer of Always over every screen, when `BackgroundLocation` has one (#13200).
    func locationAlwaysOffer() -> some View { modifier(LocationAlwaysOfferPresenter()) }
}

private struct LocationAlwaysOfferPresenter: ViewModifier {
    @Bindable private var location = BackgroundLocation.shared

    func body(content: Content) -> some View {
        content.sheet(isPresented: $location.offering) {
            LocationAlwaysSheet { allowed in
                if allowed { location.allowAlways() }
            }
            .onAppear { location.offerShown() } // shown once, even if it closes without an answer
        }
    }
}
#endif
