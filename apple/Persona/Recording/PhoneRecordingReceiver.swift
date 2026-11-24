import Foundation

/// Installs the receiver before WCSession activates; the system may launch the app in the background just to deliver a file.
final class PhoneRecordingReceiver {
    static let shared = PhoneRecordingReceiver()
    private var started = false

    func start() {
        guard !started else { return }
        started = true
        // All-day recording's chunks, events and answers come through their own hooks, in place before the session
        // activates like `onRecording` below (docs/assistant/day-recap.md「Watch to phone」).
        RecordingDayReceiver.shared.start()
        _ = RecordingCapture.shared
        WatchSync.shared.onRecording = { transfer in
            do {
                let first = RecordingLibrary.shared.entry(transfer.id) == nil
                try RecordingLibrary.shared.receive(transfer)
                #if os(iOS)
                // A day recording is never a meeting, whatever the calendar says.
                if first, transfer.kind != .day { MeetingRecorder.tagWatchRecording(transfer.id, at: transfer.createdAt) }
                #endif
                WatchSync.shared.confirmRecording(RecordingReceipt(id: transfer.id, index: transfer.segment?.index, total: transfer.total))
                RecordingTranscription.shared.enqueue(transfer.id)
                if RecordingLibrary.shared.entry(transfer.id)?.isRolling == true { RecordingRollingKeeper.shared.schedule() }
            } catch { /* No ack: the watch keeps the original and retries. */ }
        }
        RecordingCapture.shared.onSaved = { RecordingTranscription.shared.enqueue($0) }
        RecordingTranscription.shared.resumePending()
        RecordingRollingKeeper.shared.schedule()
        // Each transcribed segment is saved, and each save syncs the text to the knowledge base (docs/apple/recording.md).
        RecordingSync.shared.start()
        #if os(iOS)
        MeetingRecorder.shared.start() // the Live Activity, and picking up after a call
        #endif
    }
}
