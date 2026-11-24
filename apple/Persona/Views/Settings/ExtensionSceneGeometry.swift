#if os(iOS)
import CoreGraphics
import Foundation
import Metal
import PersonaWire
import SceneKit
import UIKit
import simd

/// Axis-aligned bounding box of a scene's shapes in plan `[x, y]` (east, north) and height `h` (up from floor).
/// Pins and labels are excluded so adding or moving a device pin never shifts the model's centre or zoom.
nonisolated struct ExtensionSceneBounds: Equatable, Sendable {
    var minX: Double
    var maxX: Double
    var minY: Double
    var maxY: Double
    var minH: Double
    var maxH: Double

    /// SceneKit right-handed world centre `(x, h, -y)`.
    var worldTarget: SIMD3<Double> {
        SIMD3((minX + maxX) * 0.5, (minH + maxH) * 0.5, -(minY + maxY) * 0.5)
    }

    var radius: Double {
        let dx = maxX - minX
        let dy = maxY - minY
        let dh = maxH - minH
        return max(0.25, 0.5 * sqrt(dx * dx + dy * dy + dh * dh))
    }

    /// Half the plan's diagonal: the farthest the floor reaches sideways from the middle, whichever way it is turned.
    var planHalfDiagonal: Double {
        max(0.25, 0.5 * sqrt((maxX - minX) * (maxX - minX) + (maxY - minY) * (maxY - minY)))
    }

    /// The half height, in metres, of a view that holds the whole model at this pitch and viewport shape, for every
    /// yaw: turning the model never changes its size on screen, and a flat home fills the frame instead of the
    /// sphere around it.
    func fitHalfHeight(pitchRadians phi: Double, aspect: Double) -> Double {
        let halfU = planHalfDiagonal
        let halfV = planHalfDiagonal * abs(sin(phi)) + 0.5 * (maxH - minH) * abs(cos(phi))
        return max(halfV, halfU / max(aspect, 0.1)) * Theme.Scene.fitMargin
    }

    static let fallback = ExtensionSceneBounds(minX: -0.5, maxX: 0.5, minY: -0.5, maxY: 0.5, minH: 0, maxH: 1)

    static func of(_ shapes: [ExtensionSceneShape]) -> ExtensionSceneBounds {
        var minX = Double.infinity, maxX = -Double.infinity
        var minY = Double.infinity, maxY = -Double.infinity
        var minH = Double.infinity, maxH = -Double.infinity

        func include(x: Double, y: Double, bottom: Double, top: Double) {
            guard x.isFinite, y.isFinite, bottom.isFinite, top.isFinite else { return }
            minX = min(minX, x); maxX = max(maxX, x)
            minY = min(minY, y); maxY = max(maxY, y)
            minH = min(minH, min(bottom, top))
            maxH = max(maxH, max(bottom, top))
        }

        for shape in shapes {
            switch shape {
            case .box(let b) where b.center.count >= 2 && b.size.count >= 2:
                let cx = b.center[0], cy = b.center[1]
                let hw = abs(b.size[0]) * 0.5, hd = abs(b.size[1]) * 0.5
                let yaw = (b.yaw ?? 0) * .pi / 180
                let c = abs(cos(yaw)), s = abs(sin(yaw))
                let ex = hw * c + hd * s
                let ey = hw * s + hd * c
                include(x: cx - ex, y: cy - ey, bottom: b.bottom, top: b.top)
                include(x: cx + ex, y: cy + ey, bottom: b.bottom, top: b.top)
            case .cylinder(let c) where c.center.count >= 2:
                let r = abs(c.radius)
                include(x: c.center[0] - r, y: c.center[1] - r, bottom: c.bottom, top: c.top)
                include(x: c.center[0] + r, y: c.center[1] + r, bottom: c.bottom, top: c.top)
            case .prism(let p):
                for pt in p.points where pt.count >= 2 {
                    include(x: pt[0], y: pt[1], bottom: p.bottom, top: p.top)
                }
            case .box, .cylinder, .unknown:
                break
            }
        }
        guard minX.isFinite, maxX.isFinite else { return .fallback }
        if maxX - minX < 0.01 { minX -= 0.05; maxX += 0.05 }
        if maxY - minY < 0.01 { minY -= 0.05; maxY += 0.05 }
        if maxH - minH < 0.005 { maxH = minH + 0.05 }
        return ExtensionSceneBounds(minX: minX, maxX: maxX, minY: minY, maxY: maxY, minH: minH, maxH: maxH)
    }
}

/// Orthographic orbit camera around the shapes' bounding box centre.
nonisolated struct ExtensionSceneCameraState: Codable, Hashable, Sendable {
    var yaw: Double = Theme.Scene.defaultYaw
    var pitch: Double = Theme.Scene.defaultPitch
    var zoom: Double = Theme.Scene.defaultZoom

    static func resolved(_ wire: ExtensionSceneCamera?) -> ExtensionSceneCameraState {
        let yaw = wire?.yaw?.isFinite == true ? wire!.yaw! : Theme.Scene.defaultYaw
        let pitch = wire?.pitch?.isFinite == true ? wire!.pitch! : Theme.Scene.defaultPitch
        let zoom = wire?.zoom?.isFinite == true ? wire!.zoom! : Theme.Scene.defaultZoom
        return ExtensionSceneCameraState(yaw: yaw, pitch: pitch, zoom: zoom).clamped()
    }

    func clamped() -> ExtensionSceneCameraState {
        var wrappedYaw = yaw.truncatingRemainder(dividingBy: 360)
        if wrappedYaw < 0 { wrappedYaw += 360 }
        let clampedPitch = min(max(pitch, Theme.Scene.minPitch), Theme.Scene.maxPitch)
        let clampedZoom = min(max(zoom, Theme.Scene.minZoom), Theme.Scene.maxZoom)
        return ExtensionSceneCameraState(yaw: wrappedYaw, pitch: clampedPitch, zoom: clampedZoom)
    }
}

/// Branch-free analytic orthographic basis matching `SCNCamera` (`projectionDirection = .vertical`), valid up to
/// `pitch = 90` with determinant `+1` and zero gimbal singularity.
nonisolated struct ExtensionSceneProjection: Equatable, Sendable {
    let target: SIMD3<Double>
    let right: SIMD3<Double>
    let camUp: SIMD3<Double>
    let out: SIMD3<Double>
    let cameraPosition: SIMD3<Double>
    let orthographicScale: Double
    let zNear: Double
    let zFar: Double
    let viewport: CGSize

    init(bounds: ExtensionSceneBounds, camera: ExtensionSceneCameraState, viewport: CGSize) {
        let cam = camera.clamped()
        let psi = cam.yaw * .pi / 180
        let phi = cam.pitch * .pi / 180
        let sinPsi = sin(psi), cosPsi = cos(psi)
        let sinPhi = sin(phi), cosPhi = cos(phi)

        target = bounds.worldTarget
        right = SIMD3(-sinPsi, 0, -cosPsi)
        camUp = SIMD3(-sinPhi * cosPsi, cosPhi, sinPhi * sinPsi)
        out = SIMD3(cosPhi * cosPsi, sinPhi, -cosPhi * sinPsi)

        let safeW = max(Double(viewport.width), 1)
        let safeH = max(Double(viewport.height), 1)
        let aspect = safeW / safeH
        orthographicScale = bounds.fitHalfHeight(pitchRadians: phi, aspect: aspect) / max(cam.zoom, 0.05)

        let distance = max(bounds.radius * 3.0, 8.0)
        cameraPosition = target + out * distance
        zNear = max(0.05, distance - bounds.radius * 2.6)
        zFar = distance + bounds.radius * 2.8
        self.viewport = CGSize(width: safeW, height: safeH)
    }

    var simdTransform: simd_float4x4 {
        let r = SIMD4<Float>(Float(right.x), Float(right.y), Float(right.z), 0)
        let u = SIMD4<Float>(Float(camUp.x), Float(camUp.y), Float(camUp.z), 0)
        let o = SIMD4<Float>(Float(out.x), Float(out.y), Float(out.z), 0)
        let p = SIMD4<Float>(Float(cameraPosition.x), Float(cameraPosition.y), Float(cameraPosition.z), 1)
        return simd_float4x4(columns: (r, u, o, p))
    }

    /// A pin's pill around `anchor`, moved just enough to stay `Theme.Scene.pinEdgeInset` inside the viewport (centred
    /// when it is wider than the viewport). A pill whose anchor is off screen (the model zoomed or turned past it) stays
    /// with its anchor, so pins never pile up along an edge.
    func keptInside(_ rect: CGRect, anchor: CGPoint) -> CGRect {
        guard CGRect(origin: .zero, size: viewport).contains(anchor) else { return rect }
        let inset = Theme.Scene.pinEdgeInset
        func fit(_ origin: CGFloat, _ length: CGFloat, _ room: CGFloat) -> CGFloat {
            guard length + 2 * inset <= room else { return (room - length) * 0.5 }
            return min(max(origin, inset), room - inset - length)
        }
        return CGRect(x: fit(rect.minX, rect.width, viewport.width), y: fit(rect.minY, rect.height, viewport.height),
                      width: rect.width, height: rect.height)
    }

    /// Closed-form 2D projection in `viewport` points and camera-forward depth in metres.
    func project(planX: Double, planY: Double, height: Double = 0) -> (point: CGPoint, depth: Double) {
        let world = SIMD3<Double>(planX, height, -planY)
        let rel = world - target
        let u = simd_dot(rel, right)
        let v = simd_dot(rel, camUp)
        let depth = simd_dot(world - cameraPosition, -out)
        let pointsPerMetre = Double(viewport.height) / (2.0 * orthographicScale)
        let sx = Double(viewport.width) * 0.5 + u * pointsPerMetre
        let sy = Double(viewport.height) * 0.5 - v * pointsPerMetre
        return (CGPoint(x: sx, y: sy), depth)
    }
}

/// One material's batched static triangle buffers. Pure value data (`Sendable`) so inline views, full-screen covers,
/// and off-thread snapshots never share mutable `SCNNode` or `SCNScene` instances.
nonisolated struct ExtensionSceneMeshBatch: Equatable, Sendable {
    var material: String
    var vertexCount: Int
    var triangleCount: Int
    var positions: Data
    var normals: Data
    var colors: Data
    var uvs: Data
    var indices: Data
}

nonisolated struct ExtensionScenePreparedGeometry: Equatable, Sendable {
    var bounds: ExtensionSceneBounds
    var floorTop: Double
    var batches: [ExtensionSceneMeshBatch]
    /// The walls drawn low for the cutaway it was built for (shape indices, sorted; #13036).
    var lowered: [Int] = []

    var totalVertices: Int { batches.reduce(0) { $0 + $1.vertexCount } }
    var totalTriangles: Int { batches.reduce(0) { $0 + $1.triangleCount } }
}

nonisolated enum ExtensionSceneGeometryBuilder {
    /// The batch of shapes the cutaway draws see-through (not one of the wire's materials).
    static let seeThrough = "see-through"

    static let materialOrder: [String] = [
        ExtensionSceneMaterial.matte,
        ExtensionSceneMaterial.wood,
        ExtensionSceneMaterial.tile,
        ExtensionSceneMaterial.metal,
        ExtensionSceneMaterial.screen,
        ExtensionSceneMaterial.glass,
        seeThrough,
    ]

    /// Drawn see-through: glass, and what the cutaway shows through (#13036).
    static func isSeeThrough(_ material: String) -> Bool {
        material == ExtensionSceneMaterial.glass || material == seeThrough
    }

    private struct Accumulator {
        var positions: [Float] = []
        var normals: [Float] = []
        var colors: [Float] = []
        var uvs: [Float] = []
        var indices: [UInt32] = []

        var vertexCount: Int { positions.count / 3 }

        mutating func addVertex(pos: SIMD3<Double>, normal: SIMD3<Double>, color: SIMD4<Float>, uv: SIMD2<Double>) -> UInt32 {
            let index = UInt32(vertexCount)
            positions.append(Float(pos.x))
            positions.append(Float(pos.y))
            positions.append(Float(pos.z))
            normals.append(Float(normal.x))
            normals.append(Float(normal.y))
            normals.append(Float(normal.z))
            colors.append(color.x)
            colors.append(color.y)
            colors.append(color.z)
            colors.append(color.w)
            uvs.append(Float(uv.x))
            uvs.append(Float(uv.y))
            return index
        }

        mutating func addTriangle(_ a: UInt32, _ b: UInt32, _ c: UInt32) {
            indices.append(a)
            indices.append(b)
            indices.append(c)
        }

        mutating func addQuad(_ p0: SIMD3<Double>, _ p1: SIMD3<Double>, _ p2: SIMD3<Double>, _ p3: SIMD3<Double>,
                              normal: SIMD3<Double>, color: SIMD4<Float>,
                              uv0: SIMD2<Double>, uv1: SIMD2<Double>, uv2: SIMD2<Double>, uv3: SIMD2<Double>) {
            let i0 = addVertex(pos: p0, normal: normal, color: color, uv: uv0)
            let i1 = addVertex(pos: p1, normal: normal, color: color, uv: uv1)
            let i2 = addVertex(pos: p2, normal: normal, color: color, uv: uv2)
            let i3 = addVertex(pos: p3, normal: normal, color: color, uv: uv3)
            addTriangle(i0, i1, i2)
            addTriangle(i0, i2, i3)
        }

        func freeze(material: String) -> ExtensionSceneMeshBatch? {
            guard !indices.isEmpty else { return nil }
            return ExtensionSceneMeshBatch(
                material: material,
                vertexCount: vertexCount,
                triangleCount: indices.count / 3,
                positions: positions.withUnsafeBufferPointer { Data(buffer: $0) },
                normals: normals.withUnsafeBufferPointer { Data(buffer: $0) },
                colors: colors.withUnsafeBufferPointer { Data(buffer: $0) },
                uvs: uvs.withUnsafeBufferPointer { Data(buffer: $0) },
                indices: indices.withUnsafeBufferPointer { Data(buffer: $0) }
            )
        }
    }

    /// The meshes for `shapes`; with `cutawayYaw`, the cutaway for a camera at that yaw (#13036). The bounds are always
    /// the whole model's, so the framing holds while walls come down.
    static func build(_ shapes: [ExtensionSceneShape], cutawayYaw: Double? = nil) -> ExtensionScenePreparedGeometry {
        let bounds = ExtensionSceneBounds.of(shapes)
        var drawn = shapes
        var faded: Set<Int> = []
        var lowered: [Int] = []
        if let yaw = cutawayYaw {
            let walls = ExtensionSceneCutaway.walls(shapes)
            lowered = ExtensionSceneCutaway.lowered(walls, bounds: bounds, yaw: yaw)
            if !lowered.isEmpty {
                let treatments = ExtensionSceneCutaway.treatments(shapes, walls: walls, lowered: lowered, yaw: yaw)
                (drawn, faded) = ExtensionSceneCutaway.apply(shapes, treatments: treatments)
            }
        }
        var floorTop = bounds.minH
        var buckets: [String: Accumulator] = [:]
        for (index, shape) in drawn.enumerated() {
            let fade = faded.contains(index)
            switch shape {
            case .box(let box):
                let top = max(box.bottom, box.top)
                if top.isFinite, top - bounds.minH <= 0.25 { floorTop = max(floorTop, top) }
                let mat = normalizedMaterial(box.material)
                let col = linearRGBA(hex: box.color, material: mat)
                appendBox(box, color: col, into: &buckets[fade ? seeThrough : mat, default: Accumulator()])
            case .cylinder(let cyl):
                let top = max(cyl.bottom, cyl.top)
                if top.isFinite, top - bounds.minH <= 0.25 { floorTop = max(floorTop, top) }
                let mat = normalizedMaterial(cyl.material)
                let col = linearRGBA(hex: cyl.color, material: mat)
                appendCylinder(cyl, color: col, into: &buckets[fade ? seeThrough : mat, default: Accumulator()])
            case .prism(let prism):
                let top = max(prism.bottom, prism.top)
                if top.isFinite, top - bounds.minH <= 0.25 { floorTop = max(floorTop, top) }
                let mat = normalizedMaterial(prism.material)
                let col = linearRGBA(hex: prism.color, material: mat)
                appendPrism(prism, color: col, into: &buckets[mat, default: Accumulator()])
            case .unknown:
                break
            }
        }
        let batches = materialOrder.compactMap { buckets[$0]?.freeze(material: $0) }
        return ExtensionScenePreparedGeometry(bounds: bounds, floorTop: floorTop, batches: batches, lowered: lowered)
    }

    static func normalizedMaterial(_ raw: String?) -> String {
        guard let raw, ExtensionSceneMaterial.all.contains(raw) else { return ExtensionSceneMaterial.matte }
        return raw
    }

    static func srgbToLinear(_ c: Double) -> Float {
        let clamped = min(max(c, 0), 1)
        if clamped <= 0.04045 { return Float(clamped / 12.92) }
        return Float(pow((clamped + 0.055) / 1.055, 2.4))
    }

    static func defaultHex(for material: String) -> UInt32 {
        switch material {
        case ExtensionSceneMaterial.wood: Theme.Scene.woodHex
        case ExtensionSceneMaterial.tile: Theme.Scene.tileHex
        case ExtensionSceneMaterial.metal: Theme.Scene.metalHex
        case ExtensionSceneMaterial.screen: Theme.Scene.screenHex
        case ExtensionSceneMaterial.glass: Theme.Scene.glassHex
        default: Theme.Scene.matteHex
        }
    }

    static func parseRGB(hex: String?, material: String) -> (Double, Double, Double) {
        if let hex, hex.count == 7, hex.first == "#", let value = UInt32(hex.dropFirst(), radix: 16) {
            return unpackHex(value)
        }
        return unpackHex(defaultHex(for: material))
    }

    static func unpackHex(_ value: UInt32) -> (Double, Double, Double) {
        let r = Double((value >> 16) & 255) / 255.0
        let g = Double((value >> 8) & 255) / 255.0
        let b = Double(value & 255) / 255.0
        return (r, g, b)
    }

    static func linearRGBA(hex: String?, material: String) -> SIMD4<Float> {
        let (r, g, b) = parseRGB(hex: hex, material: material)
        return SIMD4(srgbToLinear(r), srgbToLinear(g), srgbToLinear(b), 1.0)
    }

    // MARK: - Primitive Mesh Builders

    private static func appendBox(_ box: ExtensionSceneBox, color: SIMD4<Float>, into acc: inout Accumulator) {
        guard box.center.count >= 2, box.size.count >= 2 else { return }
        let w = max(box.size[0], 0.005)
        let d = max(box.size[1], 0.005)
        let b0 = min(box.bottom, box.top)
        let t0 = max(box.bottom, box.top)
        let h = max(t0 - b0, 0.005)
        let top = b0 + h
        let rPlan = min(max(box.round ?? 0, 0), min(w, d) * 0.5)
        let bevel = min(rPlan, h * 0.42)

        let cx = box.center[0], cy = box.center[1]
        let yaw = (box.yaw ?? 0) * .pi / 180
        let cosY = cos(yaw), sinY = sin(yaw)

        func toWorldPlan(_ lx: Double, _ ly: Double) -> SIMD2<Double> {
            SIMD2(cx + lx * cosY - ly * sinY, cy + lx * sinY + ly * cosY)
        }

        func toWorldDir(_ lx: Double, _ ly: Double) -> SIMD2<Double> {
            SIMD2(lx * cosY - ly * sinY, lx * sinY + ly * cosY)
        }

        let localRing = roundedRectLoop(width: w, depth: d, radius: rPlan)
        let n = localRing.count
        guard n >= 3 else { return }

        struct RingSample {
            var outerPlan: SIMD2<Double>
            var innerPlan: SIMD2<Double>
            var outNormal: SIMD3<Double>
            var arcS: Double
        }

        var samples: [RingSample] = []
        samples.reserveCapacity(n)
        var arc = 0.0
        for i in 0..<n {
            let p = localRing[i]
            if i > 0 {
                let prev = localRing[i - 1].point
                arc += hypot(p.point.x - prev.x, p.point.y - prev.y)
            }
            let inset = min(bevel * 0.65, min(w, d) * 0.45)
            let innerLocal = p.point - p.normal * inset
            let wp = toWorldPlan(p.point.x, p.point.y)
            let wi = toWorldPlan(innerLocal.x, innerLocal.y)
            let wn = toWorldDir(p.normal.x, p.normal.y)
            let len = max(hypot(wn.x, wn.y), 1e-9)
            samples.append(RingSample(
                outerPlan: wp,
                innerPlan: wi,
                outNormal: SIMD3(wn.x / len, 0, -wn.y / len),
                arcS: arc
            ))
        }

        if bevel > 0.003 {
            let zBotBevel = b0 + bevel
            let zTopBevel = top - bevel
            for i in 0..<n {
                let j = (i + 1) % n
                let a = samples[i], b = samples[j]
                let sNext = j == 0 ? arc + hypot(b.outerPlan.x - a.outerPlan.x, b.outerPlan.y - a.outerPlan.y) : b.arcS
                let nSide = rPlan > 0.002 ? a.outNormal : flatWallNormal(from: a.outerPlan, to: b.outerPlan)
                let nSideB = rPlan > 0.002 ? b.outNormal : nSide

                if zTopBevel - zBotBevel > 1e-4 {
                    let v0 = acc.addVertex(pos: SIMD3(a.outerPlan.x, zBotBevel, -a.outerPlan.y), normal: nSide, color: color, uv: SIMD2(a.arcS, zBotBevel))
                    let v1 = acc.addVertex(pos: SIMD3(b.outerPlan.x, zBotBevel, -b.outerPlan.y), normal: nSideB, color: color, uv: SIMD2(sNext, zBotBevel))
                    let v2 = acc.addVertex(pos: SIMD3(b.outerPlan.x, zTopBevel, -b.outerPlan.y), normal: nSideB, color: color, uv: SIMD2(sNext, zTopBevel))
                    let v3 = acc.addVertex(pos: SIMD3(a.outerPlan.x, zTopBevel, -a.outerPlan.y), normal: nSide, color: color, uv: SIMD2(a.arcS, zTopBevel))
                    acc.addTriangle(v0, v1, v2)
                    acc.addTriangle(v0, v2, v3)
                }

                let topN0 = simd_normalize(nSide + SIMD3(0, 1, 0))
                let topN1 = simd_normalize(nSideB + SIMD3(0, 1, 0))
                let tv0 = acc.addVertex(pos: SIMD3(a.outerPlan.x, zTopBevel, -a.outerPlan.y), normal: topN0, color: color, uv: a.outerPlan)
                let tv1 = acc.addVertex(pos: SIMD3(b.outerPlan.x, zTopBevel, -b.outerPlan.y), normal: topN1, color: color, uv: b.outerPlan)
                let tv2 = acc.addVertex(pos: SIMD3(b.innerPlan.x, top, -b.innerPlan.y), normal: topN1, color: color, uv: b.innerPlan)
                let tv3 = acc.addVertex(pos: SIMD3(a.innerPlan.x, top, -a.innerPlan.y), normal: topN0, color: color, uv: a.innerPlan)
                acc.addTriangle(tv0, tv1, tv2)
                acc.addTriangle(tv0, tv2, tv3)

                let botN0 = simd_normalize(nSide + SIMD3(0, -1, 0))
                let botN1 = simd_normalize(nSideB + SIMD3(0, -1, 0))
                let bv0 = acc.addVertex(pos: SIMD3(b.outerPlan.x, zBotBevel, -b.outerPlan.y), normal: botN1, color: color, uv: b.outerPlan)
                let bv1 = acc.addVertex(pos: SIMD3(a.outerPlan.x, zBotBevel, -a.outerPlan.y), normal: botN0, color: color, uv: a.outerPlan)
                let bv2 = acc.addVertex(pos: SIMD3(a.innerPlan.x, b0, -a.innerPlan.y), normal: botN0, color: color, uv: a.innerPlan)
                let bv3 = acc.addVertex(pos: SIMD3(b.innerPlan.x, b0, -b.innerPlan.y), normal: botN1, color: color, uv: b.innerPlan)
                acc.addTriangle(bv0, bv1, bv2)
                acc.addTriangle(bv0, bv2, bv3)
            }
            appendConvexCap(points: samples.map(\.innerPlan), height: top, up: true, color: color, into: &acc)
            appendConvexCap(points: samples.map(\.innerPlan), height: b0, up: false, color: color, into: &acc)
        } else {
            for i in 0..<n {
                let j = (i + 1) % n
                let a = samples[i], b = samples[j]
                let sNext = j == 0 ? arc + hypot(b.outerPlan.x - a.outerPlan.x, b.outerPlan.y - a.outerPlan.y) : b.arcS
                let nSide = rPlan > 0.002 ? a.outNormal : flatWallNormal(from: a.outerPlan, to: b.outerPlan)
                let nSideB = rPlan > 0.002 ? b.outNormal : nSide
                let v0 = acc.addVertex(pos: SIMD3(a.outerPlan.x, b0, -a.outerPlan.y), normal: nSide, color: color, uv: SIMD2(a.arcS, b0))
                let v1 = acc.addVertex(pos: SIMD3(b.outerPlan.x, b0, -b.outerPlan.y), normal: nSideB, color: color, uv: SIMD2(sNext, b0))
                let v2 = acc.addVertex(pos: SIMD3(b.outerPlan.x, top, -b.outerPlan.y), normal: nSideB, color: color, uv: SIMD2(sNext, top))
                let v3 = acc.addVertex(pos: SIMD3(a.outerPlan.x, top, -a.outerPlan.y), normal: nSide, color: color, uv: SIMD2(a.arcS, top))
                acc.addTriangle(v0, v1, v2)
                acc.addTriangle(v0, v2, v3)
            }
            appendConvexCap(points: samples.map(\.outerPlan), height: top, up: true, color: color, into: &acc)
            appendConvexCap(points: samples.map(\.outerPlan), height: b0, up: false, color: color, into: &acc)
        }
    }

    private struct LoopSample {
        var point: SIMD2<Double>
        var normal: SIMD2<Double>
    }

    private static func roundedRectLoop(width: Double, depth: Double, radius: Double) -> [LoopSample] {
        let hw = width * 0.5, hd = depth * 0.5
        if radius <= 0.002 {
            return [
                LoopSample(point: SIMD2(-hw, -hd), normal: simd_normalize(SIMD2(-1, -1))),
                LoopSample(point: SIMD2(hw, -hd), normal: simd_normalize(SIMD2(1, -1))),
                LoopSample(point: SIMD2(hw, hd), normal: simd_normalize(SIMD2(1, 1))),
                LoopSample(point: SIMD2(-hw, hd), normal: simd_normalize(SIMD2(-1, 1))),
            ]
        }
        let r = min(radius, min(hw, hd))
        let ix = hw - r, iy = hd - r
        let corners: [(center: SIMD2<Double>, startAngle: Double)] = [
            (SIMD2(ix, -iy), -.pi * 0.5),
            (SIMD2(ix, iy), 0),
            (SIMD2(-ix, iy), .pi * 0.5),
            (SIMD2(-ix, -iy), .pi),
        ]
        let segs = 4
        var out: [LoopSample] = []
        out.reserveCapacity(corners.count * (segs + 1))
        for corner in corners {
            for s in 0...segs {
                let angle = corner.startAngle + Double(s) / Double(segs) * (.pi * 0.5)
                let dir = SIMD2(cos(angle), sin(angle))
                let candidate = corner.center + dir * r
                if let last = out.last, hypot(candidate.x - last.point.x, candidate.y - last.point.y) < 1e-5 {
                    continue
                }
                out.append(LoopSample(point: candidate, normal: dir))
            }
        }
        if out.count > 2, let first = out.first, let last = out.last,
           hypot(first.point.x - last.point.x, first.point.y - last.point.y) < 1e-5 {
            out.removeLast()
        }
        return out
    }

    private static func appendConvexCap(points: [SIMD2<Double>], height: Double, up: Bool,
                                        color: SIMD4<Float>, into acc: inout Accumulator) {
        guard points.count >= 3 else { return }
        let normal = SIMD3<Double>(0, up ? 1 : -1, 0)
        var center = SIMD2<Double>.zero
        for p in points { center += p }
        center /= Double(points.count)
        let centerIdx = acc.addVertex(pos: SIMD3(center.x, height, -center.y), normal: normal, color: color, uv: center)
        var ringIdx: [UInt32] = []
        ringIdx.reserveCapacity(points.count)
        for p in points {
            ringIdx.append(acc.addVertex(pos: SIMD3(p.x, height, -p.y), normal: normal, color: color, uv: p))
        }
        for i in 0..<ringIdx.count {
            let j = (i + 1) % ringIdx.count
            if up {
                acc.addTriangle(centerIdx, ringIdx[i], ringIdx[j])
            } else {
                acc.addTriangle(centerIdx, ringIdx[j], ringIdx[i])
            }
        }
    }

    private static func appendCylinder(_ cyl: ExtensionSceneCylinder, color: SIMD4<Float>, into acc: inout Accumulator) {
        guard cyl.center.count >= 2 else { return }
        let cx = cyl.center[0], cy = cyl.center[1]
        let r = max(cyl.radius, 0.005)
        let b0 = min(cyl.bottom, cyl.top)
        let top = max(b0 + 0.005, max(cyl.bottom, cyl.top))
        let segments = 28
        var planPoints: [SIMD2<Double>] = []
        planPoints.reserveCapacity(segments)

        for i in 0..<segments {
            let a0 = Double(i) / Double(segments) * 2.0 * .pi
            let a1 = Double(i + 1) / Double(segments) * 2.0 * .pi
            let c0 = cos(a0), s0 = sin(a0)
            let c1 = cos(a1), s1 = sin(a1)
            let p0 = SIMD2(cx + r * c0, cy + r * s0)
            let p1 = SIMD2(cx + r * c1, cy + r * s1)
            planPoints.append(p0)
            let n0 = SIMD3(c0, 0, -s0)
            let n1 = SIMD3(c1, 0, -s1)
            let u0 = Double(i) / Double(segments) * (2.0 * .pi * r)
            let u1 = Double(i + 1) / Double(segments) * (2.0 * .pi * r)
            let v0 = acc.addVertex(pos: SIMD3(p0.x, b0, -p0.y), normal: n0, color: color, uv: SIMD2(u0, b0))
            let v1 = acc.addVertex(pos: SIMD3(p1.x, b0, -p1.y), normal: n1, color: color, uv: SIMD2(u1, b0))
            let v2 = acc.addVertex(pos: SIMD3(p1.x, top, -p1.y), normal: n1, color: color, uv: SIMD2(u1, top))
            let v3 = acc.addVertex(pos: SIMD3(p0.x, top, -p0.y), normal: n0, color: color, uv: SIMD2(u0, top))
            acc.addTriangle(v0, v1, v2)
            acc.addTriangle(v0, v2, v3)
        }
        appendConvexCap(points: planPoints, height: top, up: true, color: color, into: &acc)
        appendConvexCap(points: planPoints, height: b0, up: false, color: color, into: &acc)
    }

    private static func appendPrism(_ prism: ExtensionScenePrism, color: SIMD4<Float>, into acc: inout Accumulator) {
        let raw = prism.points.compactMap { pt -> SIMD2<Double>? in
            guard pt.count >= 2, pt[0].isFinite, pt[1].isFinite else { return nil }
            return SIMD2(pt[0], pt[1])
        }
        var poly = normalizedPolygonCCW(raw)
        poly = pruneCollinearVertices(poly)
        guard poly.count >= 3 else { return }

        let b0 = min(prism.bottom, prism.top)
        let top = max(b0 + 0.005, max(prism.bottom, prism.top))

        var arc = 0.0
        for i in 0..<poly.count {
            let p0 = poly[i]
            let p1 = poly[(i + 1) % poly.count]
            let segLen = hypot(p1.x - p0.x, p1.y - p0.y)
            guard segLen > 1e-6 else { continue }
            let nSide = flatWallNormal(from: p0, to: p1)
            acc.addQuad(
                SIMD3(p0.x, b0, -p0.y),
                SIMD3(p1.x, b0, -p1.y),
                SIMD3(p1.x, top, -p1.y),
                SIMD3(p0.x, top, -p0.y),
                normal: nSide,
                color: color,
                uv0: SIMD2(arc, b0),
                uv1: SIMD2(arc + segLen, b0),
                uv2: SIMD2(arc + segLen, top),
                uv3: SIMD2(arc, top)
            )
            arc += segLen
        }

        let triangles = triangulateSimplePolygonCCW(poly)
        let topNormal = SIMD3<Double>(0, 1, 0)
        let botNormal = SIMD3<Double>(0, -1, 0)
        var topIndices: [UInt32] = []
        var botIndices: [UInt32] = []
        topIndices.reserveCapacity(poly.count)
        botIndices.reserveCapacity(poly.count)
        for p in poly {
            topIndices.append(acc.addVertex(pos: SIMD3(p.x, top, -p.y), normal: topNormal, color: color, uv: p))
            botIndices.append(acc.addVertex(pos: SIMD3(p.x, b0, -p.y), normal: botNormal, color: color, uv: p))
        }
        for (a, b, c) in triangles {
            acc.addTriangle(topIndices[a], topIndices[b], topIndices[c])
            acc.addTriangle(botIndices[a], botIndices[c], botIndices[b])
        }
    }

    private static func flatWallNormal(from a: SIMD2<Double>, to b: SIMD2<Double>) -> SIMD3<Double> {
        let dx = b.x - a.x, dy = b.y - a.y
        let len = max(hypot(dx, dy), 1e-9)
        let nx = dy / len
        let ny = -dx / len
        return SIMD3(nx, 0, -ny)
    }

    // MARK: - Double-Precision Polygon Triangulation

    private static func normalizedPolygonCCW(_ points: [SIMD2<Double>]) -> [SIMD2<Double>] {
        guard points.count >= 3 else { return [] }
        var area2 = 0.0
        for i in 0..<points.count {
            let a = points[i], b = points[(i + 1) % points.count]
            area2 += a.x * b.y - b.x * a.y
        }
        return area2 < 0 ? points.reversed() : points
    }

    private static func pruneCollinearVertices(_ points: [SIMD2<Double>]) -> [SIMD2<Double>] {
        var pts = points
        var changed = true
        while changed && pts.count > 3 {
            changed = false
            var next: [SIMD2<Double>] = []
            next.reserveCapacity(pts.count)
            let n = pts.count
            for i in 0..<n {
                let a = pts[(i + n - 1) % n]
                let b = pts[i]
                let c = pts[(i + 1) % n]
                let u = b - a, v = c - b
                let lu = hypot(u.x, u.y), lv = hypot(v.x, v.y)
                if lu < 1e-6 {
                    changed = true
                    continue
                }
                let cross = u.x * v.y - u.y * v.x
                let dot = u.x * v.x + u.y * v.y
                if abs(cross) <= 1e-8 * lu * max(lv, 1e-6) && dot >= 0 {
                    changed = true
                    continue
                }
                next.append(b)
            }
            pts = next
        }
        return pts
    }

    static func triangulateSimplePolygonCCW(_ poly: [SIMD2<Double>]) -> [(Int, Int, Int)] {
        let n = poly.count
        guard n >= 3 else { return [] }
        if n == 3 { return [(0, 1, 2)] }

        var remaining = Array(0..<n)
        var out: [(Int, Int, Int)] = []
        out.reserveCapacity(n - 2)

        func cross2D(_ a: SIMD2<Double>, _ b: SIMD2<Double>, _ c: SIMD2<Double>) -> Double {
            (b.x - a.x) * (c.y - a.y) - (b.y - a.y) * (c.x - a.x)
        }

        func pointInTriangle(_ p: SIMD2<Double>, _ a: SIMD2<Double>, _ b: SIMD2<Double>, _ c: SIMD2<Double>) -> Bool {
            let eps = -1e-9
            return cross2D(a, b, p) >= eps && cross2D(b, c, p) >= eps && cross2D(c, a, p) >= eps
        }

        var guardSteps = n * n * 2
        while remaining.count > 3 && guardSteps > 0 {
            guardSteps -= 1
            var clipped = false
            let m = remaining.count
            for pass in 0..<2 where !clipped {
                let minCross = pass == 0 ? 1e-10 : -1e-9
                for k in 0..<m {
                    let ia = remaining[(k + m - 1) % m]
                    let ib = remaining[k]
                    let ic = remaining[(k + 1) % m]
                    let a = poly[ia], b = poly[ib], c = poly[ic]
                    if cross2D(a, b, c) <= minCross { continue }
                    var containsOther = false
                    for other in remaining where other != ia && other != ib && other != ic {
                        if pointInTriangle(poly[other], a, b, c) {
                            containsOther = true
                            break
                        }
                    }
                    if !containsOther {
                        out.append((ia, ib, ic))
                        remaining.remove(at: k)
                        clipped = true
                        break
                    }
                }
            }
            if !clipped {
                out.append((remaining[0], remaining[1], remaining[2]))
                remaining.remove(at: 1)
            }
        }
        if remaining.count == 3 {
            out.append((remaining[0], remaining[1], remaining[2]))
        }
        return out
    }

    // MARK: - SceneKit Assembly & Snapshot

    private static let woodPattern: CGImage? = makeWoodPattern()
    private static let tilePattern: CGImage? = makeTilePattern()

    private static func makeWoodPattern() -> CGImage? {
        let size = 128
        guard let space = CGColorSpace(name: CGColorSpace.sRGB),
              let ctx = CGContext(data: nil, width: size, height: size, bitsPerComponent: 8, bytesPerRow: size * 4,
                                  space: space, bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue) else { return nil }
        ctx.setFillColor(red: 0.99, green: 0.98, blue: 0.96, alpha: 1)
        ctx.fill(CGRect(x: 0, y: 0, width: size, height: size))
        let planks = 4
        let step = CGFloat(size) / CGFloat(planks)
        for i in 0..<planks {
            let y = CGFloat(i) * step
            let shade: CGFloat = (i % 2 == 0) ? 0.97 : 1.0
            ctx.setFillColor(red: shade, green: shade * 0.99, blue: shade * 0.97, alpha: 1)
            ctx.fill(CGRect(x: 0, y: y, width: CGFloat(size), height: step))
            ctx.setFillColor(red: 0.86, green: 0.83, blue: 0.79, alpha: 1)
            ctx.fill(CGRect(x: 0, y: y, width: CGFloat(size), height: 1.5))
            let seamX = CGFloat((i * 47 + 23) % size)
            ctx.fill(CGRect(x: seamX, y: y, width: 1.5, height: step))
        }
        return ctx.makeImage()
    }

    private static func makeTilePattern() -> CGImage? {
        let size = 96
        guard let space = CGColorSpace(name: CGColorSpace.sRGB),
              let ctx = CGContext(data: nil, width: size, height: size, bitsPerComponent: 8, bytesPerRow: size * 4,
                                  space: space, bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue) else { return nil }
        ctx.setFillColor(red: 0.99, green: 0.99, blue: 0.99, alpha: 1)
        ctx.fill(CGRect(x: 0, y: 0, width: size, height: size))
        ctx.setFillColor(red: 0.88, green: 0.87, blue: 0.85, alpha: 1)
        ctx.fill(CGRect(x: 0, y: 0, width: CGFloat(size), height: 2))
        ctx.fill(CGRect(x: 0, y: 0, width: 2, height: CGFloat(size)))
        ctx.fill(CGRect(x: 0, y: CGFloat(size / 2), width: CGFloat(size), height: 1.5))
        ctx.fill(CGRect(x: CGFloat(size / 2), y: 0, width: 1.5, height: CGFloat(size)))
        return ctx.makeImage()
    }

    static func makeMaterial(for kind: String) -> SCNMaterial {
        let mat = SCNMaterial()
        mat.lightingModel = .physicallyBased
        mat.diffuse.contents = UIColor.white
        switch kind {
        case ExtensionSceneMaterial.wood:
            if let cg = woodPattern {
                mat.diffuse.contents = cg
                mat.diffuse.wrapS = .repeat
                mat.diffuse.wrapT = .repeat
                mat.diffuse.contentsTransform = SCNMatrix4MakeScale(0.85, 0.85, 1)
            }
            mat.roughness.contents = 0.72
            mat.metalness.contents = 0.0
        case ExtensionSceneMaterial.tile:
            if let cg = tilePattern {
                mat.diffuse.contents = cg
                mat.diffuse.wrapS = .repeat
                mat.diffuse.wrapT = .repeat
                mat.diffuse.contentsTransform = SCNMatrix4MakeScale(0.85, 0.85, 1)
            }
            mat.roughness.contents = 0.45
            mat.metalness.contents = 0.02
        case ExtensionSceneMaterial.metal:
            mat.roughness.contents = 0.34
            mat.metalness.contents = 0.62
        case ExtensionSceneMaterial.screen:
            mat.roughness.contents = 0.18
            mat.metalness.contents = 0.08
        case seeThrough:
            // the cutaway's see-through furniture: its own colors, faint, never hiding what is behind it
            mat.roughness.contents = 0.84
            mat.metalness.contents = 0.0
            seeThroughSurface(mat, opacity: Theme.Scene.cutawaySeeThroughOpacity)
        case ExtensionSceneMaterial.glass:
            mat.roughness.contents = 0.08
            mat.metalness.contents = 0.05
            seeThroughSurface(mat, opacity: Theme.Scene.glassOpacity)
        default:
            mat.roughness.contents = 0.84
            mat.metalness.contents = 0.0
        }
        return mat
    }

    /// A see-through surface at `opacity`. A physically based material ignores `transparency` (it drew glass solid, #13036):
    /// the opacity goes in its transparent property, read from alpha (`.aOne`).
    static func seeThroughSurface(_ mat: SCNMaterial, opacity: CGFloat) {
        mat.transparent.contents = UIColor(white: 0, alpha: opacity)
        mat.transparencyMode = .aOne
        mat.blendMode = .alpha
        mat.writesToDepthBuffer = false
        mat.isDoubleSided = true
    }

    static func makeSCNGeometry(from batch: ExtensionSceneMeshBatch) -> SCNGeometry {
        let posSrc = SCNGeometrySource(
            data: batch.positions, semantic: .vertex, vectorCount: batch.vertexCount,
            usesFloatComponents: true, componentsPerVector: 3, bytesPerComponent: 4, dataOffset: 0, dataStride: 12)
        let normSrc = SCNGeometrySource(
            data: batch.normals, semantic: .normal, vectorCount: batch.vertexCount,
            usesFloatComponents: true, componentsPerVector: 3, bytesPerComponent: 4, dataOffset: 0, dataStride: 12)
        let colSrc = SCNGeometrySource(
            data: batch.colors, semantic: .color, vectorCount: batch.vertexCount,
            usesFloatComponents: true, componentsPerVector: 4, bytesPerComponent: 4, dataOffset: 0, dataStride: 16)
        let uvSrc = SCNGeometrySource(
            data: batch.uvs, semantic: .texcoord, vectorCount: batch.vertexCount,
            usesFloatComponents: true, componentsPerVector: 2, bytesPerComponent: 4, dataOffset: 0, dataStride: 8)
        let element = SCNGeometryElement(
            data: batch.indices, primitiveType: .triangles, primitiveCount: batch.triangleCount, bytesPerIndex: 4)
        let geometry = SCNGeometry(sources: [posSrc, normSrc, colSrc, uvSrc], elements: [element])
        geometry.materials = [makeMaterial(for: batch.material)]
        return geometry
    }

    static func uiColor(hex: UInt32, alpha: CGFloat = 1) -> UIColor {
        let (r, g, b) = unpackHex(hex)
        return UIColor(red: r, green: g, blue: b, alpha: alpha)
    }

    static func resolvedBackgroundColor(dark: Bool) -> UIColor {
        let traits = UITraitCollection(userInterfaceStyle: dark ? .dark : .light)
        return UIColor(Theme.Scene.ground).resolvedColor(with: traits)
    }

    /// One complete SceneKit rig owned by a single `SCNView` or snapshot pass.
    struct Rig {
        let scene: SCNScene
        let meshRoot: SCNNode
        let cameraNode: SCNNode
        let ambientLight: SCNLight
        let mainLightNode: SCNNode
        let mainLight: SCNLight
        let fillLight: SCNLight
        let lampNodes: [SCNNode]
        let glowRoot: SCNNode
    }

    static func makeRig() -> Rig {
        let scene = SCNScene()
        let meshRoot = SCNNode()
        scene.rootNode.addChildNode(meshRoot)

        let camera = SCNCamera()
        camera.usesOrthographicProjection = true
        camera.projectionDirection = .vertical
        let cameraNode = SCNNode()
        cameraNode.camera = camera
        scene.rootNode.addChildNode(cameraNode)

        let ambientLight = SCNLight()
        ambientLight.type = .ambient
        let ambientNode = SCNNode()
        ambientNode.light = ambientLight
        scene.rootNode.addChildNode(ambientNode)

        let mainLight = SCNLight()
        mainLight.type = .directional
        mainLight.color = uiColor(hex: Theme.Scene.sunLightHex)
        mainLight.castsShadow = true
        mainLight.shadowMode = .forward
        mainLight.automaticallyAdjustsShadowProjection = false
        mainLight.shadowSampleCount = 4
        mainLight.shadowRadius = 2.5
        mainLight.shadowMapSize = CGSize(width: 1024, height: 1024)
        let mainLightNode = SCNNode()
        mainLightNode.light = mainLight
        scene.rootNode.addChildNode(mainLightNode)

        let fillLight = SCNLight()
        fillLight.type = .directional
        fillLight.color = uiColor(hex: Theme.Scene.skyLightHex)
        let fillNode = SCNNode()
        fillNode.light = fillLight
        fillNode.eulerAngles = SCNVector3(-Float.pi * 0.25, -Float.pi * 0.65, 0)
        scene.rootNode.addChildNode(fillNode)

        var lamps: [SCNNode] = []
        lamps.reserveCapacity(Theme.Scene.maxOmniLights)
        for _ in 0..<Theme.Scene.maxOmniLights {
            let light = SCNLight()
            light.type = .omni
            light.intensity = 0
            light.attenuationStartDistance = Theme.Scene.lampAttenuationStart
            light.attenuationEndDistance = Theme.Scene.lampAttenuationEnd
            light.attenuationFalloffExponent = 1.6
            let node = SCNNode()
            node.light = light
            scene.rootNode.addChildNode(node)
            lamps.append(node)
        }

        let glowRoot = SCNNode()
        scene.rootNode.addChildNode(glowRoot)

        return Rig(scene: scene, meshRoot: meshRoot, cameraNode: cameraNode, ambientLight: ambientLight,
                   mainLightNode: mainLightNode, mainLight: mainLight, fillLight: fillLight,
                   lampNodes: lamps, glowRoot: glowRoot)
    }

    static func installGeometry(_ prepared: ExtensionScenePreparedGeometry, into rig: Rig) {
        for child in rig.meshRoot.childNodes { child.removeFromParentNode() }
        for batch in prepared.batches {
            let node = SCNNode(geometry: makeSCNGeometry(from: batch))
            if isSeeThrough(batch.material) {
                node.castsShadow = false
                node.renderingOrder = 10
            } else {
                node.castsShadow = true
                node.renderingOrder = 0
            }
            rig.meshRoot.addChildNode(node)
        }

        let target = prepared.bounds.worldTarget
        let r = prepared.bounds.radius
        let sunDir = simd_normalize(SIMD3<Double>(-0.48, 0.76, 0.44))
        let sunDist = max(r * 3.0, 8.0)
        let sunPos = target + sunDir * sunDist
        let right = simd_normalize(simd_cross(SIMD3<Double>(0, 1, 0), sunDir))
        let up = simd_cross(sunDir, right)
        rig.mainLightNode.simdTransform = simd_float4x4(columns: (
            SIMD4(Float(right.x), Float(right.y), Float(right.z), 0),
            SIMD4(Float(up.x), Float(up.y), Float(up.z), 0),
            SIMD4(Float(sunDir.x), Float(sunDir.y), Float(sunDir.z), 0),
            SIMD4(Float(sunPos.x), Float(sunPos.y), Float(sunPos.z), 1)
        ))
        rig.mainLight.orthographicScale = CGFloat(r * 1.35)
        rig.mainLight.zNear = CGFloat(max(0.1, sunDist - r * 2.4))
        rig.mainLight.zFar = CGFloat(sunDist + r * 2.6)
        rig.mainLight.shadowColor = UIColor(white: 0, alpha: 0.22)
    }

    static func selectLitPins(_ pins: [ExtensionScenePin], target: SIMD3<Double>) -> (omnis: [ExtensionScenePin], glows: [ExtensionScenePin]) {
        let indexed = pins.enumerated().compactMap { offset, pin -> (offset: Int, dist2: Double, pin: ExtensionScenePin)? in
            guard pin.light?.on == true, pin.at.count >= 2, pin.at[0].isFinite, pin.at[1].isFinite else { return nil }
            let h = (pin.height?.isFinite == true) ? pin.height! : 0
            let world = SIMD3<Double>(pin.at[0], h, -pin.at[1])
            let delta = world - target
            return (offset, simd_length_squared(delta), pin)
        }
        let sorted = indexed.sorted {
            if abs($0.dist2 - $1.dist2) > 1e-9 { return $0.dist2 < $1.dist2 }
            return $0.offset < $1.offset
        }.map(\.pin)
        let omnis = Array(sorted.prefix(Theme.Scene.maxOmniLights))
        let glows = sorted.count > Theme.Scene.maxOmniLights ? Array(sorted.dropFirst(Theme.Scene.maxOmniLights)) : []
        return (omnis, glows)
    }

    static func glowDiscWorldY(floorTop: Double) -> Float {
        Float(floorTop + 0.008)
    }

    static func updateLighting(in rig: Rig, bounds: ExtensionSceneBounds, floorTop: Double, pins: [ExtensionScenePin], dark: Bool) {
        rig.scene.background.contents = resolvedBackgroundColor(dark: dark)
        rig.ambientLight.intensity = dark ? Theme.Scene.darkAmbientIntensity : Theme.Scene.lightAmbientIntensity
        rig.ambientLight.color = UIColor.white
        rig.mainLight.intensity = dark ? Theme.Scene.darkKeyIntensity : Theme.Scene.lightKeyIntensity
        rig.fillLight.intensity = dark ? Theme.Scene.darkFillIntensity : Theme.Scene.lightFillIntensity

        let (omnis, glows) = selectLitPins(pins, target: bounds.worldTarget)
        for (index, node) in rig.lampNodes.enumerated() {
            guard let light = node.light else { continue }
            if index < omnis.count {
                let pin = omnis[index]
                let h = max(pin.height ?? 0, floorTop + 0.65)
                node.simdPosition = SIMD3(Float(pin.at[0]), Float(h), Float(-pin.at[1]))
                let cool = pin.light?.warmth == ExtensionSceneWarmth.cool
                light.color = uiColor(hex: cool ? Theme.Scene.coolLightHex : Theme.Scene.warmLightHex)
                light.intensity = dark ? Theme.Scene.lampIntensity * 1.35 : Theme.Scene.lampIntensity
            } else {
                light.intensity = 0
            }
        }

        for child in rig.glowRoot.childNodes { child.removeFromParentNode() }
        let discY = glowDiscWorldY(floorTop: floorTop)
        for pin in glows {
            let disc = SCNCylinder(radius: 0.85, height: 0.004)
            let mat = SCNMaterial()
            mat.lightingModel = .constant
            let cool = pin.light?.warmth == ExtensionSceneWarmth.cool
            mat.diffuse.contents = uiColor(hex: cool ? Theme.Scene.coolLightHex : Theme.Scene.warmLightHex, alpha: 0.28)
            mat.writesToDepthBuffer = false
            mat.blendMode = .add
            disc.materials = [mat]
            let node = SCNNode(geometry: disc)
            node.renderingOrder = 5
            node.castsShadow = false
            node.simdPosition = SIMD3(Float(pin.at[0]), discY, Float(-pin.at[1]))
            rig.glowRoot.addChildNode(node)
        }
    }

    static func updateCamera(in rig: Rig, projection: ExtensionSceneProjection) {
        rig.cameraNode.simdTransform = projection.simdTransform
        if let cam = rig.cameraNode.camera {
            cam.orthographicScale = projection.orthographicScale
            cam.zNear = projection.zNear
            cam.zFar = projection.zFar
        }
    }

    static func bestAntialiasingMode(device: MTLDevice?) -> SCNAntialiasingMode {
        guard let device else { return .none }
        if device.supportsTextureSampleCount(4) { return .multisampling4X }
        if device.supportsTextureSampleCount(2) { return .multisampling2X }
        return .none
    }

    static func snapshot(prepared: ExtensionScenePreparedGeometry, pins: [ExtensionScenePin],
                         camera: ExtensionSceneCameraState, size: CGSize, scale: CGFloat, dark: Bool) -> UIImage? {
        let pixelSize = CGSize(width: max(1, round(size.width * scale)), height: max(1, round(size.height * scale)))
        let projection = ExtensionSceneProjection(bounds: prepared.bounds, camera: camera, viewport: size)

        if let device = MTLCreateSystemDefaultDevice() {
            let rig = makeRig()
            installGeometry(prepared, into: rig)
            updateLighting(in: rig, bounds: prepared.bounds, floorTop: prepared.floorTop, pins: pins, dark: dark)
            updateCamera(in: rig, projection: projection)

            let renderer = SCNRenderer(device: device, options: nil)
            renderer.scene = rig.scene
            renderer.pointOfView = rig.cameraNode
            let image = renderer.snapshot(atTime: 0, with: pixelSize, antialiasingMode: bestAntialiasingMode(device: device))
            if image.size.width > 0 {
                if let cg = image.cgImage {
                    return UIImage(cgImage: cg, scale: scale, orientation: .up)
                }
                return image
            }
        }
        return softwareFallbackSnapshot(prepared: prepared, projection: projection, size: size, scale: scale, dark: dark)
    }

    private static func softwareFallbackSnapshot(prepared: ExtensionScenePreparedGeometry,
                                                 projection: ExtensionSceneProjection,
                                                 size: CGSize, scale: CGFloat, dark: Bool) -> UIImage? {
        let width = max(1, Int(round(size.width * scale)))
        let height = max(1, Int(round(size.height * scale)))
        guard let space = CGColorSpace(name: CGColorSpace.sRGB),
              let ctx = CGContext(data: nil, width: width, height: height, bitsPerComponent: 8, bytesPerRow: width * 4,
                                  space: space, bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue) else { return nil }
        ctx.setFillColor(resolvedBackgroundColor(dark: dark).cgColor)
        ctx.fill(CGRect(x: 0, y: 0, width: width, height: height))

        let b = prepared.bounds
        let corners = [
            projection.project(planX: b.minX, planY: b.minY, height: b.minH).point,
            projection.project(planX: b.maxX, planY: b.minY, height: b.minH).point,
            projection.project(planX: b.maxX, planY: b.maxY, height: b.minH).point,
            projection.project(planX: b.minX, planY: b.maxY, height: b.minH).point,
        ]
        ctx.beginPath()
        for (idx, pt) in corners.enumerated() {
            let p = CGPoint(x: pt.x * scale, y: CGFloat(height) - pt.y * scale)
            if idx == 0 { ctx.move(to: p) } else { ctx.addLine(to: p) }
        }
        ctx.closePath()
        ctx.setFillColor(uiColor(hex: Theme.Scene.tileHex, alpha: dark ? 0.65 : 0.9).cgColor)
        ctx.fillPath()
        guard let cg = ctx.makeImage() else { return nil }
        return UIImage(cgImage: cg, scale: scale, orientation: .up)
    }
}
#endif
