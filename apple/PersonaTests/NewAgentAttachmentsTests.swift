import Foundation
import Testing
import UIKit
@testable import Persona

/// New Agent's attachments (#2321): what the first message says, what holds Start Chat back, and the files of a computer
/// picked before.
struct NewAgentAttachmentsTests {
    @Test func textThenPathsOnePerLine() {
        #expect(FirstMessage.text(prompt: "  看看这个日志 \n", contextBlock: nil, paths: [], photos: 0) == "看看这个日志")
        #expect(FirstMessage.text(prompt: "看看这个日志", contextBlock: nil, paths: ["/tmp/build-1.log", "/tmp/会议纪要 v2.docx"], photos: 0)
            == "看看这个日志\n/tmp/build-1.log\n'/tmp/会议纪要 v2.docx'")
        // Only files: their paths alone, as the web joins them.
        #expect(FirstMessage.text(prompt: " ", contextBlock: nil, paths: ["/tmp/a.txt"], photos: 1) == "/tmp/a.txt")
        #expect(FirstMessage.text(prompt: "", contextBlock: nil, paths: [], photos: 0) == "")
    }

    @Test func onlyPhotosGetTheComposersWords() {
        let words = String(localized: "Please look at the image [Assistant View 5ad878]")
        #expect(FirstMessage.text(prompt: "", contextBlock: nil, paths: [], photos: 2) == words)
        #expect(FirstMessage.text(prompt: "这是什么", contextBlock: nil, paths: [], photos: 2) == "这是什么")
    }

    @Test func aToDosContextBlockComesFirst() {
        let block = "<persona-context kind=\"context\" title=\"待办 · 修备份\" url=\"\">NAS</persona-context>"
        #expect(FirstMessage.text(prompt: "先看日志", contextBlock: block, paths: ["/tmp/a.log"], photos: 1)
            == "\(block)\n\n先看日志\n/tmp/a.log")
        #expect(FirstMessage.text(prompt: "", contextBlock: block, paths: ["/tmp/a.log"], photos: 0) == "\(block)\n/tmp/a.log")
        // With photos only, the block is the text: no words for the photos are added.
        #expect(FirstMessage.text(prompt: "", contextBlock: block, paths: [], photos: 1) == block)
    }

    /// #1842's text-only Traex start uses this same composition as its start prompt, not a later ordinary message.
    @Test func aTraexTextOnlyPromptIsContextThenText() {
        let block = "<persona-context kind=\"context\" title=\"待办 · 查启动\" url=\"\">Traex</persona-context>"
        let first = FirstMessage.text(prompt: "  检查启动日志 \n", contextBlock: block, paths: [], photos: 0)
        #expect(first == "\(block)\n\n检查启动日志")
        #expect(!first.isEmpty)
    }

    @Test func codexTakesNoAttachmentsAndUploadsHoldTheStart() {
        let codex = String(localized: "Codex takes no attachments")
        let uploading = String(localized: "Waiting for the files to finish uploading")
        #expect(FirstMessage.hold(codex: true, attached: true, uploading: true) == codex)
        #expect(FirstMessage.hold(codex: true, attached: true, uploading: false) == codex)
        #expect(FirstMessage.hold(codex: true, attached: false, uploading: false) == nil)
        #expect(FirstMessage.hold(codex: false, attached: true, uploading: true) == uploading)
        #expect(FirstMessage.hold(codex: false, attached: true, uploading: false) == nil)
        #expect(FirstMessage.hold(codex: false, attached: false, uploading: false) == nil)
    }

    @MainActor @Test func anotherComputerTakesTheFilesOutAndKeepsThePhotos() {
        let a = NewAgentAttachments(), model = AppModel()
        a.images = [(UIImage(), ImagePayload(mime: "image/jpeg", data: "eA=="))]
        let names = ["macbook": "MacBook"]
        // Before any upload a switch changes nothing.
        a.computerChanged(to: "studio") { names[$0] }
        #expect(a.note == nil)

        a.upload([], to: MachineRef(path: "macbook"), model: model) // the files' computer, nothing picked
        a.uploaded(.init(name: "build.log", path: "/tmp/build-1.log", size: 3 << 20), on: "macbook")
        #expect(a.paths == ["/tmp/build-1.log"])
        a.computerChanged(to: "studio") { names[$0] }
        #expect(a.files.isEmpty)
        #expect(a.images.count == 1)
        let name = "MacBook"
        #expect(a.note == String(localized: "The files uploaded to “\(name)” were removed. Add them again for this computer."))
        // An upload to the old computer that ends after the switch is not kept.
        a.uploaded(.init(name: "late.log", path: "/tmp/late-1.log", size: 1), on: "macbook")
        #expect(a.files.isEmpty)
        // Switching back finds nothing to take out: no new note.
        a.add(UIImage(), model: model)
        #expect(a.note == nil)
        a.computerChanged(to: "macbook") { names[$0] }
        #expect(a.note == nil)
    }

    @MainActor @Test func removingAFile() {
        let a = NewAgentAttachments()
        a.upload([], to: MachineRef(path: "macbook"), model: AppModel())
        a.uploaded(.init(name: "a.txt", path: "/tmp/a.txt", size: 1), on: "macbook")
        a.uploaded(.init(name: "b.txt", path: "/tmp/b.txt", size: 1), on: "macbook")
        a.remove(a.files[0])
        #expect(a.paths == ["/tmp/b.txt"])
        #expect(!a.isEmpty && !a.uploading)
    }
}
