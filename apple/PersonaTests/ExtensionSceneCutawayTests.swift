import Foundation
import PersonaWire
import Testing
import simd
@testable import Persona

/// The scene's cutaway (#13036): which walls come down for a camera, what stands in them, what is drawn see-through, and
/// that the model rebuilds only when the set changes. Synthetic geometry only.
@MainActor
struct ExtensionSceneCutawayTests {
    // A 6 x 4 m room, walls 2.5 m high and 0.1 m thick, south wall along y = 0, west wall along x = 0.
    private enum I {
        static let south = 0, north = 1, west = 2, east = 3
        static let window = 4, airConditioner = 5, wardrobeSouth = 6, wardrobeNorth = 7, sofa = 8, floor = 9
    }

    private static func room() -> [ExtensionSceneShape] {
        func wall(_ c: [Double], _ s: [Double]) -> ExtensionSceneShape {
            .box(ExtensionSceneBox(center: c, size: s, bottom: 0, top: 2.5, color: "#EAE6DF", wall: true))
        }
        return [
            wall([3, 0], [6, 0.1]),   // south
            wall([3, 4], [6, 0.1]),   // north
            wall([0, 2], [0.1, 4]),   // west
            wall([6, 2], [0.1, 4]),   // east
            .box(ExtensionSceneBox(center: [2, 0], size: [1.2, 0.04], bottom: 0.9, top: 2.3, color: "#B8D8E8", material: "glass")),
            .box(ExtensionSceneBox(center: [4.5, 0.15], size: [0.9, 0.2], bottom: 2.05, top: 2.33, color: "#F4F4F2")),
            .box(ExtensionSceneBox(center: [1, 0.36], size: [1.2, 0.6], bottom: 0, top: 2.1, color: "#EFEBE4", material: "wood")),
            .box(ExtensionSceneBox(center: [3, 3.64], size: [1.2, 0.6], bottom: 0, top: 2.1, color: "#EFEBE4", material: "wood")),
            .box(ExtensionSceneBox(center: [4, 0.5], size: [2, 0.9], bottom: 0, top: 0.85, color: "#C8C8C6")),
            .prism(ExtensionScenePrism(points: [[0, 0], [6, 0], [6, 4], [0, 4]], bottom: -0.02, top: 0, material: "tile")),
        ]
    }

    @Test func theWallsFacingTheCameraComeDown() {
        let shapes = Self.room()
        let walls = ExtensionSceneCutaway.walls(shapes)
        #expect(walls.map(\.index) == [I.south, I.north, I.west, I.east])
        let bounds = ExtensionSceneBounds.of(shapes)
        // from the south-west (the default yaw): the south and west walls
        #expect(ExtensionSceneCutaway.lowered(walls, bounds: bounds, yaw: 225) == [I.south, I.west])
        // from the north-east: the north and east walls
        #expect(ExtensionSceneCutaway.lowered(walls, bounds: bounds, yaw: 45) == [I.north, I.east])
        // straight from the south: only the south wall (the side walls are seen edge-on)
        #expect(ExtensionSceneCutaway.lowered(walls, bounds: bounds, yaw: 270) == [I.south])
        // a scene without walls marked never cuts anything
        let unmarked = shapes.map { shape -> ExtensionSceneShape in
            guard case .box(var b) = shape else { return shape }
            b.wall = nil
            return .box(b)
        }
        #expect(ExtensionSceneCutaway.walls(unmarked).isEmpty)
    }

    @Test func whatStandsInALoweredWallIsCutAndTallFurnitureInFrontIsSeeThrough() {
        let shapes = Self.room()
        let walls = ExtensionSceneCutaway.walls(shapes)
        let bounds = ExtensionSceneBounds.of(shapes)
        let lowered = ExtensionSceneCutaway.lowered(walls, bounds: bounds, yaw: 225)
        let t = ExtensionSceneCutaway.treatments(shapes, walls: walls, lowered: lowered, yaw: 225)
        let cut = Theme.Scene.cutawayHeight
        #expect(t[I.south] == .clip(top: cut) && t[I.west] == .clip(top: cut))
        #expect(t[I.north] == .keep && t[I.east] == .keep)
        // the window starts above the cut: left out with its wall
        #expect(t[I.window] == .hide)
        // the air conditioner hangs above it too
        #expect(t[I.airConditioner] == .hide)
        // the wardrobe against the lowered wall would read as a wall: see-through; the one against the far wall stays
        #expect(t[I.wardrobeSouth] == .seeThrough)
        #expect(t[I.wardrobeNorth] == .keep)
        // a sofa lower than the cut plus the margin stays as it is, and so does the floor
        #expect(t[I.sofa] == .keep && t[I.floor] == .keep)

        // turned round, the other side comes down
        let back = ExtensionSceneCutaway.treatments(shapes, walls: walls,
                                                     lowered: ExtensionSceneCutaway.lowered(walls, bounds: bounds, yaw: 45), yaw: 45)
        #expect(back[I.wardrobeNorth] == .seeThrough && back[I.wardrobeSouth] == .keep)
        #expect(back[I.window] == .keep && back[I.south] == .keep)
    }

    @Test func aShapeReachingAboveTheCutIsClippedToIt() {
        let shapes = Self.room() + [
            // a door frame's jamb standing in the south wall, from the floor up
            .box(ExtensionSceneBox(center: [5, 0], size: [0.05, 0.14], bottom: 0, top: 2.1, material: "wood")),
        ]
        let walls = ExtensionSceneCutaway.walls(shapes)
        let lowered = ExtensionSceneCutaway.lowered(walls, bounds: ExtensionSceneBounds.of(shapes), yaw: 225)
        let t = ExtensionSceneCutaway.treatments(shapes, walls: walls, lowered: lowered, yaw: 225)
        #expect(t.last == .clip(top: Theme.Scene.cutawayHeight))
        let drawn = ExtensionSceneCutaway.apply(shapes, treatments: t)
        guard case .box(let jamb) = drawn.shapes.last else {
            Issue.record("the jamb was not drawn")
            return
        }
        #expect(abs(jamb.top - Theme.Scene.cutawayHeight) < 1e-9 && jamb.bottom == 0)
        // the window and the air conditioner are gone, the wardrobe is marked see-through
        #expect(drawn.shapes.count == shapes.count - 2)
        #expect(drawn.seeThrough.count == 1)
    }

    @Test func aWallBetweenRoomsKeepsItsHeightAndWhatHangsOnIt() {
        // a wall across the room's south part at y = 1.2 (a room on each side) with a TV on its south face
        let inner = 10, tv = 11
        let shapes = Self.room() + [
            .box(ExtensionSceneBox(center: [3, 1.2], size: [6, 0.1], bottom: 0, top: 2.5, color: "#EAE6DF", wall: true)),
            .box(ExtensionSceneBox(center: [3, 1.13], size: [1.2, 0.04], bottom: 1.1, top: 1.8, color: "#1E2024", material: "screen")),
        ]
        let walls = ExtensionSceneCutaway.walls(shapes)
        #expect(walls.last?.floorAcross == true && walls.last?.floorOpposite == true)
        #expect(walls.first?.floorAcross == true && walls.first?.floorOpposite == false, "the south wall has the room on its north side only")
        let lowered = ExtensionSceneCutaway.lowered(walls, bounds: ExtensionSceneBounds.of(shapes), yaw: 225)
        #expect(lowered == [I.south, I.west])
        let t = ExtensionSceneCutaway.treatments(shapes, walls: walls, lowered: lowered, yaw: 225)
        #expect(t[inner] == .keep && t[tv] == .keep)

        // without floors there is no telling: every wall in the camera's half that faces it comes down, the inner one
        // too (one index lower without the floor)
        let bare = shapes.filter { if case .prism = $0 { return false } else { return true } }
        let bareWalls = ExtensionSceneCutaway.walls(bare)
        #expect(ExtensionSceneCutaway.lowered(bareWalls, bounds: ExtensionSceneBounds.of(bare), yaw: 225) == [I.south, I.west, inner - 1])
    }

    @Test func theBuilderKeepsTheFramingAndBatchesSeeThroughApart() {
        let shapes = Self.room()
        let plain = ExtensionSceneGeometryBuilder.build(shapes)
        let cut = ExtensionSceneGeometryBuilder.build(shapes, cutawayYaw: 225)
        #expect(plain.lowered.isEmpty && cut.lowered == [I.south, I.west])
        #expect(cut.bounds == plain.bounds, "walls coming down never change the framing")
        #expect(cut.batches.contains { $0.material == ExtensionSceneGeometryBuilder.seeThrough })
        #expect(!plain.batches.contains { $0.material == ExtensionSceneGeometryBuilder.seeThrough })
        #expect(cut.totalTriangles < plain.totalTriangles, "the window and the air conditioner are left out")
    }

    @Test func turningRebuildsOnlyWhenTheLoweredWallsChange() async throws {
        let scene = ExtensionSceneComponent(key: "room", alt: "A synthetic room", camera: ExtensionSceneCamera(yaw: 225, pitch: 35, zoom: 1),
                                            shapes: Self.room())
        let model = ExtensionSceneModel(scene: scene, keeps: false)
        await model.reload(scene)
        #expect(model.prepared?.lowered == [I.south, I.west] && model.geometryVersion == 1)

        // a small turn keeps the same walls down: nothing is rebuilt
        model.orbit(deltaYaw: 10, deltaPitch: 0)
        try await Task.sleep(for: .milliseconds(200))
        #expect(model.geometryVersion == 1)

        // turned to the north-east: the other walls come down, built off the main actor
        model.orbit(deltaYaw: 180, deltaPitch: 0)
        for _ in 0..<50 where model.geometryVersion == 1 { try await Task.sleep(for: .milliseconds(20)) }
        #expect(model.geometryVersion == 2 && model.prepared?.lowered == [I.north, I.east])

        // back to the opening view
        model.reset()
        for _ in 0..<50 where model.geometryVersion == 2 { try await Task.sleep(for: .milliseconds(20)) }
        #expect(model.geometryVersion == 3 && model.prepared?.lowered == [I.south, I.west])
    }
}
