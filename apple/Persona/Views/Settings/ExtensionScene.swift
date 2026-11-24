#if os(iOS)
import PersonaWire
import SceneKit
import SwiftUI
import UIKit

private struct ExtensionSceneInput: Hashable {
    var scene: ExtensionSceneComponent
    var namespace: String
}

private struct ExtensionSceneSnapshotInput: Hashable {
    var geometryVersion: Int
    var lightVersion: Int
    var camera: ExtensionSceneCameraState
    var dark: Bool
    var width: Int
    var height: Int
}

/// UI version 11's interactive 3D `scene` component (#12544). Everyday people drag to turn their home, pinch to zoom,
/// tap Reset View to return to the opening angle, and tap any device pin to run the same action as its tile.
/// Compact cards render a still preview that opens the same model in a full-screen cover.
struct ExtensionScene: View {
    var scene: ExtensionSceneComponent
    var controller: (any ExtensionViewControlling)?
    @Binding var link: LinkTarget?

    @Environment(\.pluginUICompact) private var compact
    @Environment(\.extensionPicture) private var picture
    @Environment(\.extensionCardInset) private var cardInset
    @Environment(\.dynamicTypeSize) private var typeSize
    @State private var model: ExtensionSceneModel?
    @State private var fullScreen = false
    @State private var deferredLink: LinkTarget?
    @State private var deferredAction: (() -> Void)?
    @State private var pageWidth: CGFloat = 0
    @FocusState private var coverFocused: Bool

    var body: some View {
        content
            .dynamicTypeSize(...Theme.interfaceLargestTypeSize)
            .task(id: ExtensionSceneInput(scene: scene, namespace: persistenceNamespace)) {
                if model == nil || model?.componentKey != (scene.key ?? "") || model?.namespace != persistenceNamespace {
                    model = ExtensionSceneModel(scene: scene, namespace: persistenceNamespace, keeps: controller != nil)
                }
                await model?.reload(scene)
            }
            .fullScreenCover(isPresented: $fullScreen, onDismiss: handleCoverDismiss) {
                NavigationStack {
                    if let model {
                        ExtensionSceneCanvas(
                            scene: scene,
                            model: model,
                            controller: controller,
                            link: coverLinkBinding,
                            covered: false,
                            inCover: true,
                            showsFullScreen: false,
                            onFullScreen: openFullScreen,
                            onPinPush: { fullScreen = false },
                            onDeferAction: { action in
                                deferredAction = action
                                fullScreen = false
                            }
                        )
                        .dynamicTypeSize(...Theme.interfaceLargestTypeSize)
                        .accessibilityIdentifier("extension-scene-cover")
                        .background(Theme.pageBackground)
                        .navigationTitle(coverTitle)
                        .navigationBarTitleDisplayMode(.inline)
                        .toolbar { SheetClose(id: "extension-scene-close") { fullScreen = false } }
                        .focusable()
                        .focused($coverFocused)
                        .onKeyPress(.escape) {
                            fullScreen = false
                            return .handled
                        }
                        .accessibilityAction(.escape) { fullScreen = false }
                        .onAppear { coverFocused = true }
                        .onChange(of: (controller as? ExtensionViewModel)?.pushed) { _, pushed in
                            if pushed != nil { fullScreen = false }
                        }
                        .onChange(of: (controller as? ExtensionViewModel)?.popped) { _, popped in
                            if popped == true { fullScreen = false }
                        }
                        .onChange(of: (controller as? ExtensionViewModel)?.confirmation != nil) { _, asking in
                            if asking { fullScreen = false }
                        }
                        .onChange(of: (controller as? AssistantViewModel)?.page) { _, _ in
                            fullScreen = false
                        }
                        .onChange(of: (controller as? AssistantViewModel)?.pending != nil) { _, pending in
                            if pending { fullScreen = false }
                        }
                    }
                }
            }
    }

    @ViewBuilder private var content: some View {
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            if compact {
                if picture {
                    let pictureModel = ExtensionSceneModel.picture(scene)
                    ExtensionSceneStillPreview(scene: scene, model: pictureModel, synchronous: true)
                        .scaledFrame(height: Theme.Size.scenePreviewHeight)
                        .clipShape(.rect(cornerRadius: Theme.Radius.card, style: .continuous))
                } else {
                    ZStack {
                        Group {
                            if let model, model.prepared != nil {
                                ExtensionSceneStillPreview(scene: scene, model: model, synchronous: false)
                            } else {
                                Rectangle().fill(Theme.Fill.well).overlay { ProgressView() }
                            }
                        }
                        .allowsHitTesting(false)
                        .accessibilityHidden(true)

                        Button(action: openFullScreen) {
                            Color.clear.contentShape(.rect)
                        }
                        .buttonStyle(.plain)
                        .accessibilityElement(children: .ignore)
                        .accessibilityLabel(String(localized: "Open Scene Full Screen"))
                        .accessibilityValue(scene.alt)
                        .accessibilityIdentifier("extension-scene-preview")
                    }
                    .scaledFrame(height: Theme.Size.scenePreviewHeight)
                    .clipShape(.rect(cornerRadius: Theme.Radius.card, style: .continuous))
                }
            } else {
                ZStack {
                    if let model, model.prepared != nil {
                        ExtensionSceneCanvas(
                            scene: scene,
                            model: model,
                            controller: controller,
                            link: $link,
                            covered: fullScreen,
                            inCover: false,
                            showsFullScreen: true,
                            onFullScreen: openFullScreen,
                            onPinPush: { fullScreen = false },
                            onDeferAction: { $0() }
                        )
                    } else {
                        ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity)
                    }
                }
                .background(Theme.Scene.ground)
                .clipShape(.rect(cornerRadius: Theme.Radius.card, style: .continuous))
                .overlay(
                    RoundedRectangle(cornerRadius: Theme.Radius.card, style: .continuous)
                        .stroke(Theme.cardStroke, lineWidth: Theme.Size.hairline)
                )
                .padding(.horizontal, cardInset == nil ? Theme.Space.page : Theme.Space.flush)
                .onGeometryChange(for: CGFloat.self) { $0.size.width } action: { pageWidth = $0 }
                .containerRelativeFrame(.vertical) { visible, _ in
                    let scale = Theme.sizeScale(typeSize)
                    var height = visible * Theme.Size.scenePageShare
                    // A flat home leaves the card's top and bottom empty: as tall as the model needs at the width
                    // the card has, at the pitch it opens at (tilting never resizes the card), with room for pins
                    // above and the control bar below.
                    if let bounds = model?.prepared?.bounds, pageWidth > 0 {
                        let phi = ExtensionSceneCameraState.resolved(scene.camera).pitch * .pi / 180
                        let ratio = bounds.fitHalfHeight(pitchRadians: phi, aspect: .infinity) / bounds.planHalfDiagonal
                        height = min(height, pageWidth * ratio / Theme.Scene.fitMargin + Theme.Size.scenePageChrome * scale)
                    }
                    return min(max(height, Theme.Size.scenePageMinHeight * scale), Theme.Size.scenePageMaxHeight * scale)
                }
                .accessibilityIdentifier("extension-scene")
            }

            if let caption = scene.caption, !caption.isEmpty {
                Text(caption)
                    .font(Theme.Font.label)
                    .foregroundStyle(.secondary)
                    .padding(.horizontal, (!compact && cardInset == nil) ? Theme.Space.page : Theme.Space.flush)
            }
        }
    }

    private var coverLinkBinding: Binding<LinkTarget?> {
        Binding(
            get: { deferredLink ?? link },
            set: { target in
                if let target {
                    if fullScreen {
                        deferredLink = target
                        fullScreen = false
                    } else {
                        link = target
                    }
                } else {
                    deferredLink = nil
                    link = nil
                }
            }
        )
    }

    private func handleCoverDismiss() {
        if let target = deferredLink {
            deferredLink = nil
            link = target
        }
        if let action = deferredAction {
            deferredAction = nil
            action()
        }
    }

    private var persistenceNamespace: String {
        guard let controller else { return "" }
        let base: String
        if let page = controller as? ExtensionViewModel {
            base = page.graphPersistenceNamespace
        } else if let card = controller as? AssistantViewModel {
            base = ["assistant", card.scope.group, card.scope.conversation, card.key, card.page ?? ""].joined(separator: "\n")
        } else {
            return ""
        }
        return base + "\n" + (compact ? "compact" : "page")
    }

    private func openFullScreen() {
        fullScreen = true
    }

    private var coverTitle: String {
        if let title = (controller as? ExtensionViewModel)?.view?.title, !title.isEmpty { return title }
        if let title = (controller as? AssistantViewModel)?.view.title, !title.isEmpty { return title }
        if let first = scene.labels?.first?.text, !first.isEmpty { return first }
        return String(localized: "Scene")
    }
}

/// Still preview for compact cards and Share-as-Image (`ImageRenderer`).
private struct ExtensionSceneStillPreview: View {
    var scene: ExtensionSceneComponent
    var model: ExtensionSceneModel
    var synchronous: Bool

    @Environment(\.colorScheme) private var colorScheme
    @Environment(\.displayScale) private var displayScale
    @State private var asyncImage: UIImage?

    var body: some View {
        GeometryReader { geometry in
            let dark = colorScheme == .dark
            let size = CGSize(width: max(geometry.size.width, 1), height: max(geometry.size.height, 1))
            let prepared = model.prepared ?? ExtensionSceneGeometryBuilder.build(scene.shapes)
            let projection = ExtensionSceneProjection(bounds: prepared.bounds, camera: model.camera, viewport: size)

            ZStack(alignment: .topLeading) {
                Theme.Scene.ground
                if synchronous {
                    if let snap = ExtensionSceneModel.renderPictureSnapshot(
                        prepared: prepared,
                        pins: scene.pins ?? [],
                        camera: model.camera,
                        size: size,
                        scale: max(displayScale, 1),
                        dark: dark
                    ) {
                        Image(uiImage: snap).resizable().scaledToFill()
                    }
                } else if let asyncImage {
                    Image(uiImage: asyncImage).resizable().scaledToFill()
                }
                ExtensionSceneLabelsLayer(labels: scene.labels ?? [], projection: projection, scale: 1, avoid: [])
                ExtensionScenePreviewDotsLayer(pins: scene.pins ?? [], projection: projection)
            }
            .frame(width: geometry.size.width, height: geometry.size.height)
            .clipped()
            .task(id: ExtensionSceneSnapshotInput(
                geometryVersion: model.geometryVersion,
                lightVersion: model.lightVersion,
                camera: model.camera,
                dark: dark,
                width: Int(round(size.width)),
                height: Int(round(size.height))
            )) {
                guard !synchronous else { return }
                asyncImage = await ExtensionSceneModel.renderPreviewSnapshot(
                    prepared: prepared,
                    pins: scene.pins ?? [],
                    camera: model.camera,
                    size: size,
                    scale: max(displayScale, 1),
                    dark: dark
                )
            }
        }
    }
}

private struct ExtensionScenePreviewDotsLayer: View {
    var pins: [ExtensionScenePin]
    var projection: ExtensionSceneProjection

    var body: some View {
        ZStack(alignment: .topLeading) {
            ForEach(pins, id: \.id) { pin in
                if pin.at.count >= 2 {
                    let projected = projection.project(planX: pin.at[0], planY: pin.at[1], height: pin.height ?? 0)
                    let dotColor = ExtensionScenePinStyle.dotColor(for: pin)
                    Circle()
                        .fill(dotColor)
                        .scaledFrame(width: Theme.Size.dot, height: Theme.Size.dot)
                        .padding(Theme.Space.xxs)
                        .modifier(ExtensionSceneOpaquePill())
                        .position(projected.point)
                        .zIndex(-projected.depth)
                }
            }
        }
        .allowsHitTesting(false)
        .accessibilityHidden(true)
    }
}

/// Interactive SceneKit viewport + closed-form projected room labels, device pins, and camera controls.
private struct ExtensionSceneCanvas: View {
    var scene: ExtensionSceneComponent
    var model: ExtensionSceneModel
    var controller: (any ExtensionViewControlling)?
    @Binding var link: LinkTarget?
    var covered: Bool
    var inCover: Bool
    var showsFullScreen: Bool
    var onFullScreen: () -> Void
    var onPinPush: () -> Void
    var onDeferAction: (@escaping () -> Void) -> Void

    @Environment(\.colorScheme) private var colorScheme
    @Environment(\.dynamicTypeSize) private var typeSize
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var tabVisible = true

    var body: some View {
        GeometryReader { geometry in
            if let prepared = model.prepared {
                let size = CGSize(width: max(geometry.size.width, 1), height: max(geometry.size.height, 1))
                let projection = ExtensionSceneProjection(bounds: prepared.bounds, camera: model.camera, viewport: size)
                let pins = scene.pins ?? []
                let labels = scene.labels ?? []
                let scale = Theme.sizeScale(typeSize)
                let placements = Self.placePins(pins, projection: projection, scale: scale)

                ZStack(alignment: .topLeading) {
                    ExtensionSceneKitHost(
                        prepared: prepared,
                        geometryVersion: model.geometryVersion,
                        lightVersion: model.lightVersion,
                        pins: pins,
                        camera: model.camera,
                        projection: projection,
                        dark: colorScheme == .dark,
                        active: !covered && tabVisible,
                        onOrbit: { dYaw, dPitch in
                            model.orbit(deltaYaw: dYaw, deltaPitch: dPitch)
                        },
                        onZoomFactor: { factor in
                            model.zoom(factor, persist: false)
                        },
                        onGestureEnded: { coalesced in
                            model.persistCurrent(coalesced: coalesced)
                        }
                    )
                    .accessibilityHidden(true)

                    ExtensionSceneSummaryAccessibility(
                        alt: scene.alt,
                        labels: labels,
                        sortPriority: Double(pins.count + 10),
                        onOrbit: { dYaw, dPitch in
                            applyAnimated(reduceMotion: reduceMotion) {
                                model.orbit(deltaYaw: dYaw, deltaPitch: dPitch, persist: true)
                            }
                        },
                        onReset: {
                            applyAnimated(reduceMotion: reduceMotion) {
                                model.reset()
                            }
                        }
                    )

                    ExtensionSceneLabelsLayer(labels: labels, projection: projection, scale: scale,
                                              avoid: placements.values.map(\.rect))

                    ZStack(alignment: .topLeading) {
                        ForEach(Array(pins.enumerated()), id: \.element.id) { index, pin in
                            if let placement = placements[pin.id] {
                                ExtensionScenePlacedPin(
                                    pin: pin,
                                    collapsed: placement.collapsed,
                                    inCover: inCover,
                                    controller: controller,
                                    link: $link,
                                    reduceMotion: reduceMotion,
                                    onPinPush: onPinPush,
                                    onDeferAction: onDeferAction
                                )
                                .position(placement.point)
                                .zIndex(max(1, Theme.Scene.pinLayerBase - placement.depth))
                                .accessibilitySortPriority(Double(pins.count - index))
                            }
                        }
                    }
                }
                .frame(width: geometry.size.width, height: geometry.size.height, alignment: .topLeading)
                .clipped()
                .overlay(alignment: .bottomTrailing) {
                    controls
                        .padding(Theme.Space.page)
                        .accessibilitySortPriority(0)
                }
                .accessibilityElement(children: .contain)
                .onTabVisibility(appear: { tabVisible = true }, disappear: { tabVisible = false })
                // Full screen on an upright phone opens steeper (#14335); the card keeps the scene's own pitch.
                .onChange(of: inCover && size.height > size.width, initial: true) { _, tall in
                    if inCover { model.setTallView(tall) }
                }
                .onDisappear {
                    if inCover { model.setTallView(false) }
                }
            }
        }
    }

    private var controls: some View {
        let atDefault = !model.touched && model.camera == model.defaultCamera
        return HStack(spacing: Theme.Space.flush) {
            control("extension-scene-zoom-out", label: String(localized: "Zoom Out"), icon: Theme.Icon.zoomOut,
                    disabled: model.camera.zoom <= Theme.Scene.minZoom) {
                applyAnimated(reduceMotion: reduceMotion) {
                    model.zoom(1 / Theme.Size.sceneZoomStep, persist: true)
                }
            }
            control("extension-scene-zoom-in", label: String(localized: "Zoom In"), icon: Theme.Icon.zoomIn,
                    disabled: model.camera.zoom >= Theme.Scene.maxZoom) {
                applyAnimated(reduceMotion: reduceMotion) {
                    model.zoom(Theme.Size.sceneZoomStep, persist: true)
                }
            }
            control("extension-scene-reset", label: String(localized: "Reset View"), icon: Theme.Icon.resetDefaults,
                    disabled: atDefault) {
                applyAnimated(reduceMotion: reduceMotion) {
                    model.reset()
                }
            }
            if showsFullScreen {
                control("extension-scene-full-screen", label: String(localized: "Full Screen"), icon: Theme.Icon.enlarge) {
                    onFullScreen()
                }
            }
        }
        .padding(Theme.Space.xs)
        .chromeSurface(in: Capsule())
    }

    private func control(_ identifier: String, label: String, icon: String, disabled: Bool = false,
                         action: @escaping () -> Void) -> some View {
        Button(action: action) { ViewerIcon(symbol: icon, enabled: !disabled) }
            .buttonStyle(.plain)
            .disabled(disabled)
            .accessibilityLabel(label)
            .accessibilityShowsLargeContentViewer { Text(label) }
            .accessibilityIdentifier(identifier)
    }

    private func applyAnimated(reduceMotion: Bool, _ updates: () -> Void) {
        if reduceMotion {
            updates()
        } else {
            withAnimation(Theme.Motion.state) { updates() }
        }
    }

    private struct PinPlacement {
        var point: CGPoint
        var depth: Double
        var collapsed: Bool
        var rect: CGRect
    }

    /// Front-to-back screen-space collision pass: frontmost pins keep their full label+state pill, while tightly
    /// overlapping pins behind them collapse to a compact icon+dot pill without losing their single-tap action.
    private static func placePins(_ pins: [ExtensionScenePin], projection: ExtensionSceneProjection,
                                  scale: CGFloat) -> [String: PinPlacement] {
        var raw: [(pin: ExtensionScenePin, point: CGPoint, depth: Double)] = []
        raw.reserveCapacity(pins.count)
        for pin in pins where pin.at.count >= 2 {
            let p = projection.project(planX: pin.at[0], planY: pin.at[1], height: pin.height ?? 0)
            raw.append((pin, p.point, p.depth))
        }
        let frontToBack = raw.sorted { $0.depth < $1.depth }
        var occupied: [CGRect] = []
        occupied.reserveCapacity(frontToBack.count)
        var out: [String: PinPlacement] = [:]
        out.reserveCapacity(frontToBack.count)

        let pillHeight = Theme.Size.scenePinHeight * scale
        for item in frontToBack {
            let hasIcon = item.pin.icon?.isEmpty == false
            let hasState = item.pin.state?.isEmpty == false
            let compactWidth = (hasIcon ? Theme.Size.scenePinCompactWidth : Theme.Size.scenePinBaseWidth) * scale
            let textPts = estimatedTextWidth(item.pin.label) + estimatedTextWidth(item.pin.state ?? "")
            let iconPts = hasIcon ? Theme.Size.scenePinIconWidth : 0
            let stateGapPts = hasState ? Theme.Size.scenePinStateGap : 0
            let fullWidth = (Theme.Size.scenePinBaseWidth + iconPts + stateGapPts + min(textPts, Theme.Size.scenePinTextMaxWidth)) * scale
            let candidate = projection.keptInside(CGRect(x: item.point.x - fullWidth * 0.5, y: item.point.y - pillHeight * 0.5,
                                                         width: fullWidth, height: pillHeight), anchor: item.point)
            let collides = occupied.contains { $0.intersects(candidate) }
            let chosenRect = collides
                ? projection.keptInside(CGRect(x: item.point.x - compactWidth * 0.5, y: item.point.y - pillHeight * 0.5,
                                               width: compactWidth, height: pillHeight), anchor: item.point)
                : candidate
            occupied.append(chosenRect)
            out[item.pin.id] = PinPlacement(point: CGPoint(x: chosenRect.midX, y: chosenRect.midY), depth: item.depth,
                                            collapsed: collides, rect: chosenRect)
        }
        return out
    }

    fileprivate static func estimatedTextWidth(_ text: String) -> CGFloat {
        var total: CGFloat = 0
        for scalar in text.unicodeScalars {
            total += scalar.value >= 0x2E80 ? Theme.Size.scenePinWideCharWidth : Theme.Size.scenePinCharWidth
        }
        return total
    }
}

private struct ExtensionSceneSummaryAccessibility: View {
    var alt: String
    var labels: [ExtensionSceneLabel]
    var sortPriority: Double
    var onOrbit: (Double, Double) -> Void
    var onReset: () -> Void

    private var spokenLabel: String {
        let roomNames = labels.map(\.text).filter { !$0.isEmpty }
        guard !roomNames.isEmpty else { return alt }
        return alt + ". " + roomNames.joined(separator: ", ")
    }

    var body: some View {
        Color.clear
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .allowsHitTesting(false)
            .accessibilityElement(children: .ignore)
            .accessibilityLabel(spokenLabel)
            .accessibilityIdentifier("extension-scene-summary")
            .accessibilitySortPriority(sortPriority)
            .accessibilityAction(named: String(localized: "Turn Left")) {
                onOrbit(-Theme.Scene.accessibilityYawStep, 0)
            }
            .accessibilityAction(named: String(localized: "Turn Right")) {
                onOrbit(Theme.Scene.accessibilityYawStep, 0)
            }
            .accessibilityAction(named: String(localized: "Tilt Up")) {
                onOrbit(0, Theme.Scene.accessibilityPitchStep)
            }
            .accessibilityAction(named: String(localized: "Tilt Down")) {
                onOrbit(0, -Theme.Scene.accessibilityPitchStep)
            }
            .accessibilityAction(named: String(localized: "Reset View"), onReset)
    }
}

/// Room labels on the floor. Device pins come first: a label that would cover a pin (or another label) moves just
/// below it, else just above it, and is left out when neither is clear (VoiceOver still reads every room).
struct ExtensionSceneLabelsLayer: View {
    var labels: [ExtensionSceneLabel]
    var projection: ExtensionSceneProjection
    var scale: CGFloat
    var avoid: [CGRect]

    struct Placed {
        var text: String
        var point: CGPoint
        var depth: Double
    }

    static func place(_ labels: [ExtensionSceneLabel], projection: ExtensionSceneProjection, scale: CGFloat,
                      avoid: [CGRect]) -> [Placed] {
        var occupied = avoid
        var out: [Placed] = []
        let height = Theme.Size.sceneLabelHeight * scale
        for label in labels where !label.text.isEmpty && label.at.count >= 2 {
            let projected = projection.project(planX: label.at[0], planY: label.at[1], height: label.height ?? 0.02)
            let width = (ExtensionSceneCanvas.estimatedTextWidth(label.text) + 2 * Theme.Space.s) * scale
            let rect = { (point: CGPoint) in
                CGRect(x: point.x - width * 0.5, y: point.y - height * 0.5, width: width, height: height)
            }
            var candidates = [projected.point]
            if let hit = occupied.first(where: { $0.intersects(rect(projected.point)) }) {
                // just below what is in the way, then just above it
                candidates.append(CGPoint(x: projected.point.x, y: hit.maxY + height * 0.5 + Theme.Space.xxs))
                candidates.append(CGPoint(x: projected.point.x, y: hit.minY - height * 0.5 - Theme.Space.xxs))
            }
            if let point = candidates.first(where: { c in !occupied.contains { $0.intersects(rect(c)) } }) {
                occupied.append(rect(point))
                out.append(Placed(text: label.text, point: point, depth: projected.depth))
            }
        }
        return out
    }

    var body: some View {
        ZStack(alignment: .topLeading) {
            ForEach(Array(Self.place(labels, projection: projection, scale: scale, avoid: avoid).enumerated()), id: \.offset) { _, placed in
                Text(placed.text)
                    .font(Theme.Font.labelStrong)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                    .padding(.horizontal, Theme.Space.s)
                    .padding(.vertical, Theme.Space.xxs)
                    .modifier(ExtensionSceneOpaquePill())
                    .position(placed.point)
                    .zIndex(-placed.depth - 100)
            }
        }
        .allowsHitTesting(false)
        .accessibilityHidden(true)
    }
}

private enum ExtensionScenePinStyle {
    static func dotColor(for pin: ExtensionScenePin) -> Color {
        if let tone = pin.tone, tone != "neutral" {
            return ExtensionUIStyle.tone(tone)
        }
        if let named = ExtensionUIStyle.color(pin.color) {
            return named
        }
        return Color.secondary
    }
}

/// Opaque style-aware capsule surface so pin text keeps >= 4.5:1 contrast over any `#RRGGBB` 3D shape in both modes.
private struct ExtensionSceneOpaquePill: ViewModifier {
    func body(content: Content) -> some View {
        content
            .background(Theme.Scene.ground, in: .capsule)
            .overlay(Capsule().stroke(Theme.cardStroke, lineWidth: Theme.Size.hairline))
    }
}

private struct ExtensionScenePlacedPin: View {
    @Environment(\.extensionDisplayedRevision) private var displayed
    @Environment(AppModel.self) private var app
    var pin: ExtensionScenePin
    var collapsed: Bool
    var inCover: Bool
    var controller: (any ExtensionViewControlling)?
    @Binding var link: LinkTarget?
    var reduceMotion: Bool
    var onPinPush: () -> Void
    var onDeferAction: (@escaping () -> Void) -> Void

    private var dotColor: Color { ExtensionScenePinStyle.dotColor(for: pin) }
    private var hasAccent: Bool {
        (pin.tone != nil && pin.tone != "neutral") || ExtensionUIStyle.color(pin.color) != nil
    }

    var body: some View {
        Group {
            if let action = pin.action, let controller {
                if inCover && action.confirm?.isEmpty == false && controller.managesConfirmation {
                    let rev = displayed
                    let currentApp = app
                    let linkBinding = $link
                    Button {
                        let execute = controller.prepared(action, with: [:], displayed: rev)
                        onDeferAction {
                            Task { @MainActor in
                                ExtensionOutcome.show(await execute(), app: currentApp, link: linkBinding)
                            }
                        }
                    } label: {
                        face
                    }
                    .buttonStyle(.plain)
                    .disabled(!controller.isEnabled(action))
                    .accessibilityIdentifier("extension-scene-pin-\(pin.id)")
                } else {
                    ExtensionActionButton(
                        action: action,
                        title: pin.label,
                        model: controller,
                        values: [:],
                        link: $link,
                        identifier: "extension-scene-pin-\(pin.id)",
                        onPush: onPinPush
                    ) {
                        face
                    }
                    .buttonStyle(.plain)
                }
            } else {
                face.accessibilityIdentifier("extension-scene-pin-\(pin.id)")
            }
        }
        .accessibilityShowsLargeContentViewer {
            VStack {
                Text(pin.label)
                if let state = pin.state, !state.isEmpty { Text(state) }
            }
        }
        .contextMenu {
            Text(pin.label)
            if let state = pin.state, !state.isEmpty { Text(state) }
        }
        .animation(reduceMotion ? nil : Theme.Motion.state, value: pin.state)
        .animation(reduceMotion ? nil : Theme.Motion.state, value: pin.tone)
    }

    private var face: some View {
        HStack(spacing: Theme.Space.xs) {
            Circle()
                .fill(dotColor)
                .scaledFrame(width: Theme.Size.dot, height: Theme.Size.dot)
            if let icon = pin.icon, !icon.isEmpty {
                ThemedIcon(ExtensionUIStyle.icon(icon))
                    .font(Theme.Font.labelStrong)
                    .foregroundStyle(hasAccent ? dotColor : Color.secondary)
            }
            if !collapsed {
                Text(pin.label)
                    .font(Theme.Font.labelStrong)
                    .foregroundStyle(Color.primary)
                    .lineLimit(1)
                if let state = pin.state, !state.isEmpty {
                    Text(state)
                        .font(Theme.Font.label)
                        .foregroundStyle(Color.secondary)
                        .lineLimit(1)
                }
            }
        }
        .padding(.horizontal, Theme.Space.s)
        .padding(.vertical, Theme.Space.xs)
        .modifier(ExtensionSceneOpaquePill())
        .fixedSize()
        .tapTargetHeight()
        .scaledFrame(minWidth: Theme.Size.tapTarget)
        .frame(minWidth: Theme.Size.tapTarget)
        .contentShape(.rect)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(pin.label)
        .accessibilityValue(pin.state ?? "")
    }
}

/// UIKit SceneKit host with demand-only rendering (`rendersContinuously = false`, P9) and direct UIKit pan/pinch
/// recognizers so vertical pitch drags never scroll the enclosing `SettingsForm` list and leading-edge drags preserve
/// system navigation swipe-back (`Theme.Size.edgeBackZone`).
private struct ExtensionSceneKitHost: UIViewRepresentable {
    var prepared: ExtensionScenePreparedGeometry
    var geometryVersion: Int
    var lightVersion: Int
    var pins: [ExtensionScenePin]
    var camera: ExtensionSceneCameraState
    var projection: ExtensionSceneProjection
    var dark: Bool
    var active: Bool
    var onOrbit: (Double, Double) -> Void
    var onZoomFactor: (Double) -> Void
    var onGestureEnded: (Bool) -> Void

    func makeCoordinator() -> Coordinator {
        Coordinator(onOrbit: onOrbit, onZoomFactor: onZoomFactor, onGestureEnded: onGestureEnded)
    }

    func makeUIView(context: Context) -> SCNView {
        let device = MTLCreateSystemDefaultDevice()
        let scnView = SCNView(frame: .zero, options: nil)
        scnView.rendersContinuously = false
        scnView.isPlaying = false
        scnView.loops = false
        scnView.allowsCameraControl = false
        scnView.antialiasingMode = ExtensionSceneGeometryBuilder.bestAntialiasingMode(device: device)

        let rig = ExtensionSceneGeometryBuilder.makeRig()
        scnView.scene = rig.scene
        scnView.pointOfView = rig.cameraNode
        context.coordinator.rig = rig

        let pan = UIPanGestureRecognizer(target: context.coordinator, action: #selector(Coordinator.handlePan(_:)))
        pan.allowedScrollTypesMask = [.continuous, .discrete]
        pan.allowedTouchTypes = [
            NSNumber(value: UITouch.TouchType.direct.rawValue),
            NSNumber(value: UITouch.TouchType.indirectPointer.rawValue),
        ]
        pan.delegate = context.coordinator
        scnView.addGestureRecognizer(pan)
        context.coordinator.panRecognizer = pan

        let pinch = UIPinchGestureRecognizer(target: context.coordinator, action: #selector(Coordinator.handlePinch(_:)))
        pinch.delegate = context.coordinator
        scnView.addGestureRecognizer(pinch)
        context.coordinator.pinchRecognizer = pinch

        return scnView
    }

    func updateUIView(_ scnView: SCNView, context: Context) {
        let coordinator = context.coordinator
        coordinator.onOrbit = onOrbit
        coordinator.onZoomFactor = onZoomFactor
        coordinator.onGestureEnded = onGestureEnded
        guard let rig = coordinator.rig else { return }

        var dirty = false
        if coordinator.installedGeometryVersion != geometryVersion {
            coordinator.installedGeometryVersion = geometryVersion
            ExtensionSceneGeometryBuilder.installGeometry(prepared, into: rig)
            dirty = true
        }
        if coordinator.installedLightVersion != lightVersion || coordinator.installedDark != dark {
            coordinator.installedLightVersion = lightVersion
            coordinator.installedDark = dark
            ExtensionSceneGeometryBuilder.updateLighting(
                in: rig, bounds: prepared.bounds, floorTop: prepared.floorTop, pins: pins, dark: dark
            )
            scnView.backgroundColor = ExtensionSceneGeometryBuilder.resolvedBackgroundColor(dark: dark)
            dirty = true
        }
        if coordinator.installedProjection != projection {
            coordinator.installedProjection = projection
            ExtensionSceneGeometryBuilder.updateCamera(in: rig, projection: projection)
            dirty = true
        }
        if dirty {
            coordinator.needsDisplay = true
        }
        if coordinator.needsDisplay && active {
            coordinator.needsDisplay = false
            scnView.setNeedsDisplay()
        }
    }

    static func dismantleUIView(_ uiView: SCNView, coordinator: Coordinator) {
        uiView.scene = nil
        coordinator.rig = nil
    }

    final class Coordinator: NSObject, UIGestureRecognizerDelegate {
        var rig: ExtensionSceneGeometryBuilder.Rig?
        var installedGeometryVersion = -1
        var installedLightVersion = -1
        var installedDark: Bool?
        var installedProjection: ExtensionSceneProjection?
        var needsDisplay = true

        var onOrbit: (Double, Double) -> Void
        var onZoomFactor: (Double) -> Void
        var onGestureEnded: (Bool) -> Void
        weak var panRecognizer: UIPanGestureRecognizer?
        weak var pinchRecognizer: UIPinchGestureRecognizer?
        private var moved = false
        private var usedWheel = false

        init(onOrbit: @escaping (Double, Double) -> Void,
             onZoomFactor: @escaping (Double) -> Void,
             onGestureEnded: @escaping (Bool) -> Void) {
            self.onOrbit = onOrbit
            self.onZoomFactor = onZoomFactor
            self.onGestureEnded = onGestureEnded
        }

        @objc func handlePan(_ recognizer: UIPanGestureRecognizer) {
            switch recognizer.state {
            case .began:
                moved = false
                usedWheel = false
                fallthrough
            case .changed:
                guard let view = recognizer.view else { return }
                let delta = recognizer.translation(in: view)
                recognizer.setTranslation(.zero, in: view)
                guard delta != .zero else { return }
                moved = true
                let modified = recognizer.modifierFlags.contains(.command) || recognizer.modifierFlags.contains(.control)
                if modified, recognizer.numberOfTouches == 0, recognizer.buttonMask.isEmpty {
                    usedWheel = true
                    onZoomFactor(exp(Double(delta.y) * Theme.Size.sceneWheelZoomSensitivity))
                } else {
                    if recognizer.numberOfTouches == 0, recognizer.buttonMask.isEmpty { usedWheel = true }
                    let sens = Theme.Size.sceneOrbitSensitivity
                    onOrbit(-Double(delta.x) * sens, Double(delta.y) * sens)
                }
            case .ended, .cancelled, .failed:
                if moved { onGestureEnded(usedWheel) }
                moved = false
                usedWheel = false
            default:
                break
            }
        }

        @objc func handlePinch(_ recognizer: UIPinchGestureRecognizer) {
            switch recognizer.state {
            case .began, .changed:
                let factor = Double(recognizer.scale)
                recognizer.scale = 1
                guard factor.isFinite, factor > 0, abs(factor - 1) > 0.0005 else { return }
                onZoomFactor(factor)
            case .ended, .cancelled, .failed:
                onGestureEnded(false)
            default:
                break
            }
        }

        func gestureRecognizerShouldBegin(_ gestureRecognizer: UIGestureRecognizer) -> Bool {
            guard gestureRecognizer === panRecognizer, let view = gestureRecognizer.view else { return true }
            if gestureRecognizer.numberOfTouches > 0 {
                let start = gestureRecognizer.location(in: view)
                let leadingDistance = view.effectiveUserInterfaceLayoutDirection == .rightToLeft
                    ? (view.bounds.width - start.x) : start.x
                if leadingDistance <= Theme.Size.edgeBackZone {
                    return false
                }
            }
            return true
        }

        func gestureRecognizer(_ gestureRecognizer: UIGestureRecognizer,
                               shouldRecognizeSimultaneouslyWith otherGestureRecognizer: UIGestureRecognizer) -> Bool {
            let ours = (gestureRecognizer === panRecognizer || gestureRecognizer === pinchRecognizer)
            let otherOurs = (otherGestureRecognizer === panRecognizer || otherGestureRecognizer === pinchRecognizer)
            return ours && otherOurs
        }
    }
}
#endif
