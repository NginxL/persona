import SwiftUI
import Testing
import UIKit
@testable import Persona

/// The Agents header's own state transitions, without waiting for a screen animation.
@MainActor struct AgentsHeaderTests {
    /// Closing leaves `floating` on until the field reaches its row, although `up` is already off. A tab return in
    /// that state must not consume the refold and leave the field out for good (#10166).
    @Test func returningWhileASearchClosesFoldsWhenTheCloseFinishes() async throws {
        let header = AgentsHeader()
        let host = UIHostingController(rootView: HeaderListFixture(header: header))
        let window = UIWindow(frame: CGRect(x: 0, y: 0, width: 402, height: 874))
        window.rootViewController = host
        window.isHidden = false
        defer { window.isHidden = true; window.rootViewController = nil }
        host.view.layoutIfNeeded()
        await nextMainQueueTurn()

        let list = try #require(header.listScrollView)
        try #require(list.contentSize.height > 0 && list.bounds.height > 0)
        let top = -list.adjustedContentInset.top
        list.setContentOffset(CGPoint(x: 0, y: top), animated: false)
        header.floating = true
        header.up = false // Cancel has started the way down; its completion has not cleared `floating` yet.

        header.refold()
        await nextMainQueueTurn() // The old implementation consumes `folded` here and returns without folding.
        header.floating = false // The close finishes; the requested fold must happen now.
        await nextMainQueueTurn()

        #expect(abs(list.contentOffset.y - (top + AgentsHeader.band)) < 0.5,
                "the field stayed out: offset \(list.contentOffset.y), folded \(top + AgentsHeader.band)")
    }

    /// FIFO behind the header's queued attempt, rather than a timing sleep.
    private func nextMainQueueTurn() async {
        await withCheckedContinuation { continuation in
            DispatchQueue.main.async { continuation.resume() }
        }
    }
}

/// A real window and collection view for `AgentsHeaderProbe`'s responder-chain lookup.
private struct HeaderListFixture: View {
    let header: AgentsHeader

    var body: some View {
        HeaderCollectionFixture()
            .frame(width: 320, height: 500)
            .background { AgentsHeaderProbe(header: header) }
    }
}

private struct HeaderCollectionFixture: UIViewRepresentable {
    func makeUIView(context: Context) -> UICollectionView {
        HeaderCollection(frame: .zero, collectionViewLayout: UICollectionViewFlowLayout())
    }

    func updateUIView(_ list: UICollectionView, context: Context) {}
}

private final class HeaderCollection: UICollectionView {
    override var contentSize: CGSize {
        get { CGSize(width: 320, height: 1_200) }
        set {}
    }
}
