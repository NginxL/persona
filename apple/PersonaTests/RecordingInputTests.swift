import AVFoundation
import Foundation
import Testing
@testable import Persona

struct RecordingInputTests {
    @Test func phonePicksBuiltInMic() {
        let ports: [AVAudioSession.Port] = [.bluetoothHFP, .builtInMic]
        #expect(RecordingInput.preferredIndex(ports, for: .phone) == 1)
    }

    @Test func bluetoothPicksHFP() {
        let ports: [AVAudioSession.Port] = [.builtInMic, .bluetoothHFP]
        #expect(RecordingInput.preferredIndex(ports, for: .bluetooth) == 1)
    }

    @Test func bluetoothWithoutHFPIsNil() {
        let ports: [AVAudioSession.Port] = [.builtInMic, .bluetoothA2DP]
        #expect(RecordingInput.preferredIndex(ports, for: .bluetooth) == nil)
    }

    @Test func emptyListIsNil() {
        #expect(RecordingInput.preferredIndex([], for: .phone) == nil)
        #expect(RecordingInput.preferredIndex([], for: .bluetooth) == nil)
    }
}
