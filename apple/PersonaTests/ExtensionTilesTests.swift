import Foundation
import Testing
@testable import Persona
import PersonaWire

/// UI version 6's tiles on iOS (#827): the showcase's grids and carousels read whole, with what a tile can hold
/// (schema/extension/examples/showcase/grid.json, carousel.json, home-demo.json).
@MainActor
struct ExtensionTilesTests {
    private static let showcase = repo.appending(path: "schema/extension/examples/showcase")
    private static let repo = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
        .deletingLastPathComponent()

    private static func items(_ name: String) throws -> [ExtensionComponent] {
        try JSONDecoder().decode(ExtensionView.self, from: Data(contentsOf: showcase.appending(path: name))).sections.flatMap(\.items)
    }

    @Test func gridsAndCarouselsAreReadWhole() throws {
        let grids = try Self.items("grid.json").compactMap { if case .grid(let g) = $0 { g } else { nil } }
        #expect(grids.count == 3 && grids.allSatisfy { !$0.tiles.isEmpty })
        let carousels = try Self.items("carousel.json").compactMap { if case .carousel(let c) = $0 { c } else { nil } }
        #expect(Set(carousels.map { $0.width ?? ExtensionTileWidth.regular }) == Set(ExtensionTileWidth.all))
        let tiles = (grids.flatMap(\.tiles) + carousels.flatMap(\.tiles))
        #expect(tiles.contains { $0.active == true } && tiles.contains { $0.icon != nil })
    }

    @Test func aTilesSwitchIsAControlThisAppDraws() throws {
        let tiles = try Self.items("home-demo.json").flatMap { c -> [ExtensionTile] in
            switch c {
            case .grid(let g): g.tiles
            case .carousel(let c): c.tiles
            default: []
            }
        }
        for c in tiles.compactMap(\.control) {
            #expect(c.kind == ExtensionRowControlKind.switch && c.drawable, "a tile's control is a switch: \(c.name)")
        }
    }

    @Test func gaugeRingsKeepTheTilesWordsAndReadTheSameValue() throws {
        for name in ["View.v9-quota.json", "View.v9-gauge-percent.json"] {
            let view = try JSONDecoder().decode(ExtensionView.self,
                from: Data(contentsOf: Self.repo.appending(path: "schema/extension/examples/" + name)))
            let tiles = view.sections.flatMap(\.items).flatMap { item -> [ExtensionTile] in
                switch item {
                case .grid(let grid): grid.tiles
                case .carousel(let carousel): carousel.tiles
                default: []
                }
            }
            let gauge = try #require(tiles.compactMap(\.gauge).first, "\(name)")
            let tile = try #require(tiles.first { $0.gauge != nil }, "\(name)")
            #expect(ExtensionGaugeReading.percent(gauge.ratio) == Int((gauge.ratio * 100).rounded()))
            #expect(ExtensionGaugeReading.center(tile, gauge: gauge) ==
                    (tile.value?.isEmpty == false ? tile.value! : "\(ExtensionGaugeReading.percent(gauge.ratio))%"))
            #expect(!ExtensionGaugeReading.accessibilityValue(tile, gauge: gauge).isEmpty)
        }

        let amount = ExtensionTile(title: "5 hours", value: "4.69")
        let gauge = ExtensionGauge(ratio: 0.938, unit: "USD", color: "green")
        #expect(ExtensionGaugeReading.accessibilityValue(amount, gauge: gauge) == "4.69 USD, 94%")
        let percent = ExtensionTile(title: "Disk")
        #expect(ExtensionGaugeReading.center(percent, gauge: gauge) == "94%")
        #expect(ExtensionGaugeReading.accessibilityValue(percent, gauge: gauge) == "94%")
        #expect(ExtensionGaugeReading.percent(-1) == 0 && ExtensionGaugeReading.percent(2) == 100)
    }
}
