#if os(iOS)
import Foundation
import PersonaWire
import simd

/// A scene's cutaway (#13036, docs/extensions/extensions.md "UI version 11"): the outer walls between the camera and the
/// rooms are drawn low, so the rooms stay in view while the far walls and the walls between rooms keep their height.
///
/// - A box marked `wall` is lowered when it stands in the camera's half of the model, faces the camera (not seen
///   edge-on) and is an outer wall on that side (no floor just past its camera-side face; a wall between two rooms,
///   with the TV on it, stays): to `Theme.Scene.cutawayHeight` above its bottom.
/// - A shape standing in a lowered wall's thickness (a window, its frame, a door frame, curtains, an air conditioner) is
///   cut to the same height, or left out when it starts above it.
/// - A shape that rises `Theme.Scene.cutawaySeeThroughAbove` over that height within `Theme.Scene.cutawayReach` of a
///   lowered wall, on the room's side, is drawn see-through: the back of a wardrobe against that wall would otherwise
///   read as another wall.
///
/// Pure and cheap: `lowered` is a dot product per wall, so the view can ask on every camera change and rebuild the
/// geometry (off the main actor) only when the set changes.
nonisolated enum ExtensionSceneCutaway {
    struct Wall: Equatable, Sendable {
        /// Its index in the scene's shapes.
        var index: Int
        var center: SIMD2<Double>
        /// Unit vectors on the plan: along the wall, and across it.
        var along: SIMD2<Double>
        var across: SIMD2<Double>
        var halfLength: Double
        var halfThickness: Double
        var bottom: Double
        var top: Double
        /// A floor lies just past its face on the `across` side, and on the other side: a room's wall there.
        var floorAcross = false
        var floorOpposite = false
    }

    enum Treatment: Equatable, Sendable {
        case keep
        /// Drawn up to this height.
        case clip(top: Double)
        case hide
        case seeThrough
    }

    /// The boxes marked `wall`, as walls: their longer side is the one they run along.
    static func walls(_ shapes: [ExtensionSceneShape]) -> [Wall] {
        let floors = floors(shapes)
        var out: [Wall] = []
        for (index, shape) in shapes.enumerated() {
            guard case .box(let box) = shape, box.wall == true, box.center.count >= 2, box.size.count >= 2,
                  box.center.allSatisfy(\.isFinite), box.size.allSatisfy(\.isFinite) else { continue }
            let yaw = (box.yaw ?? 0) * .pi / 180
            let x = SIMD2(cos(yaw), sin(yaw)), y = SIMD2(-sin(yaw), cos(yaw))
            let longX = box.size[0] >= box.size[1]
            let center = SIMD2(box.center[0], box.center[1]), across = longX ? y : x
            let reach = min(box.size[0], box.size[1]) / 2 + Theme.Scene.cutawayProbe
            out.append(Wall(
                index: index, center: center,
                along: longX ? x : y, across: across,
                halfLength: max(box.size[0], box.size[1]) / 2, halfThickness: min(box.size[0], box.size[1]) / 2,
                bottom: min(box.bottom, box.top), top: max(box.bottom, box.top),
                floorAcross: floors.contains { inside(center + across * reach, $0) },
                floorOpposite: floors.contains { inside(center - across * reach, $0) }
            ))
        }
        return out
    }

    /// The prisms low enough to be floors, as polygons on the plan.
    static func floors(_ shapes: [ExtensionSceneShape]) -> [[SIMD2<Double>]] {
        shapes.compactMap { shape in
            guard case .prism(let p) = shape, max(p.bottom, p.top) <= Theme.Scene.cutawayFloorTop else { return nil }
            let points = p.points.compactMap { $0.count >= 2 && $0[0].isFinite && $0[1].isFinite ? SIMD2($0[0], $0[1]) : nil }
            return points.count >= 3 ? points : nil
        }
    }

    /// Whether a point lies inside a polygon (even-odd).
    static func inside(_ point: SIMD2<Double>, _ polygon: [SIMD2<Double>]) -> Bool {
        var result = false
        var j = polygon.count - 1
        for i in polygon.indices {
            let a = polygon[i], b = polygon[j]
            if (a.y > point.y) != (b.y > point.y), point.x < (b.x - a.x) * (point.y - a.y) / (b.y - a.y) + a.x {
                result.toggle()
            }
            j = i
        }
        return result
    }

    /// The camera's direction from the model's middle on the plan: the view looks from (cos yaw, sin yaw).
    static func cameraDirection(yaw: Double) -> SIMD2<Double> {
        let r = yaw * .pi / 180
        return SIMD2(cos(r), sin(r))
    }

    /// The shape indices of the walls drawn low with the camera at `yaw` (degrees), sorted: those in the camera's half
    /// of the model that face it with no floor on the camera's side.
    static func lowered(_ walls: [Wall], bounds: ExtensionSceneBounds, yaw: Double) -> [Int] {
        let cam = cameraDirection(yaw: yaw)
        let middle = SIMD2((bounds.minX + bounds.maxX) / 2, (bounds.minY + bounds.maxY) / 2)
        return walls.filter { wall in
            let facing = simd_dot(wall.across, cam)
            return simd_dot(wall.center - middle, cam) > 0 && abs(facing) >= Theme.Scene.cutawayFacing
                && !(facing > 0 ? wall.floorAcross : wall.floorOpposite)
        }
        .map(\.index)
        .sorted()
    }

    /// What becomes of each shape with `lowered` walls drawn low (one treatment per shape, in order).
    static func treatments(_ shapes: [ExtensionSceneShape], walls: [Wall], lowered: [Int], yaw: Double) -> [Treatment] {
        var out = [Treatment](repeating: .keep, count: shapes.count)
        guard !lowered.isEmpty else { return out }
        let cam = cameraDirection(yaw: yaw)
        let low = walls.filter { lowered.contains($0.index) }
        for wall in low {
            out[wall.index] = .clip(top: cutTop(wall))
        }
        for (index, shape) in shapes.enumerated() where out[index] == .keep {
            guard let footprint = footprint(shape) else { continue }
            if case .box(let box) = shape, box.wall == true { continue }
            for wall in low {
                let rel = footprint.center - wall.center
                let along = abs(simd_dot(rel, wall.along))
                let across = simd_dot(rel, wall.across)
                let cut = cutTop(wall)
                // standing in the wall: a window, a frame, curtains, a wall-hung unit
                if along <= wall.halfLength + Theme.Scene.cutawayBand, abs(across) <= wall.halfThickness + Theme.Scene.cutawayBand {
                    out[index] = footprint.bottom >= cut ? .hide : (footprint.top > cut ? .clip(top: cut) : .keep)
                    break
                }
                // tall, in front of it on the room's side (away from the camera)
                let roomSide = simd_dot(wall.across, cam) > 0 ? -1.0 : 1.0
                let depth = across * roomSide
                if footprint.top > cut + Theme.Scene.cutawaySeeThroughAbove, along <= wall.halfLength,
                   depth > 0, depth - footprint.halfDepth(across: wall.across) <= wall.halfThickness + Theme.Scene.cutawayReach {
                    out[index] = .seeThrough
                    break
                }
            }
        }
        return out
    }

    /// The height a lowered wall is drawn to.
    static func cutTop(_ wall: Wall) -> Double {
        min(wall.top, wall.bottom + Theme.Scene.cutawayHeight)
    }

    struct Footprint {
        var center: SIMD2<Double>
        var bottom: Double
        var top: Double
        /// Half its extent on the plan, along x and y.
        var halfX: Double
        var halfY: Double

        /// How far it reaches across a wall from its middle.
        func halfDepth(across: SIMD2<Double>) -> Double {
            abs(across.x) * halfX + abs(across.y) * halfY
        }
    }

    /// A box's or a cylinder's middle, heights and extent on the plan; nil for a prism (a floor) and anything unread.
    static func footprint(_ shape: ExtensionSceneShape) -> Footprint? {
        switch shape {
        case .box(let b):
            guard b.center.count >= 2, b.size.count >= 2 else { return nil }
            let yaw = (b.yaw ?? 0) * .pi / 180
            let c = abs(cos(yaw)), s = abs(sin(yaw))
            return Footprint(center: SIMD2(b.center[0], b.center[1]), bottom: min(b.bottom, b.top), top: max(b.bottom, b.top),
                             halfX: (b.size[0] * c + b.size[1] * s) / 2, halfY: (b.size[0] * s + b.size[1] * c) / 2)
        case .cylinder(let c):
            guard c.center.count >= 2 else { return nil }
            return Footprint(center: SIMD2(c.center[0], c.center[1]), bottom: min(c.bottom, c.top), top: max(c.bottom, c.top),
                             halfX: c.radius, halfY: c.radius)
        case .prism, .unknown:
            return nil
        }
    }

    /// The shapes as drawn with the cutaway: walls and what stands in them cut, what was left out gone, and the indices
    /// to draw see-through.
    static func apply(_ shapes: [ExtensionSceneShape], treatments: [Treatment]) -> (shapes: [ExtensionSceneShape], seeThrough: Set<Int>) {
        guard treatments.count == shapes.count else { return (shapes, []) }
        var out: [ExtensionSceneShape] = []
        var faded: Set<Int> = []
        out.reserveCapacity(shapes.count)
        for (shape, treatment) in zip(shapes, treatments) {
            switch treatment {
            case .keep:
                out.append(shape)
            case .hide:
                continue
            case .seeThrough:
                faded.insert(out.count)
                out.append(shape)
            case .clip(let top):
                switch shape {
                case .box(var b):
                    if b.top >= b.bottom { b.top = max(b.bottom + 0.005, min(b.top, top)) } else { b.bottom = max(b.top + 0.005, min(b.bottom, top)) }
                    out.append(.box(b))
                case .cylinder(var c):
                    if c.top >= c.bottom { c.top = max(c.bottom + 0.005, min(c.top, top)) } else { c.bottom = max(c.top + 0.005, min(c.bottom, top)) }
                    out.append(.cylinder(c))
                case .prism, .unknown:
                    out.append(shape)
                }
            }
        }
        return (out, faded)
    }
}
#endif
