#if os(iOS)
import BeautifulUI
import SwiftUI
import Testing
import UIKit
@testable import Persona

/// Got It on a Just finished row (#7148): a compact accent chip at the status line's trailing end, not another line
/// under it. TodoRow is interface text, so the reachable sizes are Large through xxxLarge, not the AX sizes. Rendering
/// the same row with and without `onSeen` also checks that the chip takes none of the title's or status's room.
@MainActor struct TodoRowSeenLayoutTests {
    @Test(arguments: [CGFloat(390), 320], [DynamicTypeSize.large, .xxxLarge])
    func gotItTakesNoRowHeightOrExistingInk(width: CGFloat, size: DynamicTypeSize) throws {
        for scheme in [ColorScheme.light, .dark] {
            let control = try row(width: width, size: size, scheme: scheme, gotIt: false)
            let gotIt = try row(width: width, size: size, scheme: scheme, gotIt: true)
            let whereIs = "\(Int(width)) pt, \(size), \(scheme)"
            #expect(gotIt.layoutHeight == control.layoutHeight,
                    "Got It changed the row height at \(whereIs): \(control.layoutHeight) -> \(gotIt.layoutHeight)")
            #expect(gotIt.height == control.height, "rendered heights differ at \(whereIs)")
            #expect(gotIt.width == control.width, "rendered widths differ at \(whereIs)")

            let diff = control.difference(from: gotIt)
            #expect(diff.count > 32, "the two identical-height drawings must differ: Got It has to be drawn (\(whereIs))")
            // The old separate button starts at the content's leading edge. A chip at the row right changes no leading
            // title/status pixels; truncating, moving or reflowing that content would also change pixels there.
            #expect(diff.minX >= control.width / 2,
                    "Got It changed leading content instead of only the status line's empty trailing space at \(whereIs)")
            // A pixel painted in the control may not become background in the Got It row. This catches a squeezed title
            // or status even when the overall heights happen to match.
            #expect(diff.erased == 0, "Got It erased \(diff.erased) existing title/status pixels at \(whereIs)")
        }
    }

    /// A supervised Just finished row can have a long status even at the interface cap. Got It then takes its own line
    /// below the untouched status, still at the right, rather than squeezing or overflowing the supported layout.
    @Test(arguments: [ColorScheme.light, .dark])
    func gotItFallsBelowALongReviewStatusAt320xxxLarge(scheme: ColorScheme) throws {
        let date = Date(timeIntervalSince1970: 1_760_000_000)
        let review = TodoItem(id: "seen-review", title: "Check the supervised result",
                              createdAt: date.addingTimeInterval(-3600), updatedAt: date,
                              agents: [TodoItem.Agent(machineID: "m", sessionID: "s")],
                              supervision: TaskSupervision(supervisor: "main", doneWhen: ["one", "two"],
                                                           state: "pending_review"))
        let control = try row(width: 320, size: .xxxLarge, scheme: scheme, gotIt: false, todo: review)
        let gotIt = try row(width: 320, size: .xxxLarge, scheme: scheme, gotIt: true, todo: review)
        let chip = Chip(text: "Got It", symbol: Theme.Icon.seen, look: .tint(Theme.accent), compact: true)
        let ownLine = fitting(chip, size: .xxxLarge).height - Theme.Space.xxs + Theme.Space.xs
        let added = gotIt.layoutHeight - control.layoutHeight
        #expect(abs(added - ownLine) < 0.5,
                "Got It must add exactly its own line below the long status: added \(added), expected \(ownLine)")
        #expect(gotIt.width == control.width, "the long status and Got It overflow the 320 pt row")
        try save(gotIt, name: "todos-seen-review-320-xxxLarge-\(scheme)")
    }

    /// The component itself gains a symbol only in its compact drawing; its capsule stays much smaller than the hit area.
    @Test(arguments: [DynamicTypeSize.large, .xxxLarge])
    func compactChipDrawsItsSymbolWithoutBecomingAButton(size: DynamicTypeSize) throws {
        let words = Chip(text: "Got It", look: .tint(Theme.accent), compact: true)
        let checked = Chip(text: "Got It", symbol: Theme.Icon.seen, look: .tint(Theme.accent), compact: true)
        let wordSize = fitting(words, size: size), checkedSize = fitting(checked, size: size)
        #expect(checkedSize.width > wordSize.width + 4, "a compact Chip still drops its symbol at \(size)")
        #expect(try png(checked, size: size) != png(words, size: size), "the check symbol paints no pixels at \(size)")
        #expect(checkedSize.height < Theme.Size.tapTarget,
                "the drawn capsule, rather than only its hit area, became 44 pt tall at \(size)")
    }

    /// Adding compact symbol rendering is not license to change an ordinary Chip. These references are its pre-#7148
    /// drawing in both the plain and BeautifulUI styles, with and without a symbol and in every look.
    @Test(arguments: [DynamicTypeSize.large, .xxxLarge], [ColorScheme.light, .dark])
    func nonCompactChipsKeepTheirPreviousDrawing(size: DynamicTypeSize, scheme: ColorScheme) throws {
        let looks: [Chip.Look] = [.neutral, .tint(Theme.accent), .outline]
        for (index, look) in looks.enumerated() {
            for symbol in [nil, Theme.Icon.seen] as [String?] {
                let actual = Chip(text: "Read Only", symbol: symbol, look: look)
                let old = LegacyNonCompactChip(text: "Read Only", symbol: symbol, look: look)
                #expect(try png(actual, size: size, scheme: scheme) == png(old, size: size, scheme: scheme),
                        "non-compact look \(index), symbol \(symbol != nil) changed at \(size), \(scheme)")
            }
        }
    }

    /// Files' existing compact Read Only chip used to ignore the lock in its call. Read that call so this test passes
    /// both before and after the fix: if compact Chips now draw symbols, Files must not silently acquire a new lock.
    @Test(arguments: [DynamicTypeSize.large, .xxxLarge], [ColorScheme.light, .dark])
    func filesReadOnlyKeepsItsTextOnlyDrawing(size: DynamicTypeSize, scheme: ColorScheme) throws {
        let source = try String(contentsOf: Self.apple.appending(path: "Persona/Views/Session/FilesPane.swift"), encoding: .utf8)
        let identifier = try #require(source.range(of: ".accessibilityIdentifier(\"files.readonly\")"))
        let call = try #require(source[..<identifier.lowerBound].range(of: "Chip(", options: .backwards))
        let hadSymbol = source[call.lowerBound..<identifier.lowerBound].contains("symbol:")
        let actual = Chip(text: "Read Only", symbol: hadSymbol ? Theme.Icon.locked : nil, compact: true)
        let old = LegacyCompactTextChip(text: "Read Only")
        #expect(try png(actual, size: size, scheme: scheme) == png(old, size: size, scheme: scheme),
                "Files Read Only changed appearance at \(size), \(scheme)")
    }

    private static let apple = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()

    private func row(width: CGFloat, size: DynamicTypeSize, scheme: ColorScheme, gotIt: Bool,
                     todo supplied: TodoItem? = nil) throws -> RowDrawing {
        let date = Date(timeIntervalSince1970: 1_760_000_000)
        let todo = supplied ?? TodoItem(id: "seen-layout", title: "Ship the faster page without reducing the title's room",
                                        doneAt: date, createdAt: date.addingTimeInterval(-3600), updatedAt: date, doneBy: "auto")
        let triage = TodoTriage(section: .justDone, rule: todo.doneAt == nil ? .finishedUnseen : .closed, last: date)
        let content = TodoRow(todo: todo, triage: triage, onToggleDone: {}, onSeen: gotIt ? {} : nil)
            .environment(AppModel.shared)
            .environment(\.dynamicTypeSize, size)
            .environment(\.colorScheme, scheme)
            .frame(width: width, alignment: .topLeading)
            .fixedSize(horizontal: false, vertical: true)
        let fitting = UIHostingController(rootView: content).sizeThatFits(in: CGSize(width: width, height: 2000))
        return try RowDrawing(content.background(scheme == .light ? Color.white : Color.black),
                              scheme: scheme, layoutHeight: fitting.height)
    }

    private func save(_ drawing: RowDrawing, name: String) throws {
        guard let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] else { return }
        let data = try #require(drawing.png)
        try data.write(to: URL(fileURLWithPath: dir).appendingPathComponent(name + ".png"))
    }

    private func fitting(_ view: some View, size: DynamicTypeSize) -> CGSize {
        UIHostingController(rootView: view.fixedSize().environment(\.dynamicTypeSize, size))
            .sizeThatFits(in: CGSize(width: 1000, height: 1000))
    }

    private func png(_ view: some View, size: DynamicTypeSize, scheme: ColorScheme = .light) throws -> Data {
        let renderer = ImageRenderer(content: view.fixedSize()
            .environment(\.dynamicTypeSize, size).environment(\.colorScheme, scheme))
        renderer.scale = 2
        return try #require(renderer.uiImage?.pngData())
    }
}

/// The non-compact Chip drawing as it stood before compact symbol support (#7148).
private struct LegacyNonCompactChip: View {
    let text: String
    let symbol: String?
    let look: Chip.Look

    var body: some View {
        if AppStyle.current.usesBeautifulUI {
            BUIPill(text, tone: pillTone, icon: symbol.map { .symbol(Theme.Icon.styled($0)) }, truncates: false)
        } else {
            Group {
                if let symbol {
                    HStack(spacing: Theme.Space.xs) {
                        ThemedIcon(symbol).font(Theme.Font.labelStrong)
                        Text(text).lineLimit(1)
                    }
                } else {
                    Text(text).lineLimit(1)
                }
            }
            .font(Theme.Font.labelMedium)
            .foregroundStyle(words)
            .padding(.horizontal, Theme.Space.s)
            .padding(.vertical, 2.5)
            .background {
                if case .outline = look {
                    Capsule().fill(fill).stroke(Color.secondary.opacity(0.4), lineWidth: 0.8)
                } else {
                    Capsule().fill(fill)
                }
            }
            .fixedSize(horizontal: true, vertical: true)
        }
    }

    private var pillTone: BUIPill.Tone {
        switch look {
        case .neutral: .neutral
        case .tint(let color, let text): .custom(ink: text ?? color, fill: text == nil ? color.opacity(0.12) : color)
        case .outline: .outline
        }
    }

    private var words: Color {
        switch look {
        case .neutral, .outline: .secondary
        case .tint(let color, let text): text ?? color
        }
    }

    private var fill: Color {
        switch look {
        case .neutral: Theme.Fill.chip
        case .tint(let color, let text): text == nil ? color.opacity(0.12) : color
        case .outline: .clear
        }
    }
}

/// Files Read Only's compact, text-only drawing before compact Chips gained symbols.
private struct LegacyCompactTextChip: View {
    let text: String

    var body: some View {
        Text(text).lineLimit(1)
            .font(Theme.Font.label)
            .foregroundStyle(.secondary)
            .padding(.horizontal, Theme.Space.sm)
            .padding(.vertical, Theme.Space.xxs / 2)
            .background(Capsule().fill(Theme.Fill.chip))
            .fixedSize()
    }
}

private struct RowDrawing {
    let width: Int, height: Int
    let layoutHeight: CGFloat
    let scale: CGFloat
    let scheme: ColorScheme
    let png: Data?
    private let pixels: [UInt8]
    private let background: UInt32

    @MainActor init(_ view: some View, scheme: ColorScheme, layoutHeight: CGFloat) throws {
        let renderer = ImageRenderer(content: view)
        renderer.scale = 2
        let image = try #require(renderer.cgImage)
        png = UIImage(cgImage: image).pngData()
        width = image.width; height = image.height; scale = renderer.scale
        self.scheme = scheme; self.layoutHeight = layoutHeight
        background = scheme == .light ? 0xFFFFFFFF : 0x000000FF
        var data = [UInt8](repeating: 0, count: width * height * 4)
        let context = try #require(CGContext(data: &data, width: width, height: height, bitsPerComponent: 8,
            bytesPerRow: width * 4, space: CGColorSpaceCreateDeviceRGB(),
            bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue))
        context.draw(image, in: CGRect(x: 0, y: 0, width: width, height: height))
        pixels = data
    }

    private func pixel(x: Int, y: Int) -> UInt32 {
        guard x >= 0, y >= 0, x < width, y < height else { return background }
        let i = (y * width + x) * 4
        return UInt32(pixels[i]) << 24 | UInt32(pixels[i + 1]) << 16 | UInt32(pixels[i + 2]) << 8 | UInt32(pixels[i + 3])
    }

    private func isInk(_ pixel: UInt32) -> Bool {
        let r = (pixel >> 24) & 0xFF, g = (pixel >> 16) & 0xFF, b = (pixel >> 8) & 0xFF
        return scheme == .light ? r < 245 || g < 245 || b < 245 : r > 10 || g > 10 || b > 10
    }

    func difference(from other: RowDrawing) -> (count: Int, erased: Int, minX: Int) {
        var count = 0, erased = 0, minX = Int.max
        for y in 0..<max(height, other.height) {
            for x in 0..<max(width, other.width) {
                let a = pixel(x: x, y: y), b = other.pixel(x: x, y: y)
                if a != b { count += 1; minX = min(minX, x) }
                if isInk(a), !other.isInk(b) { erased += 1 }
            }
        }
        return (count, erased, minX)
    }
}
#endif
