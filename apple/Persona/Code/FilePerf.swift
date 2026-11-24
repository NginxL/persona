import Foundation
import QuartzCore

/// How long opening a file takes and how smoothly it scrolls (docs/apple/ios.md「File viewer」's measurements). DEBUG
/// builds with `-showFilePerf YES` append lines to Caches/file-perf.log:
/// `big.json fetched 210 ms (2.0 MB)`, `… shown 260 ms`, `… colored 900 ms`, and while a file is open, a line a second
/// with the frames drawn and the longest frame (`frames 59 · longest 21 ms`). Otherwise nothing.
@MainActor final class FilePerf {
    private let name: String
    private let start = CACurrentMediaTime()
    private var marked: Set<String> = []

    init(_ path: String) { name = (path as NSString).lastPathComponent }

    func mark(_ what: String, bytes: Int) {
        guard Self.on, !marked.contains(what) else { return }
        marked.insert(what)
        let ms = Int((CACurrentMediaTime() - start) * 1000)
        Self.write("\(name) \(what) \(ms) ms" + (bytes > 0 ? " (\(Int64(bytes).storageText))" : ""))
    }

    static var on: Bool {
        #if DEBUG
        UserDefaults.standard.bool(forKey: "showFilePerf")
        #else
        false
        #endif
    }

    static let url = URL.cachesDirectory.appending(path: "file-perf.log")

    static func write(_ line: String) {
        let data = Data((line + "\n").utf8)
        if let h = try? FileHandle(forWritingTo: url) {
            h.seekToEndOfFile()
            h.write(data)
            try? h.close()
        } else {
            try? data.write(to: url)
        }
    }
}

/// Counts frames while a file is open (`-showFilePerf YES`): each second, how many were drawn and the longest gap
/// between two. A main thread busy laying out or coloring shows as a long frame.
@MainActor final class FrameMeter: NSObject {
    static let shared = FrameMeter()
    private var link: CADisplayLink?
    private var last: CFTimeInterval = 0
    private var bucketStart: CFTimeInterval = 0
    private var frames = 0
    private var longest: CFTimeInterval = 0
    private var label = ""
    /// Something scrolled in this second: those are the seconds that tell how smooth scrolling is.
    private var scrolled = false

    /// A file's view moved (CodeLinesView, the Markdown scroll view).
    func noteScroll() {
        if link != nil { scrolled = true }
    }

    func start(_ label: String) {
        guard FilePerf.on, link == nil else { return }
        self.label = label
        #if os(iOS) || os(visionOS)
        let l = CADisplayLink(target: self, selector: #selector(tick(_:)))
        #else
        // A Mac's display link comes from the screen it draws on.
        guard let l = NSScreen.main?.displayLink(target: self, selector: #selector(tick(_:))) else { return }
        #endif
        l.preferredFrameRateRange = CAFrameRateRange(minimum: 60, maximum: 120, preferred: 120)
        l.add(to: .main, forMode: .common)
        link = l
        last = 0
    }

    func stop() {
        link?.invalidate()
        link = nil
    }

    @objc private func tick(_ l: CADisplayLink) {
        let now = l.timestamp
        if last == 0 {
            last = now
            bucketStart = now
            return
        }
        longest = max(longest, now - last)
        frames += 1
        last = now
        if now - bucketStart >= 1 {
            FilePerf.write("\(label) frames \(frames) · longest \(Int(longest * 1000)) ms" + (scrolled ? " · scrolling" : ""))
            frames = 0
            longest = 0
            scrolled = false
            bucketStart = now
        }
    }
}

/// Scrolls a file's view at a steady speed for the measurements (`-showFileAutoScroll YES` with `-showFilePerf YES`):
/// 4 000 pt/s (a fling; `-showFileAutoScrollSpeed 1500` reads) down for 4 s, then up for 2 s. Measured this way and not with XCUITest drags:
/// before each gesture XCUITest takes an accessibility snapshot on the app's main thread, a 40–100 ms frame of its own.
@MainActor final class AutoScroller: NSObject {
    private var link: CADisplayLink?
    private var started: CFTimeInterval = 0
    private var last: CFTimeInterval = 0
    private let move: (CGFloat) -> Void

    static var on: Bool {
        #if DEBUG
        FilePerf.on && UserDefaults.standard.bool(forKey: "showFileAutoScroll")
        #else
        false
        #endif
    }

    /// `move` is given the points to scroll by this frame (negative: up).
    init(move: @escaping (CGFloat) -> Void) {
        self.move = move
    }

    func start(after delay: TimeInterval = 2) {
        guard Self.on, link == nil else { return }
        DispatchQueue.main.asyncAfter(deadline: .now() + delay) { [self] in
            #if os(iOS) || os(visionOS)
            let l = CADisplayLink(target: self, selector: #selector(tick(_:)))
            #else
            // A Mac's display link comes from the screen it draws on.
            guard let l = NSScreen.main?.displayLink(target: self, selector: #selector(tick(_:))) else { return }
            #endif
            l.add(to: .main, forMode: .common)
            link = l
        }
    }

    @objc private func tick(_ l: CADisplayLink) {
        if started == 0 {
            started = l.timestamp
            last = l.timestamp
            return
        }
        let t = l.timestamp - started, dt = l.timestamp - last
        last = l.timestamp
        guard t < 6 else {
            l.invalidate()
            link = nil
            FilePerf.write("autoscroll done")
            return
        }
        // The switch's name only in DEBUG: release.sh refuses a Release binary that carries it.
        #if DEBUG
        let speed = UserDefaults.standard.double(forKey: "showFileAutoScrollSpeed")
        #else
        let speed = 0.0
        #endif
        let v = speed > 0 ? speed : 4000
        move(CGFloat((t < 4 ? v : -v) * dt))
    }
}
