import SwiftUI
import Testing
import UIKit
@testable import Persona

@MainActor @Suite(.serialized)
struct CalmAttentionTests {
    private func components(_ color: Color, dark: Bool) -> [Double] {
        let c = UIColor(color).resolvedColor(with: UITraitCollection(userInterfaceStyle: dark ? .dark : .light))
        var r: CGFloat = 0, g: CGFloat = 0, b: CGFloat = 0, a: CGFloat = 0
        c.getRed(&r, green: &g, blue: &b, alpha: &a)
        return [Double(r), Double(g), Double(b)]
    }

    private func contrast(_ foreground: Color, _ background: Color, dark: Bool) -> Double {
        func luminance(_ rgb: [Double]) -> Double {
            let linear = rgb.map { $0 <= 0.04045 ? $0 / 12.92 : pow(($0 + 0.055) / 1.055, 2.4) }
            return zip(linear, [0.2126, 0.7152, 0.0722]).reduce(0) { $0 + $1.0 * $1.1 }
        }
        let a = luminance(components(foreground, dark: dark))
        let b = luminance(components(background, dark: dark))
        return (max(a, b) + 0.05) / (min(a, b) + 0.05)
    }

    @Test func attentionIsNotAnError() {
        for dark in [false, true] {
            let attention = components(StatusPalette.needsYou.dot, dark: dark)
            #expect(attention[2] > attention[0])
            #expect(StatusPalette.needsYou.dot == Palette.systemBlue)
            #expect(StatusPalette.needsYou.text != Theme.danger)
            let error = components(Theme.danger, dark: dark)
            #expect(error[0] > error[2])
        }
    }

    @Test func routineStatusLabelsHaveNeutralFills() {
        #expect(StatusPalette.needsYou.tint == StatusPalette.working.tint)
        #expect(StatusPalette.needsYou.tint == StatusPalette.unread.tint)
        #expect(StatusPalette.needsYou.tint == Color.secondary.opacity(0.08))
    }

    @Test func smallAttentionTextMeetsAA() {
        for dark in [false, true] {
            for background in [Color(.systemBackground), Color(.secondarySystemBackground), Theme.codeBackground] {
                let ratio = contrast(StatusPalette.needsYou.text, background, dark: dark)
                #expect(ratio >= 4.5, "attention text contrast: \(ratio), dark: \(dark)")
            }
        }
    }
}
