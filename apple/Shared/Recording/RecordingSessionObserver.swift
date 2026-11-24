import AVFoundation

/// What the audio session tells a recording, the same on every OS version: watchOS / iOS / visionOS 27 say it with
/// typed messages, older systems with `AVAudioSession.interruptionNotification`.
nonisolated enum RecordingSessionEvent: Equatable, Sendable {
    /// The system took the session (a call, Siri, an alarm, another app).
    case interrupted(reason: AVAudioSession.InterruptionReason?)
    /// The interruption is over; `shouldResume` is the system's advice.
    case interruptionEnded(shouldResume: Bool)
    /// 27 and later: the app deactivated its own session (`setActive(false)` when a recording stops). Not an interruption.
    case deactivatedByApp
    /// 27 and later: the session became active, our own `setActive(true)` included.
    case activated
}

/// Observes the shared audio session for capture. On 27 the old interruption notification is deprecated in favour of
/// `didBecomeInactive` / `resumptionRecommendation`, which also tell our own deactivation apart from a call's; only one
/// of the two is observed, so an interruption is never handled twice. Lives as long as its owner (capture is a
/// singleton), so the observations are never removed.
@MainActor
final class RecordingSessionObserver {
    /// `[Any]`: a 27 observation token (`NotificationCenter.ObservationToken`, watchOS 26+) can't be the stored type in
    /// a watchOS 10 build.
    private var tokens: [Any] = []

    init(_ handle: @escaping @MainActor (RecordingSessionEvent) -> Void) {
        let center = NotificationCenter.default
        #if compiler(>=6.4)
        if #available(iOS 27.0, watchOS 27.0, visionOS 27.0, *) {
            // `of: AVAudioSession.self`: from any session object, as `object: nil` was.
            tokens.append(center.addObserver(of: AVAudioSession.self, for: .didBecomeInactive) { message in
                switch message.deactivationResult {
                case .systemInterruption(let context): handle(.interrupted(reason: context.reason))
                case .appDeactivated: handle(.deactivatedByApp)
                @unknown default: handle(.interrupted(reason: nil))
                }
            })
            tokens.append(center.addObserver(of: AVAudioSession.self, for: .resumptionRecommendation) { message in
                handle(.interruptionEnded(shouldResume: message.recommendation == .shouldResume))
            })
            tokens.append(center.addObserver(of: AVAudioSession.self, for: .didBecomeActive) { _ in handle(.activated) })
            return
        }
        #endif
        tokens.append(center.addObserver(forName: AVAudioSession.interruptionNotification, object: nil, queue: .main) { @Sendable note in
            let info = note.userInfo ?? [:]
            let type = (info[AVAudioSessionInterruptionTypeKey] as? UInt).flatMap(AVAudioSession.InterruptionType.init)
            let reason = (info[AVAudioSessionInterruptionReasonKey] as? UInt).flatMap(AVAudioSession.InterruptionReason.init)
            let options = AVAudioSession.InterruptionOptions(rawValue: info[AVAudioSessionInterruptionOptionKey] as? UInt ?? 0)
            MainActor.assumeIsolated {
                switch type {
                case .began: handle(.interrupted(reason: reason))
                case .ended: handle(.interruptionEnded(shouldResume: options.contains(.shouldResume)))
                default: break
                }
            }
        })
    }
}
