import CoreGraphics
import Foundation
import Observation
import Testing
@testable import Persona
import PersonaWire

@MainActor
struct ExtensionGraphModelTests {
    private func graph(folded: [String]? = nil, badge: String = "Running") -> ExtensionGraphComponent {
        ExtensionGraphComponent(key: "work", nodes: [
            ExtensionGraphNode(id: "root", title: "Root", badge: ExtensionBadge(text: badge)),
            ExtensionGraphNode(id: "a", title: "A"),
            ExtensionGraphNode(id: "b", title: "B"),
        ], edges: [ExtensionGraphEdge(from: "root", to: "a"), ExtensionGraphEdge(from: "root", to: "b")],
        selected: "a", folded: folded)
    }

    @Test func restoredChoicesAndATouchedViewportSurviveStateOnlyRedraws() async {
        let saved = ExtensionGraphSavedState(
            transform: ExtensionGraphTransform(scale: 1.25, x: 40, y: 60), folds: ["root": false], touched: true)
        let model = ExtensionGraphModel(graph: graph(folded: ["root"]), restored: saved, keeps: false)
        await model.reload(graph(folded: ["root"]))
        #expect(model.folded.isEmpty, "an explicit unfold wins over the extension's starting folds")
        #expect(model.layout?.order == ["root", "a", "b"])
        #expect(model.transform == saved.transform && model.touched && !model.needsFit)
        let before = model.layout?.at
        let version = model.layoutVersion

        await model.reload(graph(folded: ["root"], badge: "Done"))
        #expect(model.layout?.at == before, "state changes do not move any node")
        #expect(model.layoutVersion == version, "state changes reuse the prepared layout")
        #expect(model.transform == saved.transform && !model.needsFit)
    }

    @Test func defaultFoldChangesApplyUntilThePersonChooses() async {
        let model = ExtensionGraphModel(graph: graph(folded: ["root"]), keeps: false)
        await model.reload(graph(folded: ["root"]))
        #expect(model.layout?.order == ["root"])

        await model.reload(graph())
        #expect(model.layout?.order == ["root", "a", "b"], "a new default unfolds when there is no explicit choice")
        await model.toggleFold("root")
        await model.reload(graph())
        #expect(model.folded == ["root"], "an explicit fold wins over the extension's new default")
        await model.toggleFold("root")
        await model.reload(graph(folded: ["root"]))
        #expect(model.folded.isEmpty, "an explicit unfold wins over the extension's new default")
    }

    @Test func foldsPersistAndOnlyAnUntouchedCanvasFitsAgain() async {
        var saves: [ExtensionGraphSavedState] = []
        let model = ExtensionGraphModel(graph: graph(), keeps: false, onSave: { saves.append($0) })
        await model.reload(graph())
        #expect(model.needsFit)
        model.applyFit(ExtensionGraphTransform(), explicit: false)
        #expect(!model.needsFit && !model.touched)

        await model.toggleFold("root")
        #expect(model.folded == ["root"] && model.layout?.order == ["root"])
        #expect(model.needsFit, "an untouched canvas takes the opening fit after its shape changes")
        #expect(saves.last?.folds == ["root": true] && saves.last?.touched == false)

        model.applyFit(ExtensionGraphTransform(scale: 0.75, x: 12, y: 18), explicit: true)
        #expect(model.touched && saves.last?.touched == true)
        await model.toggleFold("root")
        #expect(model.folded.isEmpty && model.layout?.order == ["root", "a", "b"])
        #expect(!model.needsFit && model.transform == ExtensionGraphTransform(scale: 0.75, x: 12, y: 18))
        #expect(saves.last?.folds == ["root": false], "the explicit unfolded choice is persisted too")
    }

    @Test func stateOnlyReplacementPublishesNewNodeContent() async {
        let model = ExtensionGraphModel(graph: graph(), keeps: false)
        await model.reload(graph())
        nonisolated(unsafe) var published = false
        withObservationTracking({ _ = model.nodesByID["root"] }, onChange: { published = true })
        await model.reload(graph(badge: "Done"))
        await Task.yield()
        #expect(published, "a badge-only replacement must redraw the node even when its layout is unchanged")
        #expect(model.nodesByID["root"]?.badge?.text == "Done")
    }

    @Test func persistenceKeysAreScopedToTheirPageAndHub() {
        let first = ExtensionGraphModel(graph: graph(), namespace: "hub-a/page-a", keeps: false)
        let same = ExtensionGraphModel(graph: graph(), namespace: "hub-a/page-a", keeps: false)
        let otherPage = ExtensionGraphModel(graph: graph(), namespace: "hub-a/page-b", keeps: false)
        let otherHub = ExtensionGraphModel(graph: graph(), namespace: "hub-b/page-a", keeps: false)
        #expect(first.componentKey == "work" && first.persistenceKey == same.persistenceKey)
        #expect(first.persistenceKey != otherPage.persistenceKey)
        #expect(first.persistenceKey != otherHub.persistenceKey)
        #expect(ExtensionGraphModel(graph: graph(), keeps: false).persistenceKey == "work")
    }

    @Test func aPictureHasItsSceneWithoutATaskOrDiskRestore() {
        let model = ExtensionGraphModel.picture(graph())
        #expect(model.layout?.order == ["root", "a", "b"])
        #expect(model.nodesByID["root"]?.badge?.text == "Running")
        #expect(!model.touched)
    }

    @Test func gestureEndPersistsAnAlreadyChangedTransform() async {
        var saves: [ExtensionGraphSavedState] = []
        let model = ExtensionGraphModel(graph: graph(), keeps: false, onSave: { saves.append($0) })
        await model.reload(graph())
        let moved = ExtensionGraphTransform(scale: 1.4, x: -30, y: 24)
        model.updateTransform(moved)
        #expect(model.transform == moved && model.touched && saves.isEmpty)

        model.persistCurrent()
        #expect(saves.count == 1, "onChanged already installed the transform; onEnded must still save it")
        #expect(saves.last?.transform == moved && saves.last?.touched == true)
    }

    @Test func wheelEndSavesCoalesceAndAnImmediateSaveWins() async {
        var saves: [ExtensionGraphSavedState] = []
        let model = ExtensionGraphModel(graph: graph(), keeps: false, onSave: { saves.append($0) })
        await model.reload(graph())
        model.updateTransform(ExtensionGraphTransform(scale: 1.1, x: -10, y: 0))
        model.persistCurrent(coalesced: true)
        let latest = ExtensionGraphTransform(scale: 1.2, x: -20, y: 0)
        model.updateTransform(latest)
        model.persistCurrent(coalesced: true)
        #expect(saves.isEmpty)
        try? await Task.sleep(for: .milliseconds(300))
        #expect(saves.count == 1 && saves.last?.transform == latest, "one trailing save for a wheel burst")

        model.persistCurrent(coalesced: true)
        model.persistCurrent()
        #expect(saves.count == 2, "an immediate gesture save is not delayed")
        try? await Task.sleep(for: .milliseconds(300))
        #expect(saves.count == 2, "the immediate save cancels the older pending wheel save")
    }

    @Test func pointerZoomKeepsItsAnchorAndClamps() {
        let original = ExtensionGraphTransform(scale: 1, x: 10, y: 20)
        let anchor = CGPoint(x: 100, y: 100)
        let contentPoint = CGPoint(x: 90, y: 80)
        let zoomed = original.zoom(1.5, around: anchor)
        let landed = zoomed.apply(to: contentPoint)
        #expect(zoomed.scale == 1.5)
        #expect(abs(landed.x - anchor.x) < 0.001 && abs(landed.y - anchor.y) < 0.001)
        #expect(original.zoom(1.5, in: CGSize(width: 400, height: 300)) ==
                original.zoom(1.5, around: CGPoint(x: 200, y: 150)))
        #expect(ExtensionGraphTransform(scale: 2).zoom(2, around: anchor) ==
                ExtensionGraphTransform(scale: 2))
        #expect(ExtensionGraphTransform(scale: 0.25).zoom(0.5, around: anchor) ==
                ExtensionGraphTransform(scale: 0.25))
    }

    @Test func fitClampsScaleAndCentersTheWholeGraph() {
        let content = CGSize(width: 800, height: 400)
        let viewport = CGSize(width: 400, height: 400)
        let fit = ExtensionGraphTransform.fit(content, in: viewport)
        #expect(fit.scale == 0.5 && fit.x == 0 && fit.y == 100)
        let opening = ExtensionGraphTransform.fit(content, in: viewport, minimum: 0.5, maximum: 1)
        #expect(opening.scale == 0.5)
        let preview = ExtensionGraphTransform.fit(content, in: CGSize(width: 100, height: 100), minimum: 0.01)
        #expect(preview.scale == 0.125 && preview.y == 25)
        let zoomed = fit.zoom(2, in: viewport)
        #expect(zoomed.scale == 1 && zoomed.x == -200 && zoomed.y == 0)
    }
}
