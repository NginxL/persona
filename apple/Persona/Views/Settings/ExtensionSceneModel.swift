#if os(iOS)
import CoreGraphics
import CryptoKit
import Foundation
import Observation
import PersonaWire
import UIKit

nonisolated struct ExtensionSceneSavedState: Codable, Equatable, Sendable {
    var camera: ExtensionSceneCameraState
    var touched: Bool

    init(camera: ExtensionSceneCameraState = ExtensionSceneCameraState(), touched: Bool = false) {
        self.camera = camera
        self.touched = touched
    }
}

private nonisolated struct ExtensionSceneSavedDocument: Codable, Sendable {
    var recent: [String] = []
    var pages: [String: ExtensionSceneSavedState] = [:]
}

/// Bounded actor-serialized state-cache store for user-adjusted 3D scene cameras (`limit = 64`).
/// Disk reads and writes run strictly off the main actor.
private actor ExtensionSceneDiskStore {
    static let shared = ExtensionSceneDiskStore()
    private static let name = "extension-scene-cameras"
    private static let limit = 64
    private var document: ExtensionSceneSavedDocument?

    func state(_ key: String) -> ExtensionSceneSavedState? {
        loadDocument().pages[key]
    }

    func save(_ key: String, _ state: ExtensionSceneSavedState) {
        var document = loadDocument()
        document.pages[key] = state
        document.recent.removeAll { $0 == key }
        document.recent.append(key)
        if document.recent.count > Self.limit {
            let gone = document.recent.prefix(document.recent.count - Self.limit)
            document.recent.removeFirst(document.recent.count - Self.limit)
            for old in gone { document.pages[old] = nil }
        }
        self.document = document
        StateCache.saveNow(Self.name, document)
    }

    private func loadDocument() -> ExtensionSceneSavedDocument {
        if let document { return document }
        let loaded = StateCache.load(Self.name, as: ExtensionSceneSavedDocument.self) ?? ExtensionSceneSavedDocument()
        document = loaded
        return loaded
    }
}

/// Bounded memory cache for compact card and Share-as-Image still snapshots (`32 MiB` pixel limit, P11).
nonisolated enum ExtensionSceneSnapshotCache {
    nonisolated(unsafe) private static let cache: NSCache<NSString, UIImage> = {
        let c = NSCache<NSString, UIImage>()
        c.totalCostLimit = 32 << 20
        return c
    }()

    static func image(for key: String) -> UIImage? {
        cache.object(forKey: key as NSString)
    }

    static func store(_ image: UIImage, for key: String) {
        let w = Int(round(image.size.width * image.scale))
        let h = Int(round(image.size.height * image.scale))
        cache.setObject(image, forKey: key as NSString, cost: max(1, w * h * 4))
    }
}

/// State of one 3D scene component, shared by its inline viewport and its full-screen cover.
/// Geometry is built off the main actor only when `shapes` change; state-only redraws reuse `prepared` in place
/// and keep a touched camera where the person left it.
@MainActor @Observable final class ExtensionSceneModel {
    private(set) var scene: ExtensionSceneComponent
    private(set) var prepared: ExtensionScenePreparedGeometry?
    private(set) var camera: ExtensionSceneCameraState
    private(set) var touched = false
    /// Shown full screen on a viewport taller than it is wide: the untouched camera opens steeper (#14335).
    private(set) var tallView = false
    private(set) var geometryVersion = 0
    private(set) var lightVersion = 0

    @ObservationIgnored let componentKey: String
    @ObservationIgnored let namespace: String
    @ObservationIgnored let persistenceKey: String
    @ObservationIgnored private let keeps: Bool
    @ObservationIgnored private let onSave: ((ExtensionSceneSavedState) -> Void)?
    @ObservationIgnored private var restored: Bool
    @ObservationIgnored private var revision = 0
    @ObservationIgnored private var saveTask: Task<Void, Never>?
    /// The scene's walls, for the cutaway (#13036), and the rebuild for a new set of lowered walls under way.
    @ObservationIgnored private var walls: [ExtensionSceneCutaway.Wall] = []
    @ObservationIgnored private var cutawayTask: Task<Void, Never>?
    @ObservationIgnored private var cutawayPending: [Int]?
    @ObservationIgnored private var wheelSaveTask: Task<Void, Never>?

    init(scene: ExtensionSceneComponent, namespace: String = "", restored: ExtensionSceneSavedState? = nil,
         keeps: Bool = true, onSave: ((ExtensionSceneSavedState) -> Void)? = nil) {
        self.scene = scene
        componentKey = scene.key ?? ""
        self.namespace = namespace
        persistenceKey = Self.makePersistenceKey(scene.key, namespace: namespace)
        self.keeps = keeps
        self.onSave = onSave
        self.restored = restored != nil || !keeps || scene.key?.isEmpty != false
        if let restored, restored.touched {
            camera = restored.camera.clamped()
            touched = true
        } else {
            camera = ExtensionSceneCameraState.resolved(scene.camera)
            touched = false
        }
    }

    private static func makePersistenceKey(_ key: String?, namespace: String) -> String {
        guard let key, !key.isEmpty else { return "" }
        guard !namespace.isEmpty else { return key }
        let digest = SHA256.hash(data: Data((namespace + "\n" + key).utf8))
        return key + "-" + digest.prefix(16).map { String(format: "%02x", $0) }.joined()
    }

    private static var pictureCache: [(shapes: [ExtensionSceneShape], yaw: Double, prepared: ExtensionScenePreparedGeometry)] = []

    /// Synchronous preparation for SwiftUI's `ImageRenderer` (`extensionPicture == true`), which never runs `.task`.
    /// Caches the last 8 shape lists so `MessageShareCard.render`'s two-pass `size()` + `render()` builds meshes once.
    static func picture(_ scene: ExtensionSceneComponent) -> ExtensionSceneModel {
        let model = ExtensionSceneModel(scene: scene, keeps: false)
        let yaw = model.camera.yaw
        if let hit = pictureCache.last(where: { $0.shapes == scene.shapes && $0.yaw == yaw }) {
            model.prepared = hit.prepared
        } else {
            let built = ExtensionSceneGeometryBuilder.build(scene.shapes, cutawayYaw: yaw)
            if pictureCache.count >= 8 { pictureCache.removeFirst() }
            pictureCache.append((scene.shapes, yaw, built))
            model.prepared = built
        }
        model.geometryVersion = 1
        model.lightVersion = 1
        return model
    }

    func reload(_ next: ExtensionSceneComponent) async {
        revision += 1
        let currentRevision = revision
        if !restored {
            restored = true
            if keeps, !persistenceKey.isEmpty, let saved = await ExtensionSceneDiskStore.shared.state(persistenceKey) {
                apply(saved)
            }
        }

        let shapesChanged = prepared == nil || scene.shapes != next.shapes
        var nextPrepared = prepared
        if shapesChanged {
            let yaw = touched ? camera.yaw : ExtensionSceneCameraState.resolved(next.camera).yaw
            nextPrepared = await Self.prepareShapes(next.shapes, cutawayYaw: yaw)
        }
        guard currentRevision == revision else { return }

        let lightsChanged = shapesChanged || Self.activeLights(scene.pins) != Self.activeLights(next.pins)
        scene = next
        if let nextPrepared, shapesChanged {
            cutawayTask?.cancel()
            cutawayPending = nil
            walls = ExtensionSceneCutaway.walls(next.shapes)
            prepared = nextPrepared
            geometryVersion += 1
        }
        if lightsChanged {
            lightVersion += 1
        }
        if !touched {
            let resolved = defaultCamera
            if camera != resolved { camera = resolved }
        }
        refreshCutaway()
    }

    @concurrent nonisolated private static func prepareShapes(_ shapes: [ExtensionSceneShape], cutawayYaw: Double) async -> ExtensionScenePreparedGeometry {
        ExtensionSceneGeometryBuilder.build(shapes, cutawayYaw: cutawayYaw)
    }

    /// The walls to draw low for the camera as it is now (#13036): a dot product per wall. When they are not the ones the
    /// geometry was built with, it is built again off the main actor; a build overtaken by another turn is dropped.
    private func refreshCutaway() {
        guard let prepared, !walls.isEmpty else { return }
        let lowered = ExtensionSceneCutaway.lowered(walls, bounds: prepared.bounds, yaw: camera.yaw)
        guard lowered != (cutawayPending ?? prepared.lowered) else { return }
        cutawayTask?.cancel()
        cutawayPending = lowered
        let shapes = scene.shapes
        let yaw = camera.yaw
        cutawayTask = Task { [weak self] in
            let built = await Self.prepareShapes(shapes, cutawayYaw: yaw)
            guard !Task.isCancelled, let self, self.scene.shapes == shapes, self.cutawayPending == built.lowered else { return }
            self.cutawayPending = nil
            self.prepared = built
            self.geometryVersion += 1
        }
    }

    private struct ActiveLightKey: Equatable {
        var id: String
        var x: Double
        var y: Double
        var height: Double
        var warmth: String
    }

    private static func activeLights(_ pins: [ExtensionScenePin]?) -> [ActiveLightKey] {
        guard let pins else { return [] }
        return pins.compactMap { pin in
            guard pin.light?.on == true, pin.at.count >= 2 else { return nil }
            return ActiveLightKey(
                id: pin.id,
                x: pin.at[0],
                y: pin.at[1],
                height: pin.height ?? 0,
                warmth: pin.light?.warmth ?? ExtensionSceneWarmth.warm
            )
        }
    }

    /// The camera the scene opens at here: the scene's own, steeper in a tall full-screen view.
    var defaultCamera: ExtensionSceneCameraState {
        var camera = ExtensionSceneCameraState.resolved(scene.camera)
        if tallView { camera.pitch = max(camera.pitch, Theme.Scene.tallViewPitch) }
        return camera.clamped()
    }

    /// The full-screen view says whether it is taller than it is wide (and false when it closes); an untouched camera
    /// follows the default for the view. Never persisted: the card keeps the scene's own pitch.
    func setTallView(_ tall: Bool) {
        guard tall != tallView else { return }
        tallView = tall
        if !touched { camera = defaultCamera }
    }

    func updateCamera(_ next: ExtensionSceneCameraState, persist: Bool = false) {
        let clamped = next.clamped()
        guard clamped != camera || !touched else { return }
        camera = clamped
        touched = true
        refreshCutaway()
        if persist { self.persist() }
    }

    func orbit(deltaYaw: Double, deltaPitch: Double, persist: Bool = false) {
        updateCamera(
            ExtensionSceneCameraState(yaw: camera.yaw + deltaYaw, pitch: camera.pitch + deltaPitch, zoom: camera.zoom),
            persist: persist
        )
    }

    func zoom(_ factor: Double, persist: Bool = true) {
        updateCamera(
            ExtensionSceneCameraState(yaw: camera.yaw, pitch: camera.pitch, zoom: camera.zoom * factor),
            persist: persist
        )
    }

    /// Restores the default camera (`defaultCamera`) and marks it untouched (persisting `touched: false` so reopening uses the default).
    func reset() {
        wheelSaveTask?.cancel()
        camera = defaultCamera
        touched = false
        refreshCutaway()
        writeCurrent()
    }

    func persistCurrent(coalesced: Bool = false) {
        wheelSaveTask?.cancel()
        guard coalesced else {
            writeCurrent()
            return
        }
        wheelSaveTask = Task { [weak self] in
            try? await Task.sleep(for: .milliseconds(150))
            guard !Task.isCancelled else { return }
            self?.writeCurrent()
        }
    }

    private func apply(_ saved: ExtensionSceneSavedState) {
        if saved.touched {
            camera = saved.camera.clamped()
            touched = true
        } else {
            camera = defaultCamera
            touched = false
        }
    }

    private func persist() {
        wheelSaveTask?.cancel()
        writeCurrent()
    }

    private func writeCurrent() {
        let saved = ExtensionSceneSavedState(camera: camera, touched: touched)
        guard keeps, !persistenceKey.isEmpty else {
            onSave?(saved)
            return
        }
        onSave?(saved)
        let previous = saveTask
        let key = persistenceKey
        saveTask = Task {
            await previous?.value
            await ExtensionSceneDiskStore.shared.save(key, saved)
        }
    }

    @concurrent nonisolated static func renderPreviewSnapshot(
        prepared: ExtensionScenePreparedGeometry,
        pins: [ExtensionScenePin],
        camera: ExtensionSceneCameraState,
        size: CGSize,
        scale: CGFloat,
        dark: Bool
    ) async -> UIImage? {
        let key = snapshotKey(prepared: prepared, pins: pins, camera: camera, size: size, scale: scale, dark: dark)
        if let cached = ExtensionSceneSnapshotCache.image(for: key) { return cached }
        guard let image = ExtensionSceneGeometryBuilder.snapshot(
            prepared: prepared, pins: pins, camera: camera, size: size, scale: scale, dark: dark
        ) else { return nil }
        ExtensionSceneSnapshotCache.store(image, for: key)
        return image
    }

    nonisolated static func renderPictureSnapshot(
        prepared: ExtensionScenePreparedGeometry,
        pins: [ExtensionScenePin],
        camera: ExtensionSceneCameraState,
        size: CGSize,
        scale: CGFloat,
        dark: Bool
    ) -> UIImage? {
        let key = snapshotKey(prepared: prepared, pins: pins, camera: camera, size: size, scale: scale, dark: dark)
        if let cached = ExtensionSceneSnapshotCache.image(for: key) { return cached }
        guard let image = ExtensionSceneGeometryBuilder.snapshot(
            prepared: prepared, pins: pins, camera: camera, size: size, scale: scale, dark: dark
        ) else { return nil }
        ExtensionSceneSnapshotCache.store(image, for: key)
        return image
    }

    nonisolated private static func snapshotKey(
        prepared: ExtensionScenePreparedGeometry,
        pins: [ExtensionScenePin],
        camera: ExtensionSceneCameraState,
        size: CGSize,
        scale: CGFloat,
        dark: Bool
    ) -> String {
        var hasher = SHA256()
        for batch in prepared.batches {
            hasher.update(data: Data(batch.material.utf8))
            hasher.update(data: batch.positions)
            hasher.update(data: batch.colors)
            hasher.update(data: batch.indices)
        }
        for pin in pins where pin.light?.on == true && pin.at.count >= 2 {
            let line = "\(pin.id):\(pin.at[0]):\(pin.at[1]):\(pin.height ?? 0):\(pin.light?.warmth ?? "")\n"
            hasher.update(data: Data(line.utf8))
        }
        let meta = "\(camera.yaw),\(camera.pitch),\(camera.zoom),\(Int(round(size.width * scale)))x\(Int(round(size.height * scale))),\(dark)"
        hasher.update(data: Data(meta.utf8))
        return hasher.finalize().prefix(16).map { String(format: "%02x", $0) }.joined()
    }
}
#endif
