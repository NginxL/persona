import Foundation

/// The decisions the capture source makes from the phone's `start` / `update` (#3954): which codec, what size, what
/// frame rate and bitrate. Kept apart from ScreenCaptureKit and VideoToolbox so they test on any platform.
public enum CapturePlan {
    /// The codecs this helper can encode, best first: HEVC Main, else H.264 High (docs/extensions/computer-use.md).
    public static let encodable = ["hevc", "h264"]

    /// The lowest and highest bitrate the helper will encode at, in kbit/s. The phone's `max_kbps` is clamped into
    /// this range, and adaptation never goes below the floor.
    public static let minKbps = 200
    public static let maxKbps = 50_000

    /// The bitrate a stream starts at before any receiver report: the phone's cap, but no more than 4 Mbit/s, so a
    /// fresh stream doesn't flood a relay before adaptation has seen how the link behaves. Adaptation raises it from
    /// there while the link stays calm (`RateController`).
    public static let startKbps = 4_000

    /// The phone's first choice that `available` can encode. With no overlap (an empty or unknown list) it is H.264,
    /// which every phone and browser decodes.
    public static func codec(offered: [String], available: [String] = encodable) -> String {
        offered.map { $0.lowercased() }.first(where: available.contains) ?? "h264"
    }

    /// The encoded size for a display `width` × `height` pixels: the long edge at most `maxSize` (0 or less: no cap),
    /// the aspect ratio kept, never larger than the display, both sides even (both codecs need it) and at least 2.
    public static func size(width: Int, height: Int, maxSize: Int) -> (width: Int, height: Int) {
        guard width > 0, height > 0 else { return (2, 2) }
        func even(_ v: Int) -> Int { max(2, v & ~1) }
        let long = max(width, height), short = min(width, height)
        guard maxSize > 0, long > maxSize else { return (even(width), even(height)) }
        // In integers, so the long edge is exactly maxSize and the short one is rounded, not floored by float error.
        let scaledShort = (short * maxSize + long / 2) / long
        return width >= height ? (even(maxSize), even(scaledShort)) : (even(scaledShort), even(maxSize))
    }

    /// The frame rate to capture at: the phone's, kept within 1–60.
    public static func fps(_ requested: Int) -> Int { min(max(requested, 1), 60) }

    /// The phone's bitrate cap, kept within `minKbps`–`maxKbps`. 0 or less means no cap from the phone.
    public static func capKbps(_ requested: Int) -> Int {
        requested <= 0 ? maxKbps : min(max(requested, minKbps), maxKbps)
    }

    /// The bitrate a stream starts at under the phone's cap.
    public static func initialKbps(cap: Int) -> Int { min(capKbps(cap), startKbps) }
}
