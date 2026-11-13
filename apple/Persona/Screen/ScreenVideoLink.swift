import Foundation

/// The cap the phone puts on the video channel (#3954, docs/extensions/computer-use.md "Adaptation"): the helper's
/// bandwidth adaptation works under it. 12 Mbit/s on Wi-Fi, 4 on cellular, 1.5 in Low Data Mode; 15 frames a second in
/// Low Power Mode or a serious thermal state, else 60.
nonisolated struct ScreenVideoBudget: Equatable, Sendable {
    var maxKbps: Int
    var fps: Int

    static func of(expensive: Bool, constrained: Bool, lowPower: Bool, thermal: ProcessInfo.ThermalState) -> ScreenVideoBudget {
        let kbps = constrained ? 1_500 : expensive ? 4_000 : 12_000
        let hot = thermal == .serious || thermal == .critical
        return ScreenVideoBudget(maxKbps: kbps, fps: lowPower || hot ? 15 : 60)
    }
}

/// How long the page waits before trying video again after it stopped (#3954): 30 s, then twice as long each time up
/// to 5 min; back to 30 s once video has stayed up a while (`ScreenVideoLink`'s `stable`), so a connection that keeps
/// dropping is tried less and less often instead of flipping the picture (and its line) back and forth.
nonisolated struct ScreenVideoBackoff: Equatable, Sendable {
    let first: Duration
    let longest: Duration
    private(set) var next: Duration

    init(first: Duration = .seconds(30), longest: Duration = .seconds(300)) {
        self.first = first
        self.longest = longest
        next = first
    }

    /// The wait before this retry; the one after is twice as long.
    mutating func take() -> Duration {
        defer { next = min(next * 2, longest) }
        return next
    }

    mutating func reset() { next = first }
}

/// Why the page shows VNC while video was chosen (#3954), in words anyone reads: no protocol or codec names.
nonisolated enum ScreenVideoFallback: Equatable, Sendable {
    /// The stream closed, went quiet, or a picture didn't decode; video is tried again later.
    case lost
    /// The Mac is locked or at its login window; video comes back when it is unlocked.
    case locked
    /// The Mac hasn't allowed Persona Screen to record its screen.
    case notAllowed

    /// The helper's `paused` reason as a fallback.
    static func paused(_ reason: String) -> ScreenVideoFallback {
        switch reason {
        case "locked", "login_window": .locked
        case "permission": .notAllowed
        default: .lost
        }
    }

    var message: String {
        switch self {
        case .lost: String(localized: "Video stopped. Showing VNC for now.")
        case .locked: String(localized: "The Mac is locked. Showing VNC until it’s unlocked.")
        case .notAllowed: String(localized: "This Mac hasn’t allowed video yet. Showing VNC.")
        }
    }

    /// The one thing the user can do about `notAllowed` themselves, in one sentence (the page's "How to turn it on").
    static var howToAllow: String {
        String(localized: "On that Mac, turn on Persona Screen in System Settings › Privacy & Security › Screen & System Audio Recording.")
    }
}

#if os(iOS) && canImport(PersonaScreenKit)
import AVFoundation
import CoreVideo
import Network
import Observation
import PersonaScreenKit
import UIKit

/// The video channel of one Mac's live screen (#3954): when it runs, what it shows, and when the page falls back to
/// VNC. RFB signs in first and shows until the first video picture decodes; then the picture is video and the session
/// stops asking RFB for updates (input, the clipboard and the fallback stay on RFB). Any of: the stream closes, no frame
/// or heartbeat for 3 s, a picture that doesn't decode, or the Mac locking (`paused`) puts RFB back at once; a stream
/// that ended is tried again after `ScreenVideoBackoff`, and a paused one comes back with the next picture after
/// `resumed`. Owned by the RFBSession, so the page and the small window share it.
@MainActor @Observable final class ScreenVideoLink {
    /// What the page wants shown as video: one display (`display`, the helper's CGDirectDisplayID; nil the main one) at
    /// `rect` on the framebuffer (pixels; nil all of it).
    struct Want: Equatable {
        var display: Int?
        var rect: CGRect?
    }

    /// The signed-in RFB connection video binds to.
    struct Ready: Equatable {
        var group: String
        var machine: String
        var pair: String
    }

    /// Video is the picture: a decoded frame is shown and RFB asks for no updates.
    private(set) var showing = false
    /// Why VNC shows while video was chosen; nil when video shows, is on its way, or isn't wanted.
    private(set) var fallback: ScreenVideoFallback?
    /// Where the video goes on the framebuffer, in its pixels; nil: all of it.
    private(set) var rect: CGRect?
    /// The stream as the stats overlay shows it.
    private(set) var fps: Double = 0
    private(set) var decodeMs: Double?
    private(set) var codec: String?
    private(set) var size: CGSize?
    /// When video is tried again after it stopped (tests).
    @ObservationIgnored private(set) var retryAt: Date?

    /// Showing (true) or not: the session holds or releases RFB's updates.
    @ObservationIgnored var onShowing: @MainActor (Bool) -> Void = { _ in }
    /// Opens the stream's transport; tests give a scripted one.
    @ObservationIgnored var transport: @MainActor (Ready) -> RFBTransport? = ScreenVideoLink.hubTransport
    /// Each change of `fallback`, and each wait before a retry (tests).
    @ObservationIgnored var onFallback: @MainActor (ScreenVideoFallback?) -> Void = { _ in }
    @ObservationIgnored var onRetry: @MainActor (Duration) -> Void = { _ in }

    @ObservationIgnored private var want: Want?
    @ObservationIgnored private var ready: Ready?
    @ObservationIgnored private var held: Set<String> = []
    @ObservationIgnored private var stream: ScreenVideoStream?
    @ObservationIgnored private var opened: (Want, Ready)?
    @ObservationIgnored private var backoff: ScreenVideoBackoff
    /// How long video must stay up before a drop starts the waits from the first again.
    @ObservationIgnored private let stable: Duration
    @ObservationIgnored private var shownSince: ContinuousClock.Instant?
    @ObservationIgnored private var retry: Task<Void, Never>?
    @ObservationIgnored private let layers = NSHashTable<AVSampleBufferDisplayLayer>.weakObjects()
    @ObservationIgnored private let mailbox = ScreenVideoMailbox()
    @ObservationIgnored private var latest: CVPixelBuffer?
    @ObservationIgnored private var shownAt: [Date] = []
    @ObservationIgnored private let path = NWPathMonitor()
    @ObservationIgnored private var budget: ScreenVideoBudget

    init(backoff: ScreenVideoBackoff = .init(), stable: Duration = .seconds(60)) {
        self.backoff = backoff
        self.stable = stable
        budget = Self.currentBudget(path: nil)
        path.pathUpdateHandler = { @Sendable [weak self] p in
            let expensive = p.isExpensive, constrained = p.isConstrained
            Task { @MainActor in self?.network(expensive: expensive, constrained: constrained) }
        }
        path.start(queue: .global(qos: .utility))
    }

    deinit { path.cancel() }

    // MARK: Inputs

    /// What the page wants shown as video; nil: VNC (chosen, or several displays shown at once).
    func want(_ w: Want?) {
        guard w != want else { return }
        want = w
        if w == nil { say(nil); stopRetry(); backoff.reset() }
        reconsider()
    }

    /// The signed-in RFB connection (its pair), or nil when there is none: a new connection tries video at once. The
    /// fallback said stays (the page shows it only while VNC is live), so a reconnection doesn't blink it off and on.
    func rfb(_ r: Ready?) {
        guard r != ready else { return }
        ready = r
        stopRetry()
        reconsider()
    }

    /// Video stops while any reason holds it (the small window shows the screen, the app is in the background) and
    /// comes back when none does, with no fallback said.
    func hold(_ reason: String, _ on: Bool) {
        let was = held.isEmpty
        if on { held.insert(reason) } else { held.remove(reason) }
        if was != held.isEmpty { reconsider() }
    }

    /// Shown in this layer from now on; the newest picture at once.
    func show(in layer: AVSampleBufferDisplayLayer) {
        layers.add(layer)
        if showing, let latest { Self.enqueue(latest, in: [layer]) }
    }

    func hide(from layer: AVSampleBufferDisplayLayer) { layers.remove(layer) }

    // MARK: Running

    private func reconsider() {
        let should: (Want, Ready)? = {
            guard let want, let ready, held.isEmpty, retry == nil else { return nil }
            return (want, ready)
        }()
        if let opened, let should, opened == should { return }
        if stream != nil { stop() }
        if let should { open(should) }
    }

    private func open(_ target: (Want, Ready)) {
        guard let t = transport(target.1) else {
            ended(.closed)
            return
        }
        opened = target
        rect = target.0.rect
        let start = StartMessage(display: target.0.display, maxSize: Self.maxSize, fps: budget.fps, maxKbps: budget.maxKbps,
                                 codecs: ["hevc", "h264"])
        let mailbox = mailbox
        let s = ScreenVideoStream(transport: t, start: start) { [weak self] event in
            switch event {
            case let .picture(pb, ms):
                // Newest picture wins: one hop to the main actor at a time, carrying whatever is newest then.
                if mailbox.put(pb, ms) { Task { @MainActor in self?.takePicture() } }
            case let .hello(h):
                Task { @MainActor in self?.hello(h) }
            case let .paused(why):
                Task { @MainActor in self?.paused(why) }
            case .resumed:
                break // the next picture shows video again
            case let .ended(why):
                Task { @MainActor in self?.ended(why) }
            }
        }
        stream = s
        s.run()
    }

    /// Closes the stream from this side, with no fallback said.
    private func stop() {
        stream?.close()
        stream = nil
        opened = nil
        _ = mailbox.take()
        show(false)
    }

    private func hello(_ h: HelloMessage) {
        codec = h.codec == "h264" ? "H.264" : "HEVC"
        size = CGSize(width: h.width, height: h.height)
        if let r = h.rect, r.count == 4 { rect = CGRect(x: r[0], y: r[1], width: r[2], height: r[3]) }
    }

    private func takePicture() {
        guard stream != nil, let (pb, ms) = mailbox.take() else { return }
        latest = pb
        decodeMs = ms
        let now = Date.now
        shownAt.append(now)
        shownAt.removeAll { now.timeIntervalSince($0) > 1 }
        fps = Double(shownAt.count)
        if !showing {
            say(nil) // video is back: the line goes
            show(true)
        }
        Self.enqueue(pb, in: layers.allObjects)
    }

    private func paused(_ why: String) {
        guard stream != nil else { return }
        show(false)
        say(.paused(why))
    }

    private func ended(_ why: ScreenVideoStream.Ending) {
        stream = nil
        opened = nil
        show(false)
        guard want != nil, ready != nil else { return }
        say(.lost)
        let wait = backoff.take()
        retryAt = Date.now.addingTimeInterval(Double(wait.components.seconds) + Double(wait.components.attoseconds) / 1e18)
        onRetry(wait)
        retry = Task { [weak self] in
            try? await Task.sleep(for: wait)
            guard !Task.isCancelled, let self else { return }
            self.retry = nil
            self.retryAt = nil
            self.reconsider()
        }
    }

    private func stopRetry() {
        retry?.cancel()
        retry = nil
        retryAt = nil
    }

    /// Changes the line only when the reason changes: the same one again (a retry that fails as the last one did) is
    /// no change, so nothing redraws.
    private func say(_ f: ScreenVideoFallback?) {
        guard f != fallback else { return }
        fallback = f
        onFallback(f)
    }

    private func show(_ on: Bool) {
        guard showing != on else { return }
        showing = on
        if on {
            shownSince = .now
        } else {
            // Video that stayed up a while starts the waits over; one that dropped soon after coming back keeps doubling.
            if let since = shownSince, ContinuousClock.now - since >= stable { backoff.reset() }
            shownSince = nil
            fps = 0
            decodeMs = nil
            latest = nil
            // Emptied, so when video shows again it never starts on an old picture.
            for l in layers.allObjects { l.sampleBufferRenderer.flush(removingDisplayedImage: true, completionHandler: nil) }
        }
        onShowing(on)
    }

    private func network(expensive: Bool, constrained: Bool) {
        let b = Self.currentBudget(path: (expensive, constrained))
        guard b != budget else { return }
        budget = b
        stream?.send(UpdateMessage(fps: b.fps, maxKbps: b.maxKbps))
    }

    private static func currentBudget(path: (expensive: Bool, constrained: Bool)?) -> ScreenVideoBudget {
        let p = ProcessInfo.processInfo
        return ScreenVideoBudget.of(expensive: path?.expensive ?? false, constrained: path?.constrained ?? false,
                                    lowPower: p.isLowPowerModeEnabled, thermal: p.thermalState)
    }

    /// The picture's long edge at most this many pixels: the phone's screen, and never under 1080p's.
    static var maxSize: Int {
        let scene = UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first
        let b = scene?.screen.nativeBounds ?? .zero
        return max(1920, Int(max(b.width, b.height)))
    }

    private static func enqueue(_ pb: CVPixelBuffer, in layers: [AVSampleBufferDisplayLayer]) {
        guard !layers.isEmpty, let sample = RFBFrameSink.sample(pb, immediate: true) else { return }
        for l in layers { RFBFrameSink.enqueue(sample, in: l) }
    }

    /// The hub's relay of the video channel (`…/screen/video/connect?pair=…`), the way RFB's goes.
    static func hubTransport(_ r: Ready) -> RFBTransport? {
        guard let api = AppModel.shared.api(r.group) else { return nil }
        return RFBWebSocket(api: api, path: "/api/v1/machines/\(r.machine)/screen/video/connect", pair: r.pair)
    }
}

/// The newest decoded picture on its way to the main actor: `put` says whether a hop is needed (none is on its way).
nonisolated final class ScreenVideoMailbox: @unchecked Sendable {
    private let lock = NSLock()
    private var picture: (CVPixelBuffer, Double)?

    func put(_ pb: CVPixelBuffer, _ ms: Double) -> Bool {
        lock.withLock {
            defer { picture = (pb, ms) }
            return picture == nil
        }
    }

    func take() -> (CVPixelBuffer, Double)? {
        lock.withLock {
            defer { picture = nil }
            return picture
        }
    }
}
#endif
