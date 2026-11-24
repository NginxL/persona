import PersonaKit
import SwiftUI
import UIKit

/// The image viewer's pages on the phone: a paging scroll view of zooming pictures that UIKit owns end to end (#5678).
/// It replaced a page-style `TabView`, which now and then stopped between two pages (a strip of the neighbour showing)
/// when the viewer re-rendered mid-swipe: a picture finishing decoding swapped its page's view, the selection changed,
/// or the pan was cancelled. Here SwiftUI updates only hand the pictures to existing pages and never move the scroll
/// view while a finger or a glide is moving it; `index` changes once a swipe has come to rest; and whenever the pages
/// come to rest off a page boundary (a cancelled pan, an interrupted animation, a new size) they move to the nearest
/// whole page (`PagerMath`).
struct ImagePagerView: UIViewRepresentable {
    var count: Int
    var images: [Int: UIImage]
    @Binding var index: Int
    var onTap: () -> Void
    var onPull: (CGFloat) -> Void
    var onPullEnd: (Bool) -> Void
    /// A page came on (or next to) the screen without its picture: load it.
    var onNeed: (Int) -> Void
    /// Pages whose picture did not load (#182): they draw `failure` instead of a spinner.
    var failed: Set<Int> = []
    var failure: (Int) -> AnyView = { _ in AnyView(EmptyView()) }

    func makeUIView(context: Context) -> PagerScrollView { PagerScrollView() }

    func updateUIView(_ v: PagerScrollView, context: Context) {
        let index = $index
        v.handlers = .init(tap: onTap, pull: onPull, pullEnd: onPullEnd, need: onNeed, failure: failure,
                           page: { i in if index.wrappedValue != i { index.wrappedValue = i } })
        v.update(count: count, images: images, failed: failed, page: index.wrappedValue)
    }
}

final class PagerScrollView: UIScrollView, UIScrollViewDelegate {
    struct Handlers {
        var tap: () -> Void = {}
        var pull: (CGFloat) -> Void = { _ in }
        var pullEnd: (Bool) -> Void = { _ in }
        var need: (Int) -> Void = { _ in }
        /// What a page that did not load says.
        var failure: (Int) -> AnyView = { _ in AnyView(EmptyView()) }
        /// The pages came to rest on this one.
        var page: (Int) -> Void = { _ in }
    }

    var handlers = Handlers()
    private var count = 0
    private var images: [Int: UIImage] = [:]
    private var failed: Set<Int> = []
    /// The page at rest (where a size change keeps the pages).
    private var page = 0
    private var pages: [Int: PagerPage] = [:]
    private var laidOutSize: CGSize = .zero
    /// A move to a whole page this view started is under way (`setContentOffset(_:animated:)`).
    private var moving = false
    #if DEBUG
    var testPageCount: Int { count }
    #endif

    init() {
        super.init(frame: .zero)
        isPagingEnabled = true
        showsHorizontalScrollIndicator = false
        showsVerticalScrollIndicator = false
        contentInsetAdjustmentBehavior = .never
        scrollsToTop = false
        backgroundColor = .clear
        delegate = self
        panGestureRecognizer.addTarget(self, action: #selector(panned(_:)))
        #if DEBUG
        ImagePagingProbe.shared.attach(self)
        #endif
    }

    required init?(coder: NSCoder) { fatalError("init(coder:) is not supported") }

    private var resting: Bool { !isTracking && !isDragging && !isDecelerating }

    /// A drag mostly downward is the page's pull to close, not a swipe: the pages' pan would otherwise take it first
    /// (a scroll view's pan starts in any direction once it can scroll in one).
    override func gestureRecognizerShouldBegin(_ g: UIGestureRecognizer) -> Bool {
        if g === panGestureRecognizer {
            let v = panGestureRecognizer.velocity(in: self)
            if v.y > 0, v.y > abs(v.x) { return false }
        }
        return super.gestureRecognizerShouldBegin(g)
    }

    /// From SwiftUI: how many, the pictures so far, the ones that did not load, the page it wants (a thumbnail tapped,
    /// or the one it opened on).
    func update(count: Int, images: [Int: UIImage], failed: Set<Int> = [], page wanted: Int) {
        if count != self.count { self.count = count; setNeedsLayout() }
        #if DEBUG
        ImagePagingProbe.shared.updated(images, previous: self.images)
        #endif
        self.images = images
        self.failed = failed
        for (i, p) in pages { show(p, i) }
        let target = min(max(0, wanted), max(0, count - 1))
        // A swipe under way decides where it ends; the binding catches up when it comes to rest.
        guard target != page, resting, !moving else { return }
        let jump = abs(target - page) > 1 || window == nil || bounds.width == 0
        page = target
        if jump {
            contentOffset.x = PagerMath.offset(page: target, width: bounds.width, count: count)
            setNeedsLayout()
        } else {
            moving = true
            setContentOffset(CGPoint(x: PagerMath.offset(page: target, width: bounds.width, count: count), y: 0), animated: true)
        }
    }

    override func layoutSubviews() {
        super.layoutSubviews()
        let size = bounds.size
        let content = CGSize(width: size.width * CGFloat(count), height: size.height)
        if contentSize != content { contentSize = content }
        if size != laidOutSize {
            // First layout, rotation, a new window size: stay on the page.
            laidOutSize = size
            moving = false
            contentOffset.x = PagerMath.offset(page: page, width: size.width, count: count)
        }
        layoutPages()
        // At rest between two pages (a pan the system cancelled, a move cut short): finish on a whole page. Not from
        // inside layout: the move scrolls, which lays out again.
        if resting, !moving, !PagerMath.isAligned(offset: contentOffset.x, width: size.width, count: count) {
            DispatchQueue.main.async { [weak self] in self?.settle() }
        }
    }

    /// Lays out the pages on screen and one on each side; the rest are dropped (a picture zoomed in starts fitted
    /// again when it comes back, as in Photos).
    private func layoutPages() {
        guard bounds.width > 0, count > 0 else {
            pages.values.forEach { $0.removeFromSuperview() }
            pages = [:]
            return
        }
        let live = PagerMath.livePages(offset: contentOffset.x, width: bounds.width, count: count)
        for (i, p) in pages where !live.contains(i) {
            p.removeFromSuperview()
            pages[i] = nil
        }
        for i in live {
            let p = pages[i] ?? makePage(i)
            let frame = CGRect(x: PagerMath.offset(page: i, width: bounds.width, count: count), y: 0, width: bounds.width, height: bounds.height)
            if p.frame != frame { p.frame = frame }
        }
    }

    private func makePage(_ i: Int) -> PagerPage {
        let p = PagerPage(index: i)
        p.handlers = .init(
            tap: { [weak self] in self?.handlers.tap() },
            pull: { [weak self] in self?.handlers.pull($0) },
            pullEnd: { [weak self] in self?.handlers.pullEnd($0) },
            // Pulling down only on a page at rest, never while the pages move sideways. Not `resting`: a finger on
            // the screen is already `isTracking`, before anyone knows which way it goes.
            canPull: { [weak self] in
                guard let self else { return false }
                return !isDragging && !isDecelerating && !moving && PagerMath.isAligned(offset: contentOffset.x, width: bounds.width, count: count)
            })
        pages[i] = p
        addSubview(p)
        show(p, i)
        // One that did not load waits for Retry.
        if images[i] == nil, !failed.contains(i) { handlers.need(i) }
        return p
    }

    private func show(_ p: PagerPage, _ i: Int) {
        p.show(images[i], failure: images[i] == nil && failed.contains(i) ? handlers.failure(i) : nil)
    }

    /// Ends on the nearest whole page, then says which.
    private func settle() {
        guard resting, count > 0, bounds.width > 0 else { return }
        let x = contentOffset.x
        let target = PagerMath.nearestPage(offset: x, width: bounds.width, count: count)
        let to = PagerMath.offset(page: target, width: bounds.width, count: count)
        if PagerMath.isAligned(offset: x, width: bounds.width, count: count) {
            moving = false
            if x != to { contentOffset.x = to }
            arrive(at: target)
        } else {
            moving = true
            setContentOffset(CGPoint(x: to, y: 0), animated: true)
        }
    }

    private func arrive(at i: Int) {
        for (j, p) in pages where j != i { p.resetZoom() }
        page = i
        handlers.page(i)
        #if DEBUG
        ImagePagingProbe.shared.settled(i)
        #endif
    }

    // MARK: UIScrollViewDelegate

    func scrollViewWillBeginDragging(_ scrollView: UIScrollView) { moving = false }

    func scrollViewDidEndDragging(_ scrollView: UIScrollView, willDecelerate decelerate: Bool) {
        if !decelerate { settle() }
    }

    func scrollViewDidEndDecelerating(_ scrollView: UIScrollView) { settle() }

    func scrollViewDidEndScrollingAnimation(_ scrollView: UIScrollView) {
        moving = false
        settle()
    }

    /// A pan the system took away (a call, Control Center, another gesture) may stop without the delegate's end: settle.
    @objc private func panned(_ g: UIPanGestureRecognizer) {
        // Capture the actual callback state before the DEBUG hook requests a UIKit cancellation. A changed callback
        // must not masquerade as a cancelled callback just because the hook disabled the recognizer inside it.
        let state = g.state
        #if DEBUG
        ImagePagingProbe.shared.pan(g)
        #endif
        guard state == .cancelled || state == .failed else { return }
        #if DEBUG
        ImagePagingProbe.shared.terminalAction()
        #endif
        DispatchQueue.main.async { [weak self] in self?.settle() }
    }
}

/// One page: a spinner until its picture is there, then the zooming picture; what the viewer says instead when the
/// picture did not load (#182). That is SwiftUI inside the page, so a swipe starting on it still turns the page and it
/// moves with the page.
final class PagerPage: UIView {
    var handlers = ZoomScrollView.Handlers()
    private let index: Int
    private let spinner = UIActivityIndicatorView(style: .medium)
    private var zoom: ZoomScrollView?
    private var failure: UIHostingController<AnyView>?

    init(index: Int) {
        self.index = index
        super.init(frame: .zero)
        backgroundColor = .clear
        clipsToBounds = true
        spinner.hidesWhenStopped = true
        addSubview(spinner)
    }

    required init?(coder: NSCoder) { fatalError("init(coder:) is not supported") }

    func show(_ image: UIImage?, failure said: AnyView? = nil) {
        if let said, image == nil {
            spinner.stopAnimating()
            if let failure { failure.rootView = said; return }
            let host = UIHostingController(rootView: said)
            host.view.backgroundColor = .clear
            host.safeAreaRegions = [] // the viewer's bars sit over the page; the words center in the whole page
            host.view.frame = bounds
            host.view.autoresizingMask = [.flexibleWidth, .flexibleHeight]
            addSubview(host.view)
            failure = host
            return
        }
        failure?.view.removeFromSuperview()
        failure = nil
        guard let image else {
            spinner.startAnimating()
            return
        }
        spinner.stopAnimating()
        if let zoom {
            zoom.handlers = handlers
            if zoom.imageView.image !== image { zoom.imageView.image = image; zoom.relayout() }
            return
        }
        let z = ZoomScrollView(image: image)
        z.handlers = handlers
        z.frame = bounds
        z.autoresizingMask = [.flexibleWidth, .flexibleHeight]
        // UI tests find the page under the middle of the screen by this (ImageViewerTests).
        z.accessibilityIdentifier = "viewer-page-\(index)"
        addSubview(z)
        zoom = z
    }

    func resetZoom() { zoom?.resetZoom() }

    override func layoutSubviews() {
        super.layoutSubviews()
        spinner.center = CGPoint(x: bounds.midX, y: bounds.midY)
    }
}

/// UIScrollView-backed zooming: native pinch, pan, bounce; double tap zooms to the tapped point (or back); a single
/// tap calls `tap`. At the fitted size a downward drag pulls the picture away: it follows the finger and shrinks,
/// `pull` reports how far (0…1), and `pullEnd` says whether to close.
final class ZoomScrollView: UIScrollView, UIScrollViewDelegate {
    struct Handlers {
        var tap: () -> Void = {}
        var pull: (CGFloat) -> Void = { _ in }
        var pullEnd: (Bool) -> Void = { _ in }
        var canPull: () -> Bool = { true }
    }

    let imageView = UIImageView()
    var handlers = Handlers()
    private var lastBounds: CGSize = .zero
    /// The pull recognizer's delegate: kept apart, as a UIView's own `gestureRecognizerShouldBegin` is asked about
    /// every recognizer on it, the scroll view's pan and pinch too.
    private lazy var pullGate = PullGate(view: self)

    init(image: UIImage) {
        super.init(frame: .zero)
        imageView.image = image
        imageView.contentMode = .scaleAspectFit
        addSubview(imageView)
        showsVerticalScrollIndicator = false
        showsHorizontalScrollIndicator = false
        contentInsetAdjustmentBehavior = .never
        decelerationRate = .fast
        backgroundColor = .clear
        delegate = self
        let double = UITapGestureRecognizer(target: self, action: #selector(doubleTapped(_:)))
        double.numberOfTapsRequired = 2
        let single = UITapGestureRecognizer(target: self, action: #selector(singleTapped))
        single.require(toFail: double)
        let pull = UIPanGestureRecognizer(target: self, action: #selector(pulled(_:)))
        pull.delegate = pullGate
        addGestureRecognizer(double)
        addGestureRecognizer(single)
        addGestureRecognizer(pull)
    }

    required init?(coder: NSCoder) { fatalError("init(coder:) is not supported") }

    var isFitted: Bool { zoomScale <= minimumZoomScale + 0.01 }

    func relayout() {
        lastBounds = .zero
        setNeedsLayout()
    }

    func resetZoom() {
        if !isFitted { setZoomScale(minimumZoomScale, animated: false) }
    }

    override func layoutSubviews() {
        super.layoutSubviews()
        guard bounds.size != lastBounds, let img = imageView.image, img.size.width > 0, img.size.height > 0 else { return }
        lastBounds = bounds.size
        // Fit the image at 1x, allow up to 5x of its pixel size (at least 3x of the fitted size).
        let fit = ZoomMath.fittedSize(image: img.size, in: bounds.size)
        zoomScale = 1
        imageView.transform = .identity
        imageView.frame = CGRect(origin: .zero, size: fit)
        contentSize = fit
        minimumZoomScale = 1
        maximumZoomScale = ZoomMath.maxScale(image: img.size, fitted: fit, screenScale: traitCollection.displayScale) // this view's display (iPhone Duo has two)
        centerImage()
    }

    func centerImage() {
        let inset = ZoomMath.centeringInset(content: imageView.frame.size, bounds: bounds.size)
        contentInset = UIEdgeInsets(top: inset.height, left: inset.width, bottom: inset.height, right: inset.width)
    }

    func viewForZooming(in scrollView: UIScrollView) -> UIView? { imageView }

    func scrollViewDidZoom(_ scrollView: UIScrollView) { centerImage() }

    @objc private func singleTapped() { handlers.tap() }

    @objc private func doubleTapped(_ g: UITapGestureRecognizer) {
        if !isFitted {
            setZoomScale(minimumZoomScale, animated: true)
        } else {
            let p = g.location(in: imageView)
            let scale = min(maximumZoomScale, 2.5)
            let size = CGSize(width: bounds.width / scale, height: bounds.height / scale)
            zoom(to: CGRect(x: p.x - size.width / 2, y: p.y - size.height / 2, width: size.width, height: size.height), animated: true)
        }
        Haptics.tap()
    }

    @objc private func pulled(_ g: UIPanGestureRecognizer) {
        let t = g.translation(in: self)
        let progress = ZoomMath.pullProgress(translation: t.y, height: bounds.height)
        switch g.state {
        case .changed:
            let scale = 1 - 0.3 * progress
            imageView.transform = CGAffineTransform(translationX: t.x, y: t.y).scaledBy(x: scale, y: scale)
            handlers.pull(progress)
        case .ended, .cancelled, .failed:
            if g.state == .ended, ZoomMath.pullCloses(translation: t.y, velocity: g.velocity(in: self).y) {
                handlers.pullEnd(true)
            } else {
                UIView.animate(withDuration: 0.25, delay: 0, usingSpringWithDamping: 0.85, initialSpringVelocity: 0) {
                    self.imageView.transform = .identity
                }
                handlers.pullEnd(false)
            }
        default: break
        }
    }

    /// Only a downward drag at the fitted size, on a page at rest, pulls; sideways goes to the pager, a zoomed picture pans.
    private final class PullGate: NSObject, UIGestureRecognizerDelegate {
        weak var view: ZoomScrollView?
        init(view: ZoomScrollView) { self.view = view }

        func gestureRecognizerShouldBegin(_ g: UIGestureRecognizer) -> Bool {
            guard let pan = g as? UIPanGestureRecognizer, let v = view, v.isFitted, v.handlers.canPull() else { return false }
            let speed = pan.velocity(in: v)
            return speed.y > 0 && speed.y > abs(speed.x) * 1.2
        }
    }
}
