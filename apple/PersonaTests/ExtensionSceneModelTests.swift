import CoreGraphics
import Foundation
import PersonaWire
import Testing
import UIKit
import simd
@testable import Persona

/// Unit tests for UI version 11's 3D `scene` model, projection math, static mesh batcher, camera persistence and
/// still snapshot renderer (#12544). All fixtures use synthetic geometry and invented labels.
@MainActor
struct ExtensionSceneModelTests {
    private static let repo = URL(fileURLWithPath: #filePath)
        .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()

    private static func sampleShapes() -> [ExtensionSceneShape] {
        [
            .prism(ExtensionScenePrism(
                points: [[-4.0, -2.5], [0.0, -2.5], [4.0, -2.5], [4.0, 0.5], [1.0, 0.5], [1.0, 2.5], [-4.0, 2.5]],
                bottom: 0.0, top: 0.06, color: "#C9AD8C", material: ExtensionSceneMaterial.wood
            )),
            .box(ExtensionSceneBox(
                center: [0.0, 2.45], size: [8.0, 0.12], bottom: 0.06, top: 1.45,
                color: "#EAE6DF", material: ExtensionSceneMaterial.matte
            )),
            .box(ExtensionSceneBox(
                center: [-1.8, 0.9], size: [2.0, 0.9], yaw: 30, round: 0.12, bottom: 0.06, top: 0.55,
                color: "#788796", material: ExtensionSceneMaterial.matte
            )),
            .cylinder(ExtensionSceneCylinder(
                center: [-0.4, -0.2], radius: 0.45, bottom: 0.06, top: 0.44,
                color: "#D8D3CA", material: ExtensionSceneMaterial.tile
            )),
            .cylinder(ExtensionSceneCylinder(
                center: [-3.2, 1.8], radius: 0.15, bottom: 0.06, top: 1.5,
                color: "#9BA2AA", material: ExtensionSceneMaterial.metal
            )),
            .box(ExtensionSceneBox(
                center: [-1.8, -2.4], size: [1.3, 0.06], bottom: 0.5, top: 1.2,
                color: "#20252D", material: ExtensionSceneMaterial.screen
            )),
            .box(ExtensionSceneBox(
                center: [1.5, -2.45], size: [2.2, 0.06], bottom: 0.55, top: 1.35,
                color: "#9EC4DC", material: ExtensionSceneMaterial.glass
            )),
        ]
    }

    private static func sampleScene(lampOn: Bool = true, camera: ExtensionSceneCamera? = nil) -> ExtensionSceneComponent {
        ExtensionSceneComponent(
            key: "home",
            alt: "Synthetic two-room home",
            caption: "Drag to turn, pinch to zoom.",
            camera: camera,
            shapes: sampleShapes(),
            labels: [ExtensionSceneLabel(text: "Living room", at: [-1.5, 0.0], height: 0.08)],
            pins: [
                ExtensionScenePin(
                    id: "lamp",
                    at: [-1.5, 0.2],
                    height: 2.3,
                    label: "Ceiling lamp",
                    state: lampOn ? "On" : "Off",
                    icon: "light",
                    tone: lampOn ? "positive" : nil,
                    light: ExtensionSceneLight(on: lampOn, warmth: ExtensionSceneWarmth.warm)
                ),
            ]
        )
    }

    @Test func boundsExcludePinsAndLabelsAndIncludeRotatedBoxes() {
        let box = ExtensionSceneShape.box(ExtensionSceneBox(
            center: [1.0, 2.0], size: [4.0, 2.0], yaw: 90, bottom: 0.1, top: 2.1
        ))
        let bounds = ExtensionSceneBounds.of([box])
        // Rotated 90 deg: width along +x is 2.0 (0...2), depth along +y is 4.0 (0...4).
        #expect(abs(bounds.minX - 0.0) < 1e-6 && abs(bounds.maxX - 2.0) < 1e-6)
        #expect(abs(bounds.minY - 0.0) < 1e-6 && abs(bounds.maxY - 4.0) < 1e-6)
        #expect(abs(bounds.minH - 0.1) < 1e-6 && abs(bounds.maxH - 2.1) < 1e-6)
        // SceneKit world coordinates map plan (x, y) and height h to (x, h, -y).
        #expect(simd_length(bounds.worldTarget - SIMD3(1.0, 1.1, -2.0)) < 1e-6)

        var withFarPin = Self.sampleScene()
        withFarPin.pins?.append(ExtensionScenePin(id: "far", at: [500, -500], height: 40, label: "Far"))
        withFarPin.labels?.append(ExtensionSceneLabel(text: "Far label", at: [-500, 500], height: 40))
        #expect(ExtensionSceneBounds.of(withFarPin.shapes) == ExtensionSceneBounds.of(Self.sampleShapes()))
    }

    @Test func orthographicProjectionMatchesPlanDirectionsAndRemainsOrthonormalAt90Degrees() {
        let bounds = ExtensionSceneBounds(minX: -4, maxX: 4, minY: -3, maxY: 3, minH: 0, maxH: 2.6)
        let viewport = CGSize(width: 360, height: 280)

        for pitch in [10.0, 35.0, 90.0] {
            let proj = ExtensionSceneProjection(
                bounds: bounds,
                camera: ExtensionSceneCameraState(yaw: 225, pitch: pitch, zoom: 1.0),
                viewport: viewport
            )
            #expect(abs(simd_length(proj.right) - 1.0) < 1e-9)
            #expect(abs(simd_length(proj.camUp) - 1.0) < 1e-9)
            #expect(abs(simd_length(proj.out) - 1.0) < 1e-9)
            #expect(abs(simd_dot(proj.right, proj.camUp)) < 1e-9)
            #expect(abs(simd_dot(proj.right, proj.out)) < 1e-9)
            #expect(abs(simd_dot(proj.camUp, proj.out)) < 1e-9)
            let det = simd_determinant(simd_double3x3(columns: (proj.right, proj.camUp, proj.out)))
            #expect(abs(det - 1.0) < 1e-9, "right-handed basis at pitch \(pitch)")

            let centre = proj.project(planX: 0, planY: 0, height: 1.3)
            #expect(abs(centre.point.x - 180) < 1e-6 && abs(centre.point.y - 140) < 1e-6)
        }

        // At default yaw 225 (standing south-west looking north-east), +x (east) projects to the right of -x (west),
        // and greater height projects higher on screen (smaller y).
        let def = ExtensionSceneProjection(bounds: bounds, camera: ExtensionSceneCameraState(), viewport: viewport)
        let east = def.project(planX: 2, planY: 0, height: 0).point
        let west = def.project(planX: -2, planY: 0, height: 0).point
        let floorPt = def.project(planX: 0, planY: 0, height: 0).point
        let ceilingPt = def.project(planX: 0, planY: 0, height: 2.4).point
        #expect(east.x > west.x)
        #expect(ceilingPt.y < floorPt.y)
    }

    @Test func framingFitsTheProjectedModelAndHoldsItsSizeWhileTurning() {
        // A flat home, 8 x 6 m and 2.4 m high, in a wide card.
        let bounds = ExtensionSceneBounds(minX: -4, maxX: 4, minY: -3, maxY: 3, minH: 0, maxH: 2.4)
        let viewport = CGSize(width: 360, height: 280)
        let scales = [0.0, 45.0, 90.0, 225.0].map { yaw in
            ExtensionSceneProjection(bounds: bounds, camera: ExtensionSceneCameraState(yaw: yaw, pitch: 35, zoom: 1),
                                     viewport: viewport).orthographicScale
        }
        // turning never changes its size on screen
        #expect(scales.allSatisfy { abs($0 - scales[0]) < 1e-9 })
        // tighter than the sphere around it (radius 5.14 m with the margin)
        #expect(scales[0] < bounds.radius * Theme.Scene.fitMargin * 0.9)
        // and every corner of the model is still inside the view, from every side
        for yaw in stride(from: 0.0, to: 360.0, by: 15.0) {
            let proj = ExtensionSceneProjection(bounds: bounds, camera: ExtensionSceneCameraState(yaw: yaw, pitch: 35, zoom: 1),
                                                viewport: viewport)
            for x in [bounds.minX, bounds.maxX] {
                for y in [bounds.minY, bounds.maxY] {
                    for h in [bounds.minH, bounds.maxH] {
                        let p = proj.project(planX: x, planY: y, height: h).point
                        #expect(p.x >= 0 && p.x <= viewport.width && p.y >= 0 && p.y <= viewport.height,
                                "corner (\(x), \(y), \(h)) at yaw \(yaw) projects to \(p)")
                    }
                }
            }
        }
        // zooming in still scales the view
        let zoomed = ExtensionSceneProjection(bounds: bounds, camera: ExtensionSceneCameraState(yaw: 0, pitch: 35, zoom: 2),
                                              viewport: viewport)
        #expect(abs(zoomed.orthographicScale - scales[0] / 2) < 1e-9)
    }

    @Test func roomLabelsMoveOffPinsOrStayOut() {
        let bounds = ExtensionSceneBounds(minX: -4, maxX: 4, minY: -3, maxY: 3, minH: 0, maxH: 2.4)
        let proj = ExtensionSceneProjection(bounds: bounds, camera: ExtensionSceneCameraState(), viewport: CGSize(width: 360, height: 280))
        let spot = proj.project(planX: 0, planY: 0, height: 0.02).point
        let label = ExtensionSceneLabel(text: "Living room", at: [0, 0], height: 0.02)
        let height = Theme.Size.sceneLabelHeight

        // nothing in the way: drawn where it stands
        let free = ExtensionSceneLabelsLayer.place([label], projection: proj, scale: 1, avoid: [])
        #expect(free.count == 1 && free[0].point == spot)

        // a pin over it: just below the pin
        let pin = CGRect(x: spot.x - 60, y: spot.y - 16, width: 120, height: 32)
        let moved = ExtensionSceneLabelsLayer.place([label], projection: proj, scale: 1, avoid: [pin])
        #expect(moved.count == 1 && abs(moved[0].point.y - (pin.maxY + height * 0.5 + Theme.Space.xxs)) < 1e-9)

        // the room below and above the pin taken too: left out
        let around = [pin, pin.offsetBy(dx: 0, dy: pin.height), pin.offsetBy(dx: 0, dy: -pin.height)]
        #expect(ExtensionSceneLabelsLayer.place([label], projection: proj, scale: 1, avoid: around).isEmpty)

        // two labels on the same spot: the second moves rather than covering the first
        let twice = ExtensionSceneLabelsLayer.place([label, ExtensionSceneLabel(text: "Hall", at: [0, 0], height: 0.02)],
                                                    projection: proj, scale: 1, avoid: [])
        #expect(twice.count == 2 && twice[0].point != twice[1].point)
    }

    @Test func geometryBuilderBatchesMixedShapesIntoAtMostSixMaterialGroups() {
        let prepared = ExtensionSceneGeometryBuilder.build(Self.sampleShapes())
        #expect(prepared.batches.count == 6 && prepared.batches.count <= 6)
        #expect(prepared.totalVertices > 0 && prepared.totalTriangles > 0)
        for batch in prepared.batches {
            #expect(batch.vertexCount > 0 && batch.triangleCount > 0)
            // 4-float linear RGBA per vertex so SceneKit's shader reads alpha = 1.0 cleanly.
            #expect(batch.colors.count == batch.vertexCount * 16)
            #expect(batch.positions.count == batch.vertexCount * 12)
            #expect(batch.normals.count == batch.vertexCount * 12)
            #expect(batch.indices.count == batch.triangleCount * 12)
        }
    }

    @Test func stateOnlyRedrawReusesPreparedGeometryAndKeepsTouchedCamera() async {
        let initial = Self.sampleScene(lampOn: true, camera: ExtensionSceneCamera(yaw: 210, pitch: 32, zoom: 1.1))
        let model = ExtensionSceneModel(scene: initial, keeps: false)
        await model.reload(initial)
        #expect(model.prepared != nil && model.geometryVersion == 1 && model.lightVersion == 1)
        #expect(model.camera == ExtensionSceneCameraState(yaw: 210, pitch: 32, zoom: 1.1))
        #expect(!model.touched)

        // Untouched redraw with a new default camera adopts the new camera without rebuilding shapes.
        let shiftedCamera = Self.sampleScene(lampOn: true, camera: ExtensionSceneCamera(yaw: 240, pitch: 40, zoom: 1.25))
        await model.reload(shiftedCamera)
        #expect(model.geometryVersion == 1 && model.camera == ExtensionSceneCameraState(yaw: 240, pitch: 40, zoom: 1.25))

        // Once the person turns the model, state-only redraws keep their camera and reuse prepared geometry.
        model.orbit(deltaYaw: 25, deltaPitch: 8)
        let userCamera = model.camera
        #expect(model.touched)

        let lampOff = Self.sampleScene(lampOn: false, camera: ExtensionSceneCamera(yaw: 180, pitch: 25, zoom: 0.9))
        await model.reload(lampOff)
        #expect(model.geometryVersion == 1, "shapes did not change so static meshes were not rebuilt")
        #expect(model.lightVersion == 2, "lamp state changed so lighting version bumped")
        #expect(model.camera == userCamera && model.touched)
    }

    @Test func resetRestoresDefaultCameraAndPersistsUntouchedFlag() async {
        var savedStates: [ExtensionSceneSavedState] = []
        let scene = Self.sampleScene(camera: ExtensionSceneCamera(yaw: 205, pitch: 38, zoom: 1.2))
        let model = ExtensionSceneModel(scene: scene, namespace: "plugin:home", keeps: false) { saved in
            savedStates.append(saved)
        }
        await model.reload(scene)
        model.orbit(deltaYaw: 40, deltaPitch: -10, persist: true)
        #expect(model.touched && savedStates.last?.touched == true)

        model.reset()
        #expect(!model.touched)
        #expect(model.camera == ExtensionSceneCameraState(yaw: 205, pitch: 38, zoom: 1.2))
        #expect(savedStates.last == ExtensionSceneSavedState(
            camera: ExtensionSceneCameraState(yaw: 205, pitch: 38, zoom: 1.2),
            touched: false
        ))

        let reopened = ExtensionSceneModel(scene: scene, namespace: "plugin:home", restored: savedStates.last, keeps: false)
        #expect(!reopened.touched && reopened.camera == ExtensionSceneCameraState(yaw: 205, pitch: 38, zoom: 1.2))
    }

    @Test func aTallFullScreenViewOpensSteeperAndTheCardKeepsTheScenesPitch() async {
        let scene = Self.sampleScene(camera: ExtensionSceneCamera(yaw: 205, pitch: 35, zoom: 1))
        let model = ExtensionSceneModel(scene: scene, keeps: false)
        await model.reload(scene)
        let steep = Theme.Scene.tallViewPitch

        // an untouched camera follows the tall view's default, and back when the view closes
        model.setTallView(true)
        #expect(model.camera == ExtensionSceneCameraState(yaw: 205, pitch: steep, zoom: 1) && !model.touched)
        #expect(model.defaultCamera == model.camera)
        model.setTallView(false)
        #expect(model.camera == ExtensionSceneCameraState(yaw: 205, pitch: 35, zoom: 1))

        // a redraw keeps the tall default; reset goes back to it
        model.setTallView(true)
        await model.reload(scene)
        #expect(model.camera.pitch == steep)
        model.orbit(deltaYaw: 20, deltaPitch: -15)
        model.reset()
        #expect(model.camera.pitch == steep && !model.touched)

        // a camera the person turned stays where they left it when the view closes
        model.orbit(deltaYaw: 20, deltaPitch: -5)
        let turned = model.camera
        model.setTallView(false)
        #expect(model.camera == turned && model.touched)

        // a scene that already looks down more keeps its own pitch
        let top = Self.sampleScene(camera: ExtensionSceneCamera(yaw: 205, pitch: 80, zoom: 1))
        let topModel = ExtensionSceneModel(scene: top, keeps: false)
        topModel.setTallView(true)
        #expect(topModel.camera.pitch == 80)
    }

    @Test func aPinOnScreenStaysInsideTheViewport() {
        let projection = ExtensionSceneProjection(bounds: ExtensionSceneBounds.of(Self.sampleShapes()),
                                                  camera: ExtensionSceneCameraState(), viewport: CGSize(width: 300, height: 600))
        let inset = Theme.Scene.pinEdgeInset
        // a pill hanging off the left edge moves right; one off the bottom moves up
        let left = projection.keptInside(CGRect(x: -30, y: 100, width: 120, height: 30), anchor: CGPoint(x: 30, y: 115))
        #expect(left.minX == inset && left.minY == 100 && left.width == 120)
        let bottom = projection.keptInside(CGRect(x: 200, y: 590, width: 80, height: 30), anchor: CGPoint(x: 240, y: 595))
        #expect(bottom.maxY == 600 - inset && bottom.minX == 200)
        // wider than the viewport: centred
        let wide = projection.keptInside(CGRect(x: 0, y: 10, width: 400, height: 30), anchor: CGPoint(x: 200, y: 25))
        #expect(wide.midX == 150)
        // an anchor off screen keeps its pill with it
        let off = CGRect(x: -200, y: 100, width: 120, height: 30)
        #expect(projection.keptInside(off, anchor: CGPoint(x: -140, y: 115)) == off)
    }

    @Test func persistenceKeyScopesByNamespaceAndCoalescesWheelSaves() async throws {
        let a = ExtensionSceneModel(scene: Self.sampleScene(), namespace: "example-home/home-a", keeps: false)
        let b = ExtensionSceneModel(scene: Self.sampleScene(), namespace: "example-home/home-b", keeps: false)
        var unkeyedScene = Self.sampleScene()
        unkeyedScene.key = nil
        let unkeyed = ExtensionSceneModel(scene: unkeyedScene, namespace: "example-home/home-a", keeps: false)
        #expect(!a.persistenceKey.isEmpty && a.persistenceKey != b.persistenceKey)
        #expect(unkeyed.persistenceKey.isEmpty)

        var saves = 0
        let coalescedModel = ExtensionSceneModel(scene: Self.sampleScene(), namespace: "wheel", keeps: false) { _ in
            saves += 1
        }
        for _ in 0..<5 {
            coalescedModel.zoom(1.03, persist: false)
            coalescedModel.persistCurrent(coalesced: true)
        }
        #expect(saves == 0)
        try await Task.sleep(for: .milliseconds(240))
        #expect(saves == 1)
    }

    @Test func pictureAndSnapshotRenderStillImagesWithSixteenLights() throws {
        var scene = Self.sampleScene()
        var pins: [ExtensionScenePin] = []
        for idx in 0..<16 {
            let ax = Double((idx % 4) * 2 - 3)
            let ay = Double((idx / 4) - 1)
            let warmth = idx.isMultiple(of: 2) ? ExtensionSceneWarmth.warm : ExtensionSceneWarmth.cool
            pins.append(ExtensionScenePin(
                id: "lamp-\(idx)",
                at: [ax, ay],
                height: 2.3,
                label: "Lamp \(idx + 1)",
                state: "On",
                tone: "positive",
                light: ExtensionSceneLight(on: true, warmth: warmth)
            ))
        }
        scene.pins = pins
        let model = ExtensionSceneModel.picture(scene)
        let prepared = try #require(model.prepared)
        for dark in [false, true] {
            let image = try #require(ExtensionSceneGeometryBuilder.snapshot(
                prepared: prepared,
                pins: scene.pins ?? [],
                camera: model.camera,
                size: CGSize(width: 320, height: 200),
                scale: 2,
                dark: dark
            ))
            #expect(image.size.width == 320 && image.size.height == 200)
        }
    }

    @Test func fiveHundredShapeSceneBuildsAndRendersFirstFramePromptly() throws {
        let materials = ExtensionSceneMaterial.all
        var shapes: [ExtensionSceneShape] = []
        shapes.reserveCapacity(500)
        for i in 0..<500 {
            let gx = Double(i % 25) * 0.55 - 6.6
            let gy = Double(i / 25) * 0.55 - 5.2
            let mat = materials[i % materials.count]
            switch i % 3 {
            case 0:
                shapes.append(.box(ExtensionSceneBox(
                    center: [gx, gy], size: [0.36, 0.32], yaw: Double((i * 17) % 90), round: 0.04,
                    bottom: 0.0, top: 0.45, color: "#C8B8A2", material: mat
                )))
            case 1:
                shapes.append(.cylinder(ExtensionSceneCylinder(
                    center: [gx, gy], radius: 0.16, bottom: 0.0, top: 0.5, color: "#9AA8B6", material: mat
                )))
            default:
                shapes.append(.prism(ExtensionScenePrism(
                    points: [[gx - 0.18, gy - 0.16], [gx + 0.18, gy - 0.16], [gx + 0.18, gy + 0.16], [gx - 0.18, gy + 0.16]],
                    bottom: 0.0, top: 0.12, color: "#D4CFC6", material: mat
                )))
            }
        }
        var pins: [ExtensionScenePin] = []
        for i in 0..<16 {
            let px = Double((i % 4) * 3 - 4)
            let py = Double((i / 4) * 2 - 3)
            pins.append(ExtensionScenePin(
                id: "p\(i)",
                at: [px, py],
                height: 2.3,
                label: "Lamp \(i + 1)",
                state: "On",
                tone: "positive",
                light: ExtensionSceneLight(on: true, warmth: ExtensionSceneWarmth.warm)
            ))
        }

        // Warm up Metal pipeline state once before measuring 5 cold geometry+frame builds.
        let warm = ExtensionSceneGeometryBuilder.build(Array(shapes.prefix(12)))
        _ = ExtensionSceneGeometryBuilder.snapshot(
            prepared: warm, pins: [], camera: ExtensionSceneCameraState(),
            size: CGSize(width: 360, height: 240), scale: 2, dark: false
        )

        var samplesMs: [Double] = []
        for _ in 0..<5 {
            let clock = ContinuousClock()
            var image: UIImage?
            let elapsed = clock.measure {
                let prepared = ExtensionSceneGeometryBuilder.build(shapes)
                image = ExtensionSceneGeometryBuilder.snapshot(
                    prepared: prepared, pins: pins, camera: ExtensionSceneCameraState(),
                    size: CGSize(width: 360, height: 240), scale: 2, dark: false
                )
            }
            #expect(image != nil)
            let ms = Double(elapsed.components.seconds) * 1_000 + Double(elapsed.components.attoseconds) / 1e15
            samplesMs.append(ms)
        }
        let sorted = samplesMs.sorted()
        let median = sorted[sorted.count / 2]
        print(String(format: "[SCENE_BENCH_500] median=%.1fms min=%.1fms max=%.1fms samples=%@",
                     median, sorted.first ?? 0, sorted.last ?? 0,
                     samplesMs.map { String(format: "%.1f", $0) }.joined(separator: ",")))
        // Debug builds can be slower under ASan/unoptimized loops, while -O wholemodule targets < 300 ms (#12544).
        #expect(median < 2_000)
    }

    @Test func canonicalV11SceneFlatExampleDecodesAndBuilds() throws {
        let data = try Data(contentsOf: Self.repo.appending(path: "schema/extension/examples/View.v11-scene-flat.json"))
        let view = try JSONDecoder().decode(ExtensionView.self, from: data)
        let scene = try #require(view.sections.flatMap(\.items).compactMap { item -> ExtensionSceneComponent? in
            if case .scene(let s) = item { return s }
            return nil
        }.first)
        let prepared = ExtensionSceneGeometryBuilder.build(scene.shapes)
        #expect(!prepared.batches.isEmpty && prepared.batches.count <= 6)
        // View.v11-scene-flat.json floor slabs span bottom: -0.02, top: 0 -> floorTop == 0 so glow discs sit above 0.
        #expect(prepared.floorTop >= 0.0)
        #expect(ExtensionSceneGeometryBuilder.glowDiscWorldY(floorTop: prepared.floorTop) > Float(prepared.floorTop))
    }

    @Test func sixteenActiveLightsSelectNearestEightOmnisAndElevateGlowDiscs() {
        let prepared = ExtensionSceneGeometryBuilder.build([
            .prism(ExtensionScenePrism(
                points: [[-5, -5], [5, -5], [5, 5], [-5, 5]],
                bottom: 0.0, top: 0.06, color: "#D8D3CA", material: ExtensionSceneMaterial.tile
            )),
        ])
        #expect(abs(prepared.floorTop - 0.06) < 1e-6)
        #expect(ExtensionSceneGeometryBuilder.glowDiscWorldY(floorTop: prepared.floorTop) > 0.06)

        var pins: [ExtensionScenePin] = []
        for idx in (0..<12).reversed() {
            let dist = Double(idx) * 0.5
            pins.append(ExtensionScenePin(
                id: "pin-\(idx)",
                at: [dist, 0],
                height: 0.03,
                label: "Lamp \(idx)",
                state: "On",
                tone: "positive",
                light: ExtensionSceneLight(on: true, warmth: ExtensionSceneWarmth.warm)
            ))
        }
        let selected = ExtensionSceneGeometryBuilder.selectLitPins(pins, target: prepared.bounds.worldTarget)
        #expect(selected.omnis.map(\.id) == (0..<8).map { "pin-\($0)" })
        #expect(selected.glows.map(\.id) == (8..<12).map { "pin-\($0)" })
    }
}
