import SwiftUI
import UIKit
import UniformTypeIdentifiers

/// Whether the clipboard holds pictures the composer has not offered yet (#4573): asked with
/// `hasImages`, which reads no content and so shows no Allow Paste prompt. Pasteboard metadata is read off the main
/// actor, and a copy made while the app is in the background is seen when it comes back.
@MainActor @Observable final class PasteboardImages {
    nonisolated struct Snapshot: Sendable {
        var hasImages: Bool
        var changeCount: Int?
        var retry = false
    }
    typealias Reader = @Sendable () -> Snapshot
    typealias ChangeCountReader = @Sendable () -> Int

    static let shared = PasteboardImages(center: .default, read: PasteboardImages.readSnapshot)

    private(set) var offered = false
    /// The clipboard's `changeCount` taken or dismissed: the same copy is offered once.
    @ObservationIgnored private var handled = -1
    /// The copy which produced the current offer. It is observable so Close captures the identity from its own render.
    private(set) var offerCount: Int?
    @ObservationIgnored private var observers: [NSObjectProtocol] = []
    @ObservationIgnored private let reader: Reader
    @ObservationIgnored private let changeCountReader: ChangeCountReader
    /// One read at a time. A notification while it runs requests exactly one trailing read.
    @ObservationIgnored private var readTask: Task<Void, Never>?
    @ObservationIgnored private var dirty = false

    init(center: NotificationCenter, read: @escaping Reader,
         changeCount: @escaping ChangeCountReader = { UIPasteboard.general.changeCount }) {
        reader = read
        changeCountReader = changeCount
        for name in [UIPasteboard.changedNotification, UIApplication.didBecomeActiveNotification] {
            observers.append(center.addObserver(forName: name, object: nil, queue: .main) { [weak self] _ in
                MainActor.assumeIsolated { self?.refresh() }
            })
        }
        refresh()
    }

    func refresh() {
        if readTask != nil {
            dirty = true
            return
        }
        startRead()
    }

    /// The system PasteButton already handed over the current copy. Keep the existing synchronous metadata read on this
    /// explicit paste action so a delayed observer read cannot consume a different copy. Get Clipboard never calls this.
    func take() {
        handled = changeCountReader()
        offerCount = nil
        offered = false
        refresh()
    }

    /// Close carries the count captured by its own SwiftUI presentation. If a newer offer arrived before the queued
    /// action ran, leave that offer alone rather than consuming a copy the user never saw.
    func dismiss(_ presentedCount: Int? = nil) {
        guard let presentedCount = presentedCount ?? offerCount, presentedCount == offerCount else { return }
        handled = presentedCount
        offerCount = nil
        offered = false
        refresh()
    }

    #if DEBUG
    /// Tests await the current read and any trailing read requested while it was in flight.
    func idle() async {
        while let readTask { await readTask.value }
    }
    #endif

    /// Taken or dismissed: the same copy is not offered again.
    nonisolated static func image(_ provider: NSItemProvider) async -> UIImage? {
        await withCheckedContinuation { c in
            _ = provider.loadDataRepresentation(for: .image) { @Sendable data, _ in c.resume(returning: data.flatMap(UIImage.init(data:))) }
        }
    }

    private func startRead() {
        dirty = false
        let reader = reader
        readTask = Task { @MainActor [weak self] in
            let snapshot = await Task.detached { reader() }.value
            self?.finishRead(snapshot)
        }
    }

    private func finishRead(_ snapshot: Snapshot) {
        readTask = nil
        if snapshot.retry || dirty {
            startRead()
            return
        }
        if snapshot.hasImages, let changeCount = snapshot.changeCount, changeCount != handled {
            offerCount = changeCount
            offered = true
        } else {
            offerCount = nil
            offered = false
        }
    }

    nonisolated static func readSnapshot() -> Snapshot {
        let board = UIPasteboard.general
        // A single count before and after hasImages keeps a concurrent text/image replacement from becoming a false
        // image offer. Retry off-main if the pasteboard changed between the two metadata calls.
        for _ in 0..<3 {
            let before = board.changeCount
            let hasImages = board.hasImages
            let after = board.changeCount
            if before == after {
                return Snapshot(hasImages: hasImages, changeCount: hasImages ? after : nil)
            }
        }
        return Snapshot(hasImages: false, changeCount: nil, retry: true)
    }
}

/// Paste over a focused composer while the clipboard holds a picture (copied in Safari, Photos, Lark, or a screenshot's
/// Copy): one tap attaches it, as Signal and Telegram offer. The text field itself only pastes text.
struct PasteImageChip: View {
    var focused: Bool
    var attach: (UIImage) -> Void
    @State private var board = PasteboardImages.shared

    var body: some View {
        if focused && board.offered, let offerCount = board.offerCount {
            HStack(spacing: Theme.Space.s) {
                // The system's paste button: a tap is the consent, so iOS shows no Allow Paste prompt.
                PasteButton(supportedContentTypes: [.image]) { providers in
                    // Handle the copy at the button callback, before loading providers can yield to a newer copy.
                    board.take()
                    Task { @MainActor in
                        // each goes to `attach`, which holds it to the hub's pictures and sends the rest as files (#12973):
                        // no more are read than a message and its files can take
                        for p in providers.prefix(ImageLimits.standard.perMessage + FileAttachments.most) {
                            if let img = await PasteboardImages.image(p) { attach(img) }
                        }
                    }
                }
                .labelStyle(.titleAndIcon)
                .buttonBorderShape(.capsule)
                .tint(Theme.accent)
                .accessibilityIdentifier("composer-paste-image")
                Spacer(minLength: 4)
                Button {
                    board.dismiss(offerCount)
                } label: {
                    ThemedIcon(Theme.Icon.close).font(Theme.Font.label).foregroundStyle(.secondary)
                        .scaledFrame(minWidth: Theme.Size.wellXS, minHeight: Theme.Size.wellXS).contentShape(.rect)
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Don't paste")
            }
            .padding(.horizontal, Theme.Space.m)
            .padding(.vertical, Theme.Space.s)
            .surface(tint: Theme.accent.opacity(0.06), interactive: true, in: .capsule)
            .transition(.move(edge: .bottom).combined(with: .opacity))
        }
    }
}
