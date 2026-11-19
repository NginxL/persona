import CoreGraphics
import CoreImage
import Testing
import UIKit
@testable import Persona

/// Choosing a pairing QR from Photos (#11057) feeds the same payload handler as the camera. Exactly one readable code is
/// delivered once; an unreadable picture, no code, or several codes delivers nothing and explains the refusal.
@MainActor struct PairingPhotoTests {
    private static let payload = "persona://pair?url=https%3A%2F%2Fhub.example.com%3A18660&token=token%2Bwith%2Freserved%3Dchars"

    /// The handler is the camera handler's contract: parse the exact payload and ask before pairing. Keeping the delivery
    /// after a throw also checks that a rejected photo never reached confirmation or changed pairing state.
    private final class Delivery {
        var payloads: [String] = []
        var confirmations: [PairConfirmation] = []

        func receive(_ payload: String) {
            payloads.append(payload)
            if let config = ServerConfig(pairURL: payload) {
                confirmations.append(PairConfirmation(config, paired: []))
            }
        }
    }

    @Test func oneQRDeliversItsExactPayloadToConfirmationOnce() throws {
        let delivery = Delivery()
        try PairingPhoto.decode(try picture(codes: [Self.payload]), onCode: delivery.receive)

        let config = try #require(ServerConfig(pairURL: Self.payload))
        #expect(delivery.payloads == [Self.payload])
        #expect(delivery.confirmations.map(\.config) == [config])
        #expect(delivery.confirmations.count == 1)
    }

    @Test func aPictureWithoutAQRDeliversNothing() throws {
        try refused(try picture(codes: []), with: .noQRCode)
    }

    @Test func aPictureWithSeveralQRsDeliversNothing() throws {
        try refused(try picture(codes: [Self.payload, "persona://pair?url=https%3A%2F%2Fother.example.com&token=other"]),
                    with: .multipleQRCodes)
    }

    @Test func anUnreadablePictureDeliversNothing() throws {
        try refused(UIImage(), with: .unreadableImage)
    }

    @Test func eachRefusalHasClearErrorCopy() {
        let failures: [PairingPhoto.Failure] = [.unreadableImage, .noQRCode, .multipleQRCodes]
        #expect(failures.allSatisfy { $0.errorDescription?.isEmpty == false })
        #expect(Set(failures.map(\.localizedDescription)).count == failures.count)
    }

    private func refused(_ image: UIImage, with failure: PairingPhoto.Failure, sourceLocation: SourceLocation = #_sourceLocation) throws {
        let delivery = Delivery()
        #expect(throws: failure, sourceLocation: sourceLocation) {
            try PairingPhoto.decode(image, onCode: delivery.receive)
        }
        #expect(delivery.payloads.isEmpty, "a rejected photo reached the payload handler", sourceLocation: sourceLocation)
        #expect(delivery.confirmations.isEmpty, "a rejected photo asked to pair", sourceLocation: sourceLocation)
    }

    /// A white 640×320 photo, with up to two generated 256-pixel QRs surrounded by quiet space. No saved images or photo
    /// library access are involved, so valid, zero, and multiple-code cases are deterministic.
    private func picture(codes: [String]) throws -> UIImage {
        #expect(codes.count <= 2)
        let context = try #require(CGContext(data: nil, width: 640, height: 320, bitsPerComponent: 8, bytesPerRow: 0,
                                             space: CGColorSpace(name: CGColorSpace.sRGB)!,
                                             bitmapInfo: CGImageAlphaInfo.noneSkipLast.rawValue))
        context.setFillColor(UIColor.white.cgColor)
        context.fill(CGRect(x: 0, y: 0, width: 640, height: 320))
        context.interpolationQuality = .none
        for (index, code) in codes.enumerated() {
            context.draw(try qr(code), in: CGRect(x: 32 + index * 352, y: 32, width: 256, height: 256))
        }
        return UIImage(cgImage: try #require(context.makeImage()))
    }

    private func qr(_ payload: String) throws -> CGImage {
        let filter = try #require(CIFilter(name: "CIQRCodeGenerator"))
        filter.setValue(Data(payload.utf8), forKey: "inputMessage")
        filter.setValue("M", forKey: "inputCorrectionLevel")
        let output = try #require(filter.outputImage)
        let scale = 256 / output.extent.width
        let scaled = output.transformed(by: CGAffineTransform(scaleX: scale, y: scale))
        return try #require(CIContext().createCGImage(scaled, from: scaled.extent))
    }
}
