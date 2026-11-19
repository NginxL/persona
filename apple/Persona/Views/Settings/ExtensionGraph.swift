#if os(iOS)
import SwiftUI
import UIKit
import PersonaWire

/// Reload when either the component or its hub/page namespace changes.
private struct ExtensionGraphInput: Hashable {
    var graph: ExtensionGraphComponent
    var namespace: String
}

/// UI version 10's native graph canvas. Layout and restoration belong to `ExtensionGraphModel`; this file only draws
/// the prepared scene and turns gestures into transform or fold changes. Compact cards are still previews which open
/// the same model in a full-screen cover, never a second graph with separate state.
struct ExtensionGraph: View {
    var graph: ExtensionGraphComponent
    var controller: (any ExtensionViewControlling)?
    @Binding var link: LinkTarget?

    @Environment(\.pluginUICompact) private var compact
    @Environment(\.extensionPicture) private var picture
    @Environment(\.dynamicTypeSize) private var typeSize
    @State private var model: ExtensionGraphModel?
    @State private var fullScreen = false
    @FocusState private var coverFocused: Bool

    var body: some View {
        content
            .task(id: ExtensionGraphInput(graph: graph, namespace: persistenceNamespace)) {
                if model == nil || model?.componentKey != (graph.key ?? "") || model?.namespace != persistenceNamespace {
                    model = ExtensionGraphModel(graph: graph, namespace: persistenceNamespace, keeps: controller != nil)
                }
                await model?.reload(graph)
            }
            .fullScreenCover(isPresented: $fullScreen) {
                NavigationStack {
                    if let model {
                        ExtensionGraphCanvas(graph: graph, model: model, controller: controller, link: $link,
                                             preview: false, picture: false, covered: false, showsFullScreen: false,
                                             onFullScreen: openFullScreen, onNodePush: { fullScreen = false })
                            .accessibilityIdentifier("extension-graph-cover")
                            .background(Theme.pageBackground)
                            .navigationTitle(coverTitle(model))
                            .navigationBarTitleDisplayMode(.inline)
                            .toolbar { SheetClose(id: "extension-graph-close") { fullScreen = false } }
                            .focusable()
                            .focused($coverFocused)
                            .onKeyPress(.escape) {
                                fullScreen = false
                                return .handled
                            }
                            .accessibilityAction(.escape) { fullScreen = false } // VoiceOver's scrub, as on a sheet
                            .onAppear { coverFocused = true }
                            .onChange(of: (controller as? ExtensionViewModel)?.pushed) { _, pushed in
                                // A node's push belongs to the page's navigation stack, not to a cover left over it.
                                if pushed != nil { fullScreen = false }
                            }
                    }
                }
            }
    }

    @ViewBuilder private var content: some View {
        if compact {
            if picture {
                let model = ExtensionGraphModel.picture(graph)
                ExtensionGraphCanvas(graph: graph, model: model, controller: nil, link: $link,
                                     preview: true, picture: true, covered: false, showsFullScreen: false,
                                     onFullScreen: {}, onNodePush: {})
                    .scaledFrame(height: Theme.Size.graphPreviewHeight)
            } else {
                ZStack {
                    canvas(preview: true)
                        .allowsHitTesting(false)
                        .accessibilityHidden(true)
                    Button(action: openFullScreen) {
                        Color.clear.contentShape(.rect)
                    }
                    .buttonStyle(.plain)
                    .accessibilityElement(children: .ignore)
                    .accessibilityLabel(String(localized: "Open Graph Full Screen"))
                    .accessibilityIdentifier("extension-graph-preview")
                }
                .scaledFrame(height: Theme.Size.graphPreviewHeight)
            }
        } else {
            canvas(preview: false)
                .containerRelativeFrame(.vertical) { visible, _ in
                    let scale = Theme.sizeScale(typeSize)
                    return min(max(visible * Theme.Size.graphPageShare,
                                   Theme.Size.graphPageMinHeight * scale), Theme.Size.graphPageMaxHeight * scale)
                }
                .accessibilityIdentifier("extension-graph")
        }
    }

    private func canvas(preview: Bool) -> some View {
        ZStack {
            if let model, model.layout != nil {
                ExtensionGraphCanvas(graph: graph, model: model, controller: controller, link: $link,
                                     preview: preview, picture: false, covered: fullScreen, showsFullScreen: !preview,
                                     onFullScreen: openFullScreen, onNodePush: { fullScreen = false })
            } else {
                ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity)
            }
        }
        .background(Theme.pageBackground)
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
        model?.requestOpeningFit()
        fullScreen = true
    }

    private func coverTitle(_ model: ExtensionGraphModel) -> String {
        guard let first = model.layout?.order.first, let node = model.nodesByID[first] else {
            return String(localized: "Graph")
        }
        return node.title
    }
}

/// One interactive canvas. The whole prepared scene is transformed as one layer, while fold controls stay 44 pt hit
/// targets in screen points. Edges are one Canvas, not one view per edge.
private struct ExtensionGraphCanvas: View {
    var graph: ExtensionGraphComponent
    var model: ExtensionGraphModel
    var controller: (any ExtensionViewControlling)?
    @Binding var link: LinkTarget?
    var preview: Bool
    /// A synchronous still for ImageRenderer: its transform is computed from the viewport, never restored or animated.
    var picture: Bool
    /// True for the inline canvas while its full-screen cover is in front. An untouched inline canvas refits on return.
    var covered: Bool
    var showsFullScreen: Bool
    var onFullScreen: () -> Void
    var onNodePush: () -> Void

    @Environment(\.dynamicTypeSize) private var typeSize
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var dragStart: ExtensionGraphTransform?
    @State private var pinchStart: ExtensionGraphTransform?

    private var nodeSide: CGFloat { Theme.Size.graphNodeWidth * Theme.sizeScale(typeSize) }
    private var nodeHeight: CGFloat { Theme.Size.graphNodeHeight * Theme.sizeScale(typeSize) }
    private var gapX: CGFloat { Theme.Size.graphNodeGap * Theme.sizeScale(typeSize) }
    private var gapY: CGFloat { Theme.Size.graphNodeGap * Theme.sizeScale(typeSize) }
    private var margin: CGFloat { Theme.Space.xl * Theme.sizeScale(typeSize) }

    var body: some View {
        GeometryReader { geometry in
            if let layout = model.layout {
                let content = contentSize(layout)
                let transform = (picture || preview) ? ExtensionGraphTransform.fit(content, in: geometry.size,
                    minimum: Theme.Size.graphPreviewMinScale, maximum: 1) : model.transform
                ZStack(alignment: .topLeading) {
                    Theme.pageBackground
                    scene(layout)
                        .frame(width: content.width, height: content.height, alignment: .topLeading)
                        .scaleEffect(transform.scale, anchor: .topLeading)
                        .offset(x: transform.x, y: transform.y)
                    if !preview {
                        foldControls(layout)
                    }
                }
                .frame(width: geometry.size.width, height: geometry.size.height, alignment: .topLeading)
                .clipped()
                .contentShape(.rect)
                .background {
                    if !preview {
                        ExtensionGraphPointerPan { delta in
                            let current = model.transform
                            model.updateTransform(ExtensionGraphTransform(scale: current.scale,
                                x: current.x + delta.width, y: current.y + delta.height))
                        } onZoom: { factor, point in
                            model.updateTransform(model.transform.zoom(factor, around: point))
                        } onEnded: {
                            model.persistCurrent(coalesced: true)
                        }
                    }
                }
                .simultaneousGesture(dragGesture, including: .all)
                .simultaneousGesture(pinchGesture(geometry.size), including: .all)
                .overlay(alignment: .bottomTrailing) {
                    if !preview {
                        controls(content: content, viewport: geometry.size)
                            .padding(Theme.Space.page)
                    }
                }
                .accessibilityElement(children: .contain)
                .onAppear { fitIfNeeded(layout, content: content, viewport: geometry.size) }
                .onChange(of: model.layoutVersion) { _, _ in
                    fitIfNeeded(layout, content: content, viewport: geometry.size)
                }
                .onChange(of: geometry.size) { _, size in
                    model.requestOpeningFit()
                    fitIfNeeded(layout, content: content, viewport: size)
                }
                .onChange(of: covered) { _, covered in
                    if !covered {
                        model.requestOpeningFit()
                        fitIfNeeded(layout, content: content, viewport: geometry.size)
                    }
                }
            }
        }
        .background(Theme.pageBackground)
    }

    private func scene(_ layout: ExtensionGraphLayouter.Result) -> some View {
        ZStack(alignment: .topLeading) {
            groupFrames(layout)
            edgeLayer(layout)
            ForEach(layout.order, id: \.self) { id in
                if let node = model.nodesByID[id], let point = layout.at[id] {
                    ExtensionGraphPlacedNode(node: node, selected: graph.selected == id, folded: model.folded.contains(id),
                                             parentTitle: model.parentTitles[id], layout: layout, controller: controller,
                                             link: $link, reduceMotion: reduceMotion, onNodePush: onNodePush) {
                        Task { await model.toggleFold(id) }
                    }
                    .scaledFrame(width: Theme.Size.graphNodeWidth, height: Theme.Size.graphNodeHeight)
                    .position(x: margin + point.x * (nodeSide + gapX) + nodeSide / 2,
                              y: margin + point.y * (nodeHeight + gapY) + nodeHeight / 2)
                }
            }
        }
        .animation(reduceMotion ? nil : Theme.Motion.state, value: layout.order)
    }

    private func groupFrames(_ layout: ExtensionGraphLayouter.Result) -> some View {
        ZStack(alignment: .topLeading) {
            ForEach(layout.frames, id: \.self) { frame in
                if let rect = frameRect(frame) {
                    RoundedRectangle(cornerRadius: Theme.Radius.card, style: .continuous)
                        .fill(Theme.Fill.chip)
                        .frame(width: rect.width, height: rect.height)
                        .position(x: rect.midX, y: rect.midY)
                    Text(frame.group)
                        .font(Theme.Font.caption)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                        .position(x: rect.minX + (Theme.Space.m + Theme.Space.l) * Theme.sizeScale(typeSize),
                                  y: rect.minY + Theme.Space.m * Theme.sizeScale(typeSize))
                }
            }
        }
        .accessibilityHidden(true)
    }

    private func edgeLayer(_ layout: ExtensionGraphLayouter.Result) -> some View {
        Canvas { context, _ in
            var path = Path()
            for edge in layout.edges {
                guard let from = layout.at[edge.from], let to = layout.at[edge.to] else { continue }
                let a = nodeRect(from), b = nodeRect(to)
                if graph.direction == ExtensionGraphDirection.down {
                    let start = CGPoint(x: a.midX, y: a.maxY), end = CGPoint(x: b.midX, y: b.minY)
                    let distance = max(abs(end.y - start.y) / 2, Theme.Space.l)
                    path.move(to: start)
                    path.addCurve(to: end,
                                  control1: CGPoint(x: start.x, y: start.y + distance),
                                  control2: CGPoint(x: end.x, y: end.y - distance))
                } else {
                    let start = CGPoint(x: a.maxX, y: a.midY), end = CGPoint(x: b.minX, y: b.midY)
                    let distance = max(abs(end.x - start.x) / 2, Theme.Space.l)
                    path.move(to: start)
                    path.addCurve(to: end,
                                  control1: CGPoint(x: start.x + distance, y: start.y),
                                  control2: CGPoint(x: end.x - distance, y: end.y))
                }
            }
            context.stroke(path, with: .color(Theme.cardStroke), lineWidth: Theme.Size.line)
        }
        .accessibilityHidden(true)
    }

    private func foldControls(_ layout: ExtensionGraphLayouter.Result) -> some View {
        ZStack(alignment: .topLeading) {
            ForEach(layout.order, id: \.self) { id in
                if let hidden = layout.folds[id], let point = layout.at[id] {
                    let rect = nodeRect(point)
                    let folded = model.folded.contains(id)
                    let center = model.transform.apply(to: graph.direction == ExtensionGraphDirection.down
                        ? CGPoint(x: rect.midX, y: rect.maxY)
                        : CGPoint(x: rect.maxX, y: rect.midY))
                    Button {
                        Task { await model.toggleFold(id) }
                    } label: {
                        Group {
                            if folded {
                                Text("+\(hidden)")
                                    .font(Theme.Font.labelStrong)
                                    .lineLimit(1)
                                    .monospacedDigit()
                                    .padding(.horizontal, Theme.Space.xxs)
                            } else {
                                ThemedIcon(Theme.Icon.disclosure)
                                    .font(Theme.Font.labelStrong)
                                    .rotationEffect(.degrees(graph.direction == ExtensionGraphDirection.down ? 90 : 0))
                            }
                        }
                        .foregroundStyle(.secondary)
                        .scaledFrame(minWidth: Theme.Size.graphFoldChip, minHeight: Theme.Size.graphFoldChip)
                        .background(Theme.Fill.chip, in: .capsule)
                        .overlay(Capsule().stroke(Theme.cardStroke, lineWidth: Theme.Size.hairline))
                        .tapTargetFrame()
                        .contentShape(.rect)
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel(foldLabel(id: id, hidden: hidden))
                    .accessibilityIdentifier("extension-graph-fold-\(id)")
                    .position(center)
                    // The node's custom action already speaks this same fold in reading order.
                    .accessibilityHidden(true)
                }
            }
        }
    }

    private func controls(content: CGSize, viewport: CGSize) -> some View {
        HStack(spacing: Theme.Space.flush) {
            control("extension-graph-zoom-out", label: String(localized: "Zoom Out"), icon: Theme.Icon.zoomOut,
                    disabled: model.transform.scale <= ExtensionGraphTransform.minimumScale) {
                model.zoom(1 / Theme.Size.graphZoomStep, in: viewport)
            }
            control("extension-graph-zoom-in", label: String(localized: "Zoom In"), icon: Theme.Icon.zoomIn,
                    disabled: model.transform.scale >= ExtensionGraphTransform.maximumScale) {
                model.zoom(Theme.Size.graphZoomStep, in: viewport)
            }
            control("extension-graph-fit", label: String(localized: "Fit"), icon: Theme.Icon.shrink) {
                model.applyFit(.fit(content, in: viewport), explicit: true)
            }
            if showsFullScreen {
                control("extension-graph-full-screen", label: String(localized: "Full Screen"), icon: Theme.Icon.enlarge) {
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
            .accessibilityIdentifier(identifier)
    }

    private var dragGesture: some Gesture {
        DragGesture(minimumDistance: Theme.Space.xs)
            .onChanged { value in
                guard !preview else { return }
                if dragStart == nil { dragStart = model.transform }
                guard let start = dragStart else { return }
                model.updateTransform(ExtensionGraphTransform(scale: start.scale,
                    x: start.x + value.translation.width, y: start.y + value.translation.height))
            }
            .onEnded { _ in
                if dragStart != nil { model.persistCurrent() }
                dragStart = nil
            }
    }

    private func pinchGesture(_ viewport: CGSize) -> some Gesture {
        MagnificationGesture()
            .onChanged { value in
                guard !preview else { return }
                if pinchStart == nil { pinchStart = model.transform }
                guard let start = pinchStart else { return }
                model.updateTransform(start.zoom(value, in: viewport))
            }
            .onEnded { _ in
                if pinchStart != nil { model.persistCurrent() }
                pinchStart = nil
            }
    }

    private func fitIfNeeded(_ layout: ExtensionGraphLayouter.Result, content: CGSize, viewport: CGSize) {
        guard !picture, !preview, !covered, model.needsFit || !model.touched, viewport.width > 0, viewport.height > 0 else { return }
        var fitted = ExtensionGraphTransform.fit(content, in: viewport, minimum: 0.5, maximum: 1)
        if let selected = graph.selected, let point = layout.at[selected] {
            let rect = nodeRect(point)
            fitted.x = viewport.width / 2 - rect.midX * fitted.scale
            fitted.y = viewport.height / 2 - rect.midY * fitted.scale
        } else if let first = layout.order.first, let point = layout.at[first] {
            let rect = nodeRect(point)
            fitted.x = Theme.Space.page - rect.minX * fitted.scale
            fitted.y = margin - rect.minY * fitted.scale
        }
        model.applyFit(fitted, explicit: false)
    }

    private func contentSize(_ layout: ExtensionGraphLayouter.Result) -> CGSize {
        CGSize(width: layout.size.width * (nodeSide + gapX) + margin * 2,
               height: layout.size.height * (nodeHeight + gapY) + margin * 2)
    }

    private func nodeRect(_ point: ExtensionGraphLayouter.Point) -> CGRect {
        CGRect(x: margin + point.x * (nodeSide + gapX), y: margin + point.y * (nodeHeight + gapY),
               width: nodeSide, height: nodeHeight)
    }

    private func frameRect(_ frame: ExtensionGraphLayouter.Frame) -> CGRect? {
        guard let model = model.layout else { return nil }
        var rect: CGRect?
        for id in frame.members {
            guard let point = model.at[id] else { continue }
            let next = nodeRect(point)
            rect = rect?.union(next) ?? next
        }
        guard let rect else { return nil }
        let scale = Theme.sizeScale(typeSize)
        let horizontal = Theme.Space.s * scale
        // A caption band and a small foot fit inside the node gap, so adjacent groups stay separate.
        let top = Theme.Space.xl * scale
        let bottom = Theme.Space.xs * scale
        return CGRect(x: rect.minX - horizontal, y: rect.minY - top,
                      width: rect.width + horizontal * 2, height: rect.height + top + bottom)
    }

    private func foldLabel(id: String, hidden: Int) -> String {
        model.folded.contains(id) ? String(localized: "Show \(hidden) hidden") : String(localized: "Fold")
    }
}

/// The native face of one node, plus its row-style action and fold accessibility action. Its position is handled by
/// the canvas so a state-only redraw changes no geometry.
private struct ExtensionGraphPlacedNode: View {
    var node: ExtensionGraphNode
    var selected: Bool
    var folded: Bool
    var parentTitle: String?
    var layout: ExtensionGraphLayouter.Result
    var controller: (any ExtensionViewControlling)?
    @Binding var link: LinkTarget?
    var reduceMotion: Bool
    var onNodePush: () -> Void
    var fold: () -> Void

    private var accent: Color? {
        if node.tone != nil { return ExtensionUIStyle.tone(node.tone) }
        return ExtensionUIStyle.color(node.color)
    }

    var body: some View {
        Group {
            if let action = node.action, let controller {
                ExtensionActionButton(action: action, title: node.title, model: controller, values: [:], link: $link,
                                      identifier: "extension-graph-node-\(node.id)", onPush: onNodePush) { face }
                    .buttonStyle(.plain)
            } else {
                face.accessibilityIdentifier("extension-graph-node-\(node.id)")
            }
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(node.title)
        .accessibilityValue(spokenValue)
        .accessibilityAddTraits(node.action == nil ? [] : .isButton)
        .accessibilityAddTraits(selected ? .isSelected : [])
        .modifier(ExtensionGraphFoldAction(label: foldName, enabled: layout.folds[node.id] != nil, action: fold))
        .contextMenu {
            Text(node.title)
            if let subtitle = node.subtitle, !subtitle.isEmpty { Text(subtitle) }
        }
        .animation(reduceMotion ? nil : Theme.Motion.state, value: node.badge)
        .animation(reduceMotion ? nil : Theme.Motion.state, value: node.progress)
        .transition(.opacity)
    }

    private var face: some View {
        let shape = RoundedRectangle(cornerRadius: Theme.Radius.card, style: .continuous)
        return HStack(spacing: Theme.Space.s) {
            if let icon = node.icon {
                ThemedIcon(ExtensionUIStyle.icon(icon))
                    .font(Theme.Font.heading)
                    .foregroundStyle(accent ?? Color.secondary)
                    .scaledFrame(width: Theme.Size.icon, height: Theme.Size.icon)
            }
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                HStack(spacing: Theme.Space.xs) {
                    Text(node.title).font(Theme.Font.bodyStrong).lineLimit(1).truncationMode(.tail)
                    if let badge = node.badge, !badge.text.isEmpty {
                        Chip(text: badge.text, look: ExtensionUIStyle.badge(badge))
                    }
                }
                HStack(spacing: Theme.Space.xs) {
                    if let accent { Circle().fill(accent).scaledFrame(width: Theme.Size.dotXS, height: Theme.Size.dotXS) }
                    if let subtitle = node.subtitle, !subtitle.isEmpty {
                        Text(subtitle).font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(1)
                    }
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .padding(.leading, accent == nil ? Theme.Space.flush : Theme.Space.xs)
        .card(padding: Theme.Space.m)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .overlay(alignment: .leading) {
            if let accent {
                Capsule().fill(accent).frame(width: Theme.Size.bar)
            }
        }
        .overlay(alignment: .bottom) {
            if let progress = node.progress {
                GeometryReader { geometry in
                    Capsule()
                        .fill(accent ?? Theme.accent)
                        .frame(width: geometry.size.width * min(max(progress, 0), 1), height: Theme.Size.bar)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
                .frame(height: Theme.Size.bar)
            }
        }
        .clipShape(shape)
        .overlay {
            if selected {
                shape.strokeBorder(Theme.accent, lineWidth: Theme.Size.rule)
            }
        }
        .contentShape(shape)
    }

    private var spokenValue: String {
        var parts: [String] = []
        if let badge = node.badge?.text, !badge.isEmpty { parts.append(badge) }
        if let subtitle = node.subtitle, !subtitle.isEmpty { parts.append(subtitle) }
        if let progress = node.progress { parts.append("\(Int((min(max(progress, 0), 1) * 100).rounded()))%") }
        if let parentTitle {
            parts.append(String(localized: "Under \(parentTitle)"))
        }
        return parts.joined(separator: ", ")
    }

    private var foldName: String {
        guard let hidden = layout.folds[node.id] else { return "" }
        // Another parent can keep every child visible in a layered graph, so the actual fold state names the action.
        return folded ? String(localized: "Show \(hidden) hidden") : String(localized: "Fold")
    }
}

private struct ExtensionGraphFoldAction: ViewModifier {
    var label: String
    var enabled: Bool
    var action: () -> Void

    @ViewBuilder func body(content: Content) -> some View {
        if enabled {
            content.accessibilityAction(named: label, action)
        } else {
            content
        }
    }
}

/// Trackpad scrolling and a mouse wheel are UIPanGestureRecognizer scroll events on iPadOS, not SwiftUI drag events.
/// Ordinary scrolling pans; Command/Control + wheel zooms around the pointer. Direct touches stay with SwiftUI.
private struct ExtensionGraphPointerPan: UIViewRepresentable {
    var onChanged: (CGSize) -> Void
    var onZoom: (Double, CGPoint) -> Void
    var onEnded: () -> Void

    func makeCoordinator() -> Coordinator { Coordinator(onChanged: onChanged, onZoom: onZoom, onEnded: onEnded) }

    func makeUIView(context: Context) -> UIView {
        let view = UIView(frame: .zero)
        view.backgroundColor = .clear
        let recognizer = UIPanGestureRecognizer(target: context.coordinator, action: #selector(Coordinator.pan(_:)))
        recognizer.allowedScrollTypesMask = [.continuous, .discrete]
        recognizer.allowedTouchTypes = [NSNumber(value: UITouch.TouchType.indirectPointer.rawValue)]
        recognizer.delegate = context.coordinator
        view.addGestureRecognizer(recognizer)
        return view
    }

    func updateUIView(_ view: UIView, context: Context) {
        context.coordinator.onChanged = onChanged
        context.coordinator.onZoom = onZoom
        context.coordinator.onEnded = onEnded
    }

    final class Coordinator: NSObject, UIGestureRecognizerDelegate {
        var onChanged: (CGSize) -> Void
        var onZoom: (Double, CGPoint) -> Void
        var onEnded: () -> Void
        private var moved = false

        init(onChanged: @escaping (CGSize) -> Void, onZoom: @escaping (Double, CGPoint) -> Void,
             onEnded: @escaping () -> Void) {
            self.onChanged = onChanged
            self.onZoom = onZoom
            self.onEnded = onEnded
        }

        @objc func pan(_ recognizer: UIPanGestureRecognizer) {
            switch recognizer.state {
            case .began, .changed:
                let delta = recognizer.translation(in: recognizer.view)
                recognizer.setTranslation(.zero, in: recognizer.view)
                guard delta != .zero else { return }
                moved = true
                let modified = recognizer.modifierFlags.contains(.command) || recognizer.modifierFlags.contains(.control)
                if modified, recognizer.buttonMask.isEmpty {
                    onZoom(exp(Double(delta.y) * Theme.Size.graphWheelZoomSensitivity),
                           recognizer.location(in: recognizer.view))
                } else {
                    onChanged(CGSize(width: delta.x, height: delta.y))
                }
            case .ended, .cancelled, .failed:
                if moved { onEnded() }
                moved = false
            default:
                break
            }
        }

        func gestureRecognizerShouldBegin(_ gestureRecognizer: UIGestureRecognizer) -> Bool {
            // Scroll only. A button-down pointer drag belongs to SwiftUI's DragGesture, not to both handlers.
            gestureRecognizer.buttonMask.isEmpty
        }

        func gestureRecognizer(_ gestureRecognizer: UIGestureRecognizer,
                               shouldRecognizeSimultaneouslyWith otherGestureRecognizer: UIGestureRecognizer) -> Bool { true }
    }
}
#endif
