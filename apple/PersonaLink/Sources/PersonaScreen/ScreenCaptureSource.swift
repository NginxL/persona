#if canImport(ScreenCaptureKit)
import CoreGraphics
import CoreMedia
import CoreVideo
import Foundation
import PersonaScreenKit
@preconcurrency import ScreenCaptureKit // SCShareableContent and SCStream predate Sendable; used only on `queue`
import VideoToolbox

/// The real capture source for the live-screen video channel (#3954): ScreenCaptureKit captures one display and
/// VideoToolbox encodes it with the low-latency settings gate ① measured on a real Mac (≈5 ms p50 / ≈10 ms p95 from a
/// screen change to an encoded frame at 1080p60): `RealTime`, low-latency rate control, no frame reordering,
/// `MaxFrameDelayCount` 0, and a capture queue depth of 3.
///
/// It answers `start` with `hello`, then `format` (the parameter sets) and a keyframe, then a `frame` for each screen
/// change, and a `heartbeat` each second nothing changed. It pauses (`paused`) when it has no Screen Recording
/// permission, when the Mac is locked or at the login window, when the display is gone, or when the encoder can't be
/// made, and on unlock it sends `resumed` and starts again with a keyframe. Receiver reports drive the bitrate through
/// `RateController`.
///
/// Everything it holds is touched only on `queue`: ScreenCaptureKit delivers frames there, and VideoToolbox's output
/// and every incoming message hop onto it. That confinement is why it is `@unchecked Sendable`. `generation` counts
/// (re)starts, so a completion from a capture that has since been replaced does nothing.
final class ScreenCaptureSource: NSObject, ScreenSource, SCStreamOutput, SCStreamDelegate, @unchecked Sendable {
    var onMessage: ((ScreenVideoMessage) -> Void)?

    private let queue = DispatchQueue(label: "persona-screen.capture", qos: .userInteractive)
    /// The phone's request, with its updates applied; nil before `start` and after `stop`.
    private var request: StartMessage?
    private var stream: SCStream?
    private var config: SCStreamConfiguration?
    private var encoder: VTCompressionSession?
    private var codec = "h264"
    private var rate = RateController(cap: 0)
    private var generation = 0
    /// The last frame's number; frames count from 1 for the whole helper run.
    private var number: UInt32 = 0
    /// When the last frame or heartbeat went out, µs on the monotonic clock.
    private var lastSent: UInt64 = 0
    private var parameterSets: [Data] = []
    private var wantKeyframe = true
    /// Why capture is stopped, as sent in `paused`; nil while it runs (or before it starts).
    private var pausedReason: String?
    /// The phone asked to pause (`update.paused`): no capture, and no `paused` message, since it knows.
    private var userPaused = false
    private var timer: DispatchSourceTimer?

    // MARK: ScreenSource

    func start(_ start: StartMessage) {
        queue.async { [self] in
            request = start
            rate = RateController(cap: start.maxKbps)
            userPaused = false
            pausedReason = nil
            startTimer()
            begin()
        }
    }

    func update(_ update: UpdateMessage) {
        queue.async { [self] in
            guard var r = request else { return }
            var restart = false
            if let d = update.display, d != r.display { r.display = d; restart = true }
            if let m = update.maxSize, m != r.maxSize { r.maxSize = m; restart = true }
            if let c = update.codecs, c != r.codecs {
                r.codecs = c
                restart = restart || CapturePlan.codec(offered: c) != codec
            }
            let fpsChanged = update.fps.map { $0 != r.fps } ?? false
            if let f = update.fps { r.fps = f }
            if let k = update.maxKbps {
                r.maxKbps = k
                if let lowered = rate.setCap(k) { setBitrate(lowered) }
            }
            request = r

            if update.paused == true {
                if !userPaused {
                    userPaused = true
                    stopCapture()
                }
                return
            }
            if update.paused == false, userPaused {
                userPaused = false
                begin()
                return
            }
            guard !userPaused else { return }
            if restart {
                begin()
            } else if fpsChanged {
                setFPS(CapturePlan.fps(r.fps))
            }
        }
    }

    func requestKeyframe() {
        queue.async { [self] in wantKeyframe = true }
    }

    func receive(report: ReceiverReport) {
        queue.async { [self] in
            let decision = rate.receive(report, now: Self.nowMicros)
            if let kbps = decision.kbps { setBitrate(kbps) }
            if decision.keyframe { wantKeyframe = true }
        }
    }

    func stop() {
        queue.sync {
            timer?.cancel()
            timer = nil
            stopCapture()
            request = nil
        }
    }

    // MARK: Capture

    /// (Re)opens capture for `request`: a fresh encoder, then `hello`, then frames. Pauses instead when it can't.
    private func begin() {
        stopCapture()
        guard request != nil else { return }
        if let reason = Self.consoleReason() { return pause(reason) }
        guard CGPreflightScreenCaptureAccess() else { return pause("permission") }
        let gen = generation
        Task {
            do {
                let content = try await SCShareableContent.excludingDesktopWindows(false, onScreenWindowsOnly: true)
                queue.async { [self] in
                    guard gen == generation, let request else { return }
                    open(content, for: request)
                }
            } catch {
                queue.async { [self] in
                    guard gen == generation else { return }
                    warn("shareable content: \(error)")
                    pause(CGPreflightScreenCaptureAccess() ? "busy" : "permission")
                }
            }
        }
    }

    private func open(_ content: SCShareableContent, for request: StartMessage) {
        let wanted = request.display.map { CGDirectDisplayID(truncatingIfNeeded: $0) } ?? CGMainDisplayID()
        guard let display = content.displays.first(where: { $0.displayID == wanted }) else { return pause("display_gone") }
        let pixels = Self.pixelSize(of: display)
        let size = CapturePlan.size(width: pixels.width, height: pixels.height, maxSize: request.maxSize)
        let fps = CapturePlan.fps(request.fps)

        var codec = CapturePlan.codec(offered: request.codecs)
        var encoder = makeEncoder(codec: codec, width: size.width, height: size.height, fps: fps)
        if encoder == nil, codec == "hevc" {
            codec = "h264" // no HEVC encoder on this Mac: H.264 High, which the phone decodes too
            encoder = makeEncoder(codec: codec, width: size.width, height: size.height, fps: fps)
        }
        guard let encoder else { return pause("busy") }

        let config = SCStreamConfiguration()
        config.width = size.width
        config.height = size.height
        config.minimumFrameInterval = CMTime(value: 1, timescale: CMTimeScale(fps))
        config.pixelFormat = kCVPixelFormatType_420YpCbCr8BiPlanarVideoRange // what the encoder takes, no conversion
        config.queueDepth = 3 // gate ①: 1 starves ScreenCaptureKit, more only adds latency
        config.showsCursor = true
        config.capturesAudio = false

        let stream = SCStream(filter: SCContentFilter(display: display, excludingWindows: []), configuration: config,
                              delegate: self)
        do {
            try stream.addStreamOutput(self, type: .screen, sampleHandlerQueue: queue)
        } catch {
            VTCompressionSessionInvalidate(encoder)
            warn("stream output: \(error)")
            return pause("busy")
        }
        self.stream = stream
        self.config = config
        self.encoder = encoder
        self.codec = codec
        parameterSets = []
        wantKeyframe = true
        pausedReason = nil

        send(.hello(HelloMessage(version: ScreenVideoWire.protocolVersion, codec: codec,
                                 display: Int(display.displayID), width: size.width, height: size.height)))
        lastSent = Self.nowMicros
        let gen = generation
        stream.startCapture { [weak self] error in
            guard let self, let error else { return }
            self.queue.async {
                guard gen == self.generation else { return }
                self.warn("start capture: \(error)")
                self.pause(CGPreflightScreenCaptureAccess() ? "busy" : "permission")
            }
        }
    }

    /// Stops the stream and the encoder, if any. Bumps `generation`, so their late completions do nothing.
    private func stopCapture() {
        generation += 1
        if let stream {
            self.stream = nil
            stream.stopCapture { _ in }
        }
        config = nil
        if let encoder {
            self.encoder = nil
            VTCompressionSessionInvalidate(encoder)
        }
    }

    /// Stops capture and tells the phone why, once per reason.
    private func pause(_ reason: String) {
        stopCapture()
        guard pausedReason != reason else { return }
        pausedReason = reason
        send(.paused(PausedMessage(reason: reason)))
    }

    private func makeEncoder(codec: String, width: Int, height: Int, fps: Int) -> VTCompressionSession? {
        let type = codec == "hevc" ? kCMVideoCodecType_HEVC : kCMVideoCodecType_H264
        let spec = [kVTVideoEncoderSpecification_EnableLowLatencyRateControl: kCFBooleanTrue] as CFDictionary
        var made: VTCompressionSession?
        let status = VTCompressionSessionCreate(allocator: nil, width: Int32(width), height: Int32(height), codecType: type,
                                                encoderSpecification: spec, imageBufferAttributes: nil,
                                                compressedDataAllocator: nil, outputCallback: nil, refcon: nil,
                                                compressionSessionOut: &made)
        guard status == noErr, let session = made else {
            warn("\(codec) encoder: \(status)")
            return nil
        }
        func set(_ key: CFString, _ value: CFTypeRef) { VTSessionSetProperty(session, key: key, value: value) }
        set(kVTCompressionPropertyKey_RealTime, kCFBooleanTrue)
        set(kVTCompressionPropertyKey_AllowFrameReordering, kCFBooleanFalse)
        set(kVTCompressionPropertyKey_MaxFrameDelayCount, NSNumber(value: 0))
        set(kVTCompressionPropertyKey_ProfileLevel,
            codec == "hevc" ? kVTProfileLevel_HEVC_Main_AutoLevel : kVTProfileLevel_H264_High_AutoLevel)
        set(kVTCompressionPropertyKey_ExpectedFrameRate, NSNumber(value: fps))
        // A keyframe at least every 10 s: the phone asks for one after a decoding error, so this only bounds recovery.
        set(kVTCompressionPropertyKey_MaxKeyFrameIntervalDuration, NSNumber(value: 10))
        set(kVTCompressionPropertyKey_AverageBitRate, NSNumber(value: rate.kbps * 1000))
        VTCompressionSessionPrepareToEncodeFrames(session)
        return session
    }

    private func setBitrate(_ kbps: Int) {
        guard let encoder else { return }
        VTSessionSetProperty(encoder, key: kVTCompressionPropertyKey_AverageBitRate, value: NSNumber(value: kbps * 1000))
    }

    private func setFPS(_ fps: Int) {
        guard let stream, let config else { return }
        config.minimumFrameInterval = CMTime(value: 1, timescale: CMTimeScale(fps))
        stream.updateConfiguration(config) { [weak self] error in
            if let error { self?.warn("frame rate: \(error)") }
        }
        if let encoder {
            VTSessionSetProperty(encoder, key: kVTCompressionPropertyKey_ExpectedFrameRate, value: NSNumber(value: fps))
        }
    }

    // MARK: Frames

    func stream(_ stream: SCStream, didOutputSampleBuffer sample: CMSampleBuffer, of type: SCStreamOutputType) {
        // On `queue`.
        guard type == .screen, stream === self.stream, let encoder, sample.isValid,
              let info = (CMSampleBufferGetSampleAttachmentsArray(sample, createIfNecessary: false)
                  as? [[SCStreamFrameInfo: Any]])?.first,
              (info[.status] as? Int) == SCFrameStatus.complete.rawValue, // idle frames: nothing changed
              let image = sample.imageBuffer else { return }
        let shown = (info[.displayTime] as? UInt64) ?? (info[.displayTime] as? NSNumber)?.uint64Value ?? mach_absolute_time()
        let captured = Self.micros(shown)
        guard rate.admit(now: Self.nowMicros) else { return }

        var options: CFDictionary?
        if wantKeyframe {
            options = [kVTEncodeFrameOptionKey_ForceKeyFrame: kCFBooleanTrue] as CFDictionary
            wantKeyframe = false
        }
        let gen = generation
        VTCompressionSessionEncodeFrame(encoder, imageBuffer: image, presentationTimeStamp: sample.presentationTimeStamp,
                                        duration: .invalid, frameProperties: options, infoFlagsOut: nil) { [weak self] status, _, out in
            guard let self, status == noErr, let out else { return }
            self.queue.async {
                guard gen == self.generation else { return }
                self.emit(out, captured: captured)
            }
        }
    }

    /// One encoded sample: `format` first when the parameter sets changed, then the `frame`.
    private func emit(_ sample: CMSampleBuffer, captured: UInt64) {
        let attachments = CMSampleBufferGetSampleAttachmentsArray(sample, createIfNecessary: false) as? [[CFString: Any]]
        let keyframe = !((attachments?.first?[kCMSampleAttachmentKey_NotSync] as? Bool) ?? false)
        if let description = sample.formatDescription {
            let sets = Self.parameterSets(of: description, codec: codec)
            if !sets.isEmpty, sets != parameterSets {
                parameterSets = sets
                send(.format(FormatMessage(parameterSets: sets)))
            }
        }
        guard let block = sample.dataBuffer, let bytes = try? block.dataBytes() else { return }
        number &+= 1
        send(.frame(VideoFrame(number: number, captureMicros: captured, flags: keyframe ? .keyframe : [], sample: bytes)))
        lastSent = Self.nowMicros
    }

    func stream(_ stream: SCStream, didStopWithError error: Error) {
        queue.async { [self] in
            guard stream === self.stream else { return }
            warn("capture stopped: \(error)")
            pause(Self.consoleReason() ?? (CGPreflightScreenCaptureAccess() ? "display_gone" : "permission"))
        }
    }

    // MARK: Heartbeat and lock

    /// Once a second: pause when the Mac locks, start again when it unlocks, and send a heartbeat while the screen is
    /// still (ScreenCaptureKit sends nothing when nothing changes).
    private func startTimer() {
        guard timer == nil else { return }
        let timer = DispatchSource.makeTimerSource(queue: queue)
        timer.schedule(deadline: .now() + 1, repeating: 1, leeway: .milliseconds(100))
        timer.setEventHandler { [weak self] in self?.tick() }
        timer.resume()
        self.timer = timer
    }

    private func tick() {
        guard request != nil, !userPaused else { return }
        let action = CaptureTick.decide(session: Self.consoleReason(), paused: pausedReason, capturing: stream != nil,
                                        sinceLastSent: Self.nowMicros &- lastSent)
        switch action {
        case .none:
            break
        case let .pause(reason):
            pause(reason)
        case .resume:
            pausedReason = nil
            send(.resumed)
            begin()
        case .heartbeat:
            send(.heartbeat(HeartbeatMessage(last: Int(number))))
            lastSent = Self.nowMicros
        }
    }

    // MARK: Helpers

    private func send(_ message: ScreenVideoMessage) { onMessage?(message) }

    private func warn(_ text: String) {
        FileHandle.standardError.write(Data("persona-screen: \(text)\n".utf8))
    }

    /// The display's size in pixels (ScreenCaptureKit gives points).
    private static func pixelSize(of display: SCDisplay) -> (width: Int, height: Int) {
        if let mode = CGDisplayCopyDisplayMode(display.displayID) { return (mode.pixelWidth, mode.pixelHeight) }
        return (display.width, display.height)
    }

    /// The codec's parameter sets: HEVC's VPS, SPS and PPS, or H.264's SPS and PPS. Empty when they can't be read.
    private static func parameterSets(of description: CMFormatDescription, codec: String) -> [Data] {
        let hevc = codec == "hevc"
        func read(_ index: Int, _ pointer: UnsafeMutablePointer<UnsafePointer<UInt8>?>?, _ size: UnsafeMutablePointer<Int>?,
                  _ count: UnsafeMutablePointer<Int>?) -> OSStatus {
            hevc
                ? CMVideoFormatDescriptionGetHEVCParameterSetAtIndex(description, parameterSetIndex: index,
                                                                     parameterSetPointerOut: pointer, parameterSetSizeOut: size,
                                                                     parameterSetCountOut: count, nalUnitHeaderLengthOut: nil)
                : CMVideoFormatDescriptionGetH264ParameterSetAtIndex(description, parameterSetIndex: index,
                                                                     parameterSetPointerOut: pointer, parameterSetSizeOut: size,
                                                                     parameterSetCountOut: count, nalUnitHeaderLengthOut: nil)
        }
        var count = 0
        guard read(0, nil, nil, &count) == noErr else { return [] }
        var sets: [Data] = []
        for index in 0..<count {
            var pointer: UnsafePointer<UInt8>?
            var size = 0
            guard read(index, &pointer, &size, nil) == noErr, let pointer else { return [] }
            sets.append(Data(bytes: pointer, count: size))
        }
        return sets
    }

    private static let timebase: mach_timebase_info_data_t = {
        var info = mach_timebase_info_data_t()
        mach_timebase_info(&info)
        return info
    }()

    /// Mach ticks as µs on the monotonic clock: the timeline of `captureMicros` and of the rate controller. Scaled
    /// before dividing, so no precision is lost (the product only overflows after about two centuries of uptime).
    static func micros(_ ticks: UInt64) -> UInt64 { ticks * UInt64(timebase.numer) / UInt64(timebase.denom) / 1000 }

    static var nowMicros: UInt64 { micros(mach_absolute_time()) }

    /// Why the console session stops capture now, or nil (`ConsoleSession.reason`).
    static func consoleReason() -> String? {
        ConsoleSession.reason(from: CGSessionCopyCurrentDictionary() as? [String: Any])
    }
}
#endif
