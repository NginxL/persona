import AVFoundation
import CoreMedia
import CoreVideo
import IOSurface
import QuartzCore

nonisolated private struct SampleBox: @unchecked Sendable { let sample: CMSampleBuffer }
nonisolated private struct LayersBox: @unchecked Sendable { let layers: [AVSampleBufferDisplayLayer] }

/// The frames out of the client into the layers that show them: at most 60 a second (a last one after a burst, so
/// nothing is left out), each a pixel buffer shown at once.
///
/// The buffers come from a pool and go back to it once no layer holds them, so a few take turns. Each keeps what has
/// changed on the Mac since it was last filled (by its IOSurface, which a buffer keeps for life), and a frame copies
/// only that from the framebuffer: typing on a 5K screen copies a few kilobytes, not 59 MB (#3067). A buffer the
/// pool has not handed out before is filled whole.
///
/// The timed layers (the small window's source) may show one part of the framebuffer instead (`cut`, #9659: Phone Screen's
/// portrait display): copied the same way into buffers of that part's own size, at the framebuffer's pixels, so the
/// small window takes the part's shape and stays sharp.
nonisolated final class RFBFrameSink: @unchecked Sendable {
    /// The shortest time between two frames: the display's refresh.
    static let interval: CFTimeInterval = 1.0 / 60
    /// The most buffers out of the pool at once: shown, on its way to the layer, kept for a new layer, being filled.
    static let most = 6
    /// How often fps and latency are told (the page redraws as they change): the first frame at once.
    static let reportEvery: CFTimeInterval = 0.25

    private let lock = NSLock()
    private let queue = DispatchQueue(label: "persona.vnc.frames", qos: .userInteractive)
    private weak var client: RFBClient?
    private var layers = NSHashTable<AVSampleBufferDisplayLayer>.weakObjects()
    /// Layers with a timebase of their own (the small window's source, VNCPiP): their samples carry a time on the host
    /// clock instead of DisplayImmediately, which AVFoundation advises against mixing with a timebase.
    private var timed = NSHashTable<AVSampleBufferDisplayLayer>.weakObjects()
    private var dirty = false, scheduled = false
    private var last: CFTimeInterval = 0
    private var shown: [CFTimeInterval] = []
    private var reported: CFTimeInterval = 0
    private var report: (@Sendable (Double, Double?) -> Void)?
    private var latest: CVPixelBuffer?
    /// The part of the framebuffer the timed layers show, and the framebuffer's size it was cut for: on a framebuffer of
    /// another size (the Mac's displays changed, a new connection) they show all of it. Nil: all of it.
    private var timedCut: (rect: CGRect, framebuffer: CGSize)?
    /// The last frame of that part handed to the timed layers; nil while there is none for the cut now.
    private var latestCut: CVPixelBuffer?

    // On `queue` only (copy):
    private var whole = Buffers(), cutBuffers = Buffers()

    var copied: Int { queue.sync { whole.copied + cutBuffers.copied } }
    /// The last frame handed to the layers (tests).
    var latestBuffer: CVPixelBuffer? { lock.withLock { latest } }
    /// The last frame of the cut handed to the timed layers (tests).
    var latestCutBuffer: CVPixelBuffer? { lock.withLock { latestCut } }

    func attach(_ client: RFBClient, report: @escaping @Sendable (Double, Double?) -> Void) {
        lock.withLock {
            self.client = client
            self.report = report
            reported = 0
        }
        client.onFrame = { [weak self] _, _ in self?.changed() }
    }

    func detach() { lock.withLock { client = nil } }

    func add(_ layer: AVSampleBufferDisplayLayer, timed isTimed: Bool = false) {
        let (frame, cutOwed) = lock.withLock { () -> (CVPixelBuffer?, Bool) in
            (isTimed ? timed : layers).add(layer)
            guard isTimed, timedCut != nil else { return (latest, false) }
            return (latestCut ?? latest, latestCut == nil)
        }
        if let frame, let sample = Self.sample(frame, immediate: !isTimed) {
            let box = SampleBox(sample: sample), one = LayersBox(layers: [layer])
            Task { @MainActor in for l in one.layers { Self.enqueue(box.sample, in: l) } }
        }
        // The first timed layer with a cut: its part from the framebuffer as it is, not at the Mac's next change.
        if cutOwed { changed() }
    }

    /// The timed layers show this part of a framebuffer of this size (pixels) from now on; nil: all of it. A change is
    /// copied out at once, from the framebuffer as it is, so the small window takes its shape without waiting for the
    /// Mac to draw.
    func cut(_ rect: CGRect?, of framebuffer: CGSize) {
        let changedCut = lock.withLock { () -> Bool in
            let now = rect.map { (rect: $0.integral, framebuffer: framebuffer) }
            guard now?.rect != timedCut?.rect || now?.framebuffer != timedCut?.framebuffer else { return false }
            timedCut = now
            latestCut = nil
            return true
        }
        if changedCut { changed() }
    }

    func remove(_ layer: AVSampleBufferDisplayLayer) {
        lock.withLock {
            layers.remove(layer)
            timed.remove(layer)
        }
    }

    private func changed() {
        lock.withLock {
            dirty = true
            guard !scheduled else { return }
            scheduled = true
            let wait = max(0, Self.interval - (CACurrentMediaTime() - last))
            queue.asyncAfter(deadline: .now() + wait) { [weak self] in self?.copy() }
        }
    }

    private func copy() {
        guard let client = lock.withLock({ () -> RFBClient? in
            scheduled = false
            dirty = false
            last = CACurrentMediaTime()
            return self.client
        }) else { return }
        // The part the timed layers show, while there are any to show it.
        let wanted = lock.withLock { timed.allObjects.isEmpty ? nil : timedCut }
        var stamp: Double?
        var starved = false
        let buffers: (CVPixelBuffer, CVPixelBuffer?)? = client.withDamage { fb, damage in
            guard fb.width > 1 else { return nil }
            guard let pb = Self.fill(&whole, from: fb, damage: damage, part: CGRect(x: 0, y: 0, width: fb.width, height: fb.height)) else {
                starved = true
                return nil
            }
            var partPB: CVPixelBuffer?
            if let rect = wanted.flatMap({ Self.cutRect($0, on: fb) }) {
                partPB = Self.fill(&cutBuffers, from: fb, damage: damage, part: rect)
                if partPB == nil {
                    starved = true
                    return nil
                }
            } else {
                // Not copied this time: what its buffers lack is not followed, so each is filled whole when it is again.
                cutBuffers.forget()
            }
            stamp = Self.stamp(fb)
            return (pb, partPB)
        }
        if starved {
            // No buffer free (every one still shown or on its way): the damage is kept, try again a frame later.
            lock.withLock { dirty = true }
            queue.asyncAfter(deadline: .now() + Self.interval) { [weak self] in self?.retry() }
            return
        }
        guard let buffers else { return }
        let (buffer, cutBuffer) = buffers
        guard let sample = Self.sample(buffer, immediate: true) else { return }
        let now = CACurrentMediaTime()
        let (targets, timedTargets, report, fps) = lock.withLock {
            () -> ([AVSampleBufferDisplayLayer], [AVSampleBufferDisplayLayer], (@Sendable (Double, Double?) -> Void)?, Double) in
            latest = buffer
            // Only a part cut for the cut as it is now (it may have changed while this one was copied).
            latestCut = cutBuffer != nil && timedCut?.rect == wanted?.rect && timedCut?.framebuffer == wanted?.framebuffer ? cutBuffer : nil
            shown.append(now)
            shown.removeAll { now - $0 > 1 }
            let due = now - reported >= Self.reportEvery
            if due { reported = now }
            return (layers.allObjects, timed.allObjects, due ? self.report : nil, Double(shown.count))
        }
        let latency = stamp.map { Date().timeIntervalSince1970 * 1000 - $0 }
        report?(fps, latency.flatMap { $0 >= 0 && $0 < 60_000 ? $0 : nil })
        let box = SampleBox(sample: sample), onScreen = LayersBox(layers: targets)
        Task { @MainActor in for l in onScreen.layers { Self.enqueue(box.sample, in: l) } }
        if !timedTargets.isEmpty, let atTime = Self.sample(cutBuffer ?? buffer, immediate: false) {
            let box = SampleBox(sample: atTime), small = LayersBox(layers: timedTargets)
            Task { @MainActor in for l in small.layers { Self.enqueue(box.sample, in: l) } }
        }
        if lock.withLock({ dirty && !scheduled }) { changed() }
    }

    /// The cut on this framebuffer, in whole pixels: nil when it was cut for a framebuffer of another size or does not
    /// lie on this one.
    static func cutRect(_ cut: (rect: CGRect, framebuffer: CGSize), on fb: RFBFramebuffer) -> CGRect? {
        let whole = CGRect(x: 0, y: 0, width: fb.width, height: fb.height)
        let r = cut.rect.integral
        guard Int(cut.framebuffer.width) == fb.width, Int(cut.framebuffer.height) == fb.height,
              r.width >= 2, r.height >= 2, whole.contains(r) else { return nil }
        return r
    }

    private func retry() {
        if lock.withLock({ dirty && !scheduled }) { changed() }
    }

    /// Buffers of one size from a pool, for one part of the framebuffer (all of it, or the cut), and what each handed
    /// out so far lacks of that part now, by its IOSurface's id, with the frame it was last filled for (the oldest are
    /// forgotten: the pool let them go).
    private struct Buffers {
        var pool: CVPixelBufferPool?
        var part = CGRect.null
        var stale: [IOSurfaceID: (region: RFBRegion, frame: Int)] = [:]
        var frames = 0
        /// Bytes copied into them so far (tests).
        var copied = 0

        /// What every buffer lacks is no longer known: each is filled whole when it is next handed out.
        mutating func forget() { stale = [:] }
    }

    /// A buffer from the pool brought up to `part` of the framebuffer: every buffer's stale part grows by damage, and
    /// the one handed out gets its stale part copied in. Nil when the pool has none to give (all still held).
    private static func fill(_ b: inout Buffers, from fb: RFBFramebuffer, damage: RFBRegion, part: CGRect) -> CVPixelBuffer? {
        let w = Int(part.width), h = Int(part.height), ox = Int(part.minX), oy = Int(part.minY)
        let own = CGRect(x: 0, y: 0, width: w, height: h)
        if b.pool == nil || b.part != part {
            let attrs: [String: Any] = [kCVPixelBufferPixelFormatTypeKey as String: kCVPixelFormatType_32BGRA,
                                        kCVPixelBufferWidthKey as String: w, kCVPixelBufferHeightKey as String: h,
                                        kCVPixelBufferIOSurfacePropertiesKey as String: [:]]
            if b.pool == nil || b.part.size != part.size {
                b.pool = nil
                CVPixelBufferPoolCreate(nil, nil, attrs as CFDictionary, &b.pool)
            }
            b.part = part
            b.stale = [:]
        }
        // The damage on this part, in the part's own pixels.
        let local = ox == 0 && oy == 0 && w == fb.width && h == fb.height ? damage : damage.clipped(to: part).offsetBy(dx: -part.minX, dy: -part.minY)
        for id in b.stale.keys { b.stale[id]?.region.add(local) }
        var pb: CVPixelBuffer?
        let aux = [kCVPixelBufferPoolAllocationThresholdKey as String: Self.most] as CFDictionary
        guard let pool = b.pool, CVPixelBufferPoolCreatePixelBufferWithAuxAttributes(nil, pool, aux, &pb) == kCVReturnSuccess, let pb,
              let surface = CVPixelBufferGetIOSurface(pb)?.takeUnretainedValue() else { return nil }
        let id = IOSurfaceGetID(surface)
        b.frames += 1
        let region = (b.stale[id]?.region ?? RFBRegion(own)).clipped(to: own)
        b.stale[id] = (RFBRegion(), b.frames)
        if b.stale.count > Self.most * 2, let oldest = b.stale.min(by: { $0.value.frame < $1.value.frame })?.key {
            b.stale[oldest] = nil
        }
        CVPixelBufferLockBaseAddress(pb, [])
        if let dst = CVPixelBufferGetBaseAddress(pb) {
            let row = CVPixelBufferGetBytesPerRow(pb), src = fb.bytesPerRow
            if region.rects.count == 1, region.rects[0] == own, row == src, ox == 0, oy == 0, w == fb.width {
                memcpy(dst, fb.pixels, src * h)
            } else {
                for r in region.rects {
                    let x = Int(r.minX) * 4, n = Int(r.width) * 4
                    for y in Int(r.minY)..<Int(r.maxY) { memcpy(dst + y * row + x, fb.pixels + (y + oy) * src + ox * 4 + x, n) }
                }
            }
            b.copied += region.area * 4
        }
        CVPixelBufferUnlockBaseAddress(pb, [])
        return pb
    }

    /// The mock's Screen Sharing stamps when it sent a frame (MOCK_SCREEN_ANIMATE) in its first two pixels: a marker
    /// (pure magenta) then the low 24 bits of its clock in milliseconds; nil on any other screen.
    private static func stamp(_ fb: RFBFramebuffer) -> Double? {
        let m = fb.pixel(0, 0)
        guard m[0] == 255, m[1] == 0, m[2] == 255 else { return nil }
        let t = fb.pixel(1, 0)
        let low = Double(Int(t[2]) << 16 | Int(t[1]) << 8 | Int(t[0]))
        let now = Date().timeIntervalSince1970 * 1000
        let base = now - now.truncatingRemainder(dividingBy: 16_777_216)
        let sent = base + low
        return sent > now ? sent - 16_777_216 : sent
    }

    /// A sample of the frame at this moment on the host clock (rising from one to the next); `immediate`: shown at once
    /// whatever the layer's time (the page's), else when its timebase reaches it (the small window's, at once too).
    static func sample(_ pb: CVPixelBuffer, immediate: Bool) -> CMSampleBuffer? {
        var format: CMVideoFormatDescription?
        guard CMVideoFormatDescriptionCreateForImageBuffer(allocator: nil, imageBuffer: pb, formatDescriptionOut: &format) == noErr,
              let format else { return nil }
        var timing = CMSampleTimingInfo(duration: CMTime(value: 1, timescale: 60), presentationTimeStamp: CMClockGetTime(CMClockGetHostTimeClock()),
                                        decodeTimeStamp: .invalid)
        var sample: CMSampleBuffer?
        guard CMSampleBufferCreateReadyWithImageBuffer(allocator: nil, imageBuffer: pb, formatDescription: format,
                                                       sampleTiming: &timing, sampleBufferOut: &sample) == noErr, let sample else { return nil }
        if immediate, let attachments = CMSampleBufferGetSampleAttachmentsArray(sample, createIfNecessary: true),
           CFArrayGetCount(attachments) > 0 {
            let dict = unsafeBitCast(CFArrayGetValueAtIndex(attachments, 0), to: CFMutableDictionary.self)
            CFDictionarySetValue(dict, Unmanaged.passUnretained(kCMSampleAttachmentKey_DisplayImmediately).toOpaque(),
                                 Unmanaged.passUnretained(kCFBooleanTrue).toOpaque())
        }
        return sample
    }

    /// A picture of the app's own into a timed layer (the small window's card, VNCPiPCard), at this moment on the host
    /// clock: the client's next frame, later, replaces it.
    @MainActor static func show(_ pb: CVPixelBuffer, in layer: AVSampleBufferDisplayLayer) {
        if let sample = sample(pb, immediate: false) { enqueue(sample, in: layer) }
    }

    /// Into the layer's renderer, flushed first when it failed (a picture of another format, the app suspended).
    @MainActor static func enqueue(_ sample: CMSampleBuffer, in layer: AVSampleBufferDisplayLayer) {
        let r = layer.sampleBufferRenderer
        if r.status == .failed { r.flush() }
        r.enqueue(sample)
    }
}
