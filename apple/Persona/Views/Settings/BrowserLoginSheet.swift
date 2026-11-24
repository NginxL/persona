import SwiftUI
import VisionKit

// Settings › Devices › Scan Browser Sign-in Code (#10293): one sheet that scans the web sign-in page's code, then asks
// whether that browser may sign in, then shows the number to type there, then says how it ended. Every step leaves by
// the ✕ at the top leading corner; until the number is typed, leaving declines the request (the browser is told).

struct BrowserLoginSheet: View {
    @Environment(\.dismiss) private var dismiss
    /// Set by the scanner (or from the start: a DEBUG sample).
    @State private var page: BrowserLoginApprovalModel?
    @State private var notOurs = false
    @State private var canScan = false

    init() {
        #if DEBUG
        // `-showBrowserLoginSample <step>`: that step with sample data (BrowserLoginSample). Read here, not handed in:
        // a model handed to the sheet from the page's state was sometimes not there yet when the sheet was made.
        if let phase = BrowserLoginSample.current { _page = State(initialValue: BrowserLoginApprovalModel(sample: phase)) }
        #endif
    }

    var body: some View {
        NavigationStack {
            Group {
                if let page { BrowserLoginApprovalPage(page: page) { dismiss() } } else { scanner }
            }
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                SheetClose(id: "browserLogin.close") {
                    page?.decline()
                    dismiss()
                }
            }
        }
        .presentationDetents([.large])
        .presentationDragIndicator(.visible)
        // A swipe down leaves as the ✕ does: before the number is typed, the request is declined.
        .onDisappear { page?.decline() }
    }

    @ViewBuilder private var scanner: some View {
        SettingsForm {
            if canScan {
                Section {
                    QRScanner { code in
                        guard page == nil else { return }
                        if let link = BrowserLoginLink(code) { page = BrowserLoginApprovalModel(link) } else { notOurs = true }
                    }
                    .scaledFrame(height: Theme.Size.box280)
                    .clipShape(.rect(cornerRadius: Theme.Radius.card, style: .continuous))
                    .listRowInsets(EdgeInsets())
                    .accessibilityIdentifier("browserLogin.scanner")
                } footer: {
                    Text(notOurs ? Self.notOurs : Self.where_)
                        .foregroundStyle(notOurs ? Theme.danger : .secondary)
                }
            } else {
                Section { Text("This device can't scan codes with its camera.").foregroundStyle(.secondary) }
            }
        }
        .themedPage()
        .navigationTitle("Scan Browser Sign-in Code")
        .task { canScan = DataScannerViewController.isSupported && DataScannerViewController.isAvailable }
    }

    private static let where_ = String(localized: "Scan the code on the browser's sign-in page.")
    private static let notOurs = String(localized: "That isn't a browser sign-in code. Scan the code on the browser's sign-in page.")
}

/// The steps after the code is read: the request, the number, how it ended.
private struct BrowserLoginApprovalPage: View {
    let page: BrowserLoginApprovalModel
    let close: () -> Void

    var body: some View {
        content
            .themedPage()
            .task { await page.load() }
            .task(id: page.showsNumber) { if page.showsNumber { await page.watch() } }
    }

    @ViewBuilder private var content: some View {
        switch page.phase {
        case .loading:
            SettingsForm {
                Section {
                    HStack(spacing: Theme.Space.s) {
                        ProgressView()
                        Text("Checking the sign-in request…").foregroundStyle(.secondary)
                    }
                    .frame(maxWidth: .infinity)
                }
            }
        case .asking(let request, let allowing):
            asking(request, allowing: allowing)
        case .number(let number):
            numberPage(number)
        case .done(let outcome):
            done(outcome)
        }
    }

    // MARK: The request

    private func asking(_ r: BrowserLoginApprovalModel.Request, allowing: Bool) -> some View {
        SettingsForm {
            Section {
                VStack(spacing: Theme.Space.s) {
                    Text("Sign In on This Browser?")
                        .font(Theme.Font.title)
                        .multilineTextAlignment(.center)
                        .accessibilityAddTraits(.isHeader)
                    if r.network != .internet {
                        Text(Self.onlyYours)
                            .font(Theme.Font.secondary).foregroundStyle(.secondary)
                            .multilineTextAlignment(.center)
                    }
                }
                .frame(maxWidth: .infinity)
                .listRowBackground(Color.clear)
            }
            if r.network == .internet {
                Section {
                    NoticeBanner(text: String(localized: "This request came from the internet. Only allow a browser you are signing in to yourself, right now."),
                                 tone: .caution, lineLimit: nil)
                        .listRowInsets(EdgeInsets())
                        .listRowBackground(Color.clear)
                        .accessibilityIdentifier("browserLogin.internetWarning")
                }
            }
            Section {
                fact(String(localized: "Browser"), r.name, note: String(localized: "The name the browser gave itself"))
                fact(String(localized: "From [Browser Sign-in]"), Self.from(r.network),
                     note: r.network == .unknown ? String(localized: "The request was forwarded, so its real source can't be seen") : nil,
                     tint: r.network == .internet ? Theme.caution : nil)
                fact(String(localized: "Requested"), r.requestedAt.formatted(.relative(presentation: .named)))
                fact(String(localized: "Gets"), String(localized: "The same full access as this iPhone"),
                     note: String(localized: "You can revoke it any time in Settings › Devices"))
            }
        }
        .floatingFooter(over: Theme.groupedPage) {
            VStack(spacing: Theme.Space.s) {
                Button { Task { await page.allow() } } label: {
                    Group {
                        if allowing { ProgressView().tint(.white) } else { Text("Allow Sign-in").buttonLabelLine(minimumScale: 1) }
                    }
                    .floatingPill(prominent: true)
                }
                .buttonStyle(.plain)
                .disabled(allowing)
                .accessibilityIdentifier("browserLogin.allow")
                Button {
                    page.decline()
                    close()
                } label: {
                    Text("Decline [Browser Sign-in]").buttonLabelLine(minimumScale: 1).floatingPill(prominent: false)
                }
                .buttonStyle(.plain)
                .disabled(allowing)
                .accessibilityIdentifier("browserLogin.decline")
            }
            .frame(maxWidth: Theme.Size.readable)
            .frame(maxWidth: .infinity)
            .padding(.horizontal, Theme.Space.page)
            .padding(.vertical, Theme.Space.s)
        }
    }

    private static let onlyYours = String(localized: "Only allow a browser you are signing in to yourself, right now.")

    static func from(_ n: BrowserLoginApprovalModel.Network) -> String {
        switch n {
        case .lan: String(localized: "Your local network")
        case .internet: String(localized: "The internet")
        case .unknown: String(localized: "Can't tell")
        }
    }

    private func fact(_ title: String, _ value: String, note: String? = nil, tint: Color? = nil) -> some View {
        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
            Text(title).font(Theme.Font.label).foregroundStyle(.secondary)
            Text(value).font(Theme.Font.body).foregroundStyle(tint ?? .primary)
            if let note { Text(note).font(Theme.Font.label).foregroundStyle(.secondary) }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .accessibilityElement(children: .combine)
    }

    // MARK: The number

    private func numberPage(_ number: String) -> some View {
        SettingsForm {
            Section {
                VStack(spacing: Theme.Space.m) {
                    Text("Type This Number in the Browser")
                        .font(Theme.Font.title)
                        .multilineTextAlignment(.center)
                        .accessibilityAddTraits(.isHeader)
                    Text(number)
                        .font(Theme.Font.signInNumber)
                        // VoiceOver reads the digits one by one ("four seven"), as they are typed.
                        .accessibilityLabel(Text(number.map(String.init).joined(separator: " ")))
                        .accessibilityIdentifier("browserLogin.number")
                    VStack(spacing: Theme.Space.xs) {
                        Text("Don't tell anyone this number.")
                        Text("One try: a wrong number cancels this sign-in.")
                    }
                    .font(Theme.Font.secondary).foregroundStyle(.secondary)
                    .multilineTextAlignment(.center)
                    .fixedSize(horizontal: false, vertical: true)
                    HStack(spacing: Theme.Space.s) {
                        ProgressView()
                        Text("Waiting for the browser…").foregroundStyle(.secondary)
                    }
                    .font(Theme.Font.secondary)
                }
                .frame(maxWidth: .infinity)
                .listRowBackground(Color.clear)
            }
        }
        .floatingFooter(over: Theme.groupedPage) {
            Button {
                page.decline()
                close()
            } label: {
                Text("Cancel Sign-in").buttonLabelLine(minimumScale: 1)
                    .foregroundStyle(Theme.danger)
                    .floatingPill(prominent: false)
            }
            .buttonStyle(.plain)
            .accessibilityIdentifier("browserLogin.cancel")
            .frame(maxWidth: Theme.Size.readable)
            .frame(maxWidth: .infinity)
            .padding(.horizontal, Theme.Space.page)
            .padding(.vertical, Theme.Space.s)
        }
    }

    // MARK: How it ended

    private func done(_ outcome: BrowserLoginApprovalModel.Outcome) -> some View {
        let (symbol, tint, title, detail) = Self.words(outcome)
        return SettingsForm {
            Section {
                VStack(spacing: Theme.Space.s) {
                    ThemedIcon(symbol)
                        .font(Theme.Font.display)
                        .foregroundStyle(tint)
                    Text(title).font(Theme.Font.heading).multilineTextAlignment(.center)
                    if let detail {
                        Text(detail).font(Theme.Font.secondary).foregroundStyle(.secondary).multilineTextAlignment(.center)
                    }
                }
                .frame(maxWidth: .infinity)
                .padding(.vertical, Theme.Space.l)
                .accessibilityElement(children: .combine)
                .accessibilityIdentifier("browserLogin.result")
                .listRowBackground(Color.clear)
            }
        }
        .floatingFooter(over: Theme.groupedPage) {
            Button(action: close) {
                Text("Done [Browser Sign-in]").buttonLabelLine(minimumScale: 1).floatingPill(prominent: false)
            }
            .buttonStyle(.plain)
            .accessibilityIdentifier("browserLogin.done")
            .frame(maxWidth: Theme.Size.readable)
            .frame(maxWidth: .infinity)
            .padding(.horizontal, Theme.Space.page)
            .padding(.vertical, Theme.Space.s)
        }
    }

    static func words(_ o: BrowserLoginApprovalModel.Outcome) -> (String, Color, String, String?) {
        switch o {
        case .signedIn(let name):
            (Theme.Icon.successFilled, Theme.positive, String(localized: "Browser Signed In"),
             String(localized: "“\(name)” is now in Settings › Devices."))
        case .voided:
            (Theme.Icon.error, Theme.danger, String(localized: "The number didn't match, so this sign-in was cancelled"),
             String(localized: "Refresh the code in the browser to try again."))
        case .declined:
            (Theme.Icon.close, .secondary, String(localized: "This sign-in was declined"), nil)
        case .expired:
            (Theme.Icon.time, .secondary, String(localized: "This code has expired"),
             String(localized: "Refresh the code in the browser to try again."))
        case .failed(let message):
            (Theme.Icon.error, Theme.danger, String(localized: "Couldn't check this sign-in"), message)
        }
    }
}

extension BrowserLoginApprovalModel {
    var showsNumber: Bool { if case .number = phase { true } else { false } }
}
