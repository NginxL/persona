import Foundation
import XCTest
@testable import PersonaScreenKit

final class ScreenVideoWireTests: XCTestCase {
    /// A representative message in each direction, for round-trip and streaming tests.
    private let samples: [ScreenVideoMessage] = [
        .hello(HelloMessage(version: 1, codec: "hevc", display: 2, width: 1920, height: 1080, rect: [0, 0, 1920, 1080])),
        .hello(HelloMessage(version: 1, codec: "h264", display: 0, width: 800, height: 600, rect: nil)),
        .format(FormatMessage(parameterSets: [Data([1, 2, 3]), Data([4, 5]), Data()])),
        .frame(VideoFrame(number: 42, captureMicros: 1_000_000_007, flags: [.keyframe], sample: Data([0, 0, 0, 4, 9, 9, 9, 9]))),
        .frame(VideoFrame(number: 43, captureMicros: 2, flags: [.refinement], sample: Data())),
        .heartbeat(HeartbeatMessage(last: 43)),
        .paused(PausedMessage(reason: "locked")),
        .resumed,
        .start(StartMessage(display: 1, maxSize: 1600, fps: 30, maxKbps: 12000, codecs: ["hevc", "h264"])),
        .start(StartMessage(display: nil, maxSize: 900, fps: 15, maxKbps: 1500, codecs: ["h264"])),
        .report(ReceiverReport(lastFrame: 42, captureMicros: 1_000_000_007, arrivalMicros: 1_000_050_000, bytesSinceLast: 40000, flags: [.decodingError])),
        .keyframe,
        .update(UpdateMessage(maxKbps: 4000, paused: true)),
        .update(UpdateMessage(display: 3)),
        .unknown(type: 200, body: Data([7, 7, 7])),
    ]

    func testRoundTrip() throws {
        for message in samples {
            var parser = ScreenVideoParser()
            let raws = try parser.push(message.framed())
            XCTAssertEqual(raws.count, 1, "\(message)")
            let decoded = try ScreenVideoMessage.decode(type: raws[0].type, body: raws[0].body)
            XCTAssertEqual(decoded, message)
        }
    }

    func testStreamedByteByByte() throws {
        var all = Data()
        for message in samples {
            all.append(message.framed())
        }
        var parser = ScreenVideoParser()
        var decoded: [ScreenVideoMessage] = []
        for byte in all {
            for raw in try parser.push(Data([byte])) {
                decoded.append(try ScreenVideoMessage.decode(type: raw.type, body: raw.body))
            }
        }
        XCTAssertEqual(decoded, samples)
    }

    func testLengthPrefixIsBigEndian() {
        // A keyframe is just its type byte: length 1, big-endian.
        XCTAssertEqual(ScreenVideoMessage.keyframe.framed(), Data([0, 0, 0, 1, 18]))
    }

    func testFormatBodyLayout() {
        // u8 count, then (u16 length + bytes) each.
        let framed = ScreenVideoMessage.format(FormatMessage(parameterSets: [Data([0xAA]), Data([0xBB, 0xCC])])).framed()
        // length = 1 (type) + 1 (count) + (2+1) + (2+2) = 9
        XCTAssertEqual(Array(framed.prefix(5)), [0, 0, 0, 9, 2])
        // body: count 2, then (len 1, 0xAA), (len 2, 0xBB 0xCC)
        XCTAssertEqual(Array(framed.dropFirst(5)), [2, 0, 1, 0xAA, 0, 2, 0xBB, 0xCC])
    }

    func testTooLongThrows() {
        var parser = ScreenVideoParser()
        let header = Data([0x7F, 0xFF, 0xFF, 0xFF, 1]) // ~2 GB length
        XCTAssertThrowsError(try parser.push(header)) { error in
            XCTAssertEqual(error as? ScreenVideoError, .messageTooLong(0x7FFFFFFF))
        }
    }

    func testTruncatedFrameThrows() {
        // A `frame` type with a body too short for its fixed fields (needs 4+8+1 bytes).
        let body = Data([0, 0, 0, 1, 0, 0]) // 6 bytes
        XCTAssertThrowsError(try ScreenVideoMessage.decode(type: ScreenVideoWire.Kind.frame.rawValue, body: body)) { error in
            XCTAssertEqual(error as? ScreenVideoError, .truncated)
        }
    }

    func testBadJSONThrows() {
        let body = Data("not json".utf8)
        XCTAssertThrowsError(try ScreenVideoMessage.decode(type: ScreenVideoWire.Kind.hello.rawValue, body: body)) { error in
            XCTAssertEqual(error as? ScreenVideoError, .badJSON)
        }
    }

    func testStartJSONUsesSnakeCase() throws {
        let framed = ScreenVideoMessage.start(StartMessage(display: 1, maxSize: 1600, fps: 30, maxKbps: 12000, codecs: ["hevc"])).framed()
        let body = framed.dropFirst(5) // past length + type
        let object = try JSONSerialization.jsonObject(with: body) as? [String: Any]
        XCTAssertEqual(object?["max_size"] as? Int, 1600)
        XCTAssertEqual(object?["max_kbps"] as? Int, 12000)
        XCTAssertNil(object?["maxSize"])
    }
}
