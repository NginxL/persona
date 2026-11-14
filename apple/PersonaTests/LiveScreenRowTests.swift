import Foundation
import Testing
@testable import Persona

/// Settings' Live Screen row (#6398, #6396): there only when the page can change something for a computer here, so
/// someone using only the assistant never sees it.
@MainActor
struct LiveScreenRowTests {
    private func machine(_ extra: String) throws -> Machine {
        try JSON.decoder.decode(Machine.self, from: Data("{\"id\":\"macbook\",\"name\":\"MacBook\",\"kind\":\"ssh\",\"online\":true\(extra)}".utf8))
    }

    @Test func noComputerWithALiveScreenNoRow() throws {
        #expect(!LiveScreenSettingsView.isShown(machines: [], routesOffered: true), "no computer")
        #expect(!LiveScreenSettingsView.isShown(machines: [try machine("")], routesOffered: true), "a computer without its live screen")
        #expect(!LiveScreenSettingsView.isShown(machines: [try machine(",\"computer\":{\"enabled\":false,\"screen_sharing\":true}")],
                                                routesOffered: true), "the extension off")
    }

    @Test func withDirectRoutesAnyLiveScreen() throws {
        let screen = try machine(",\"computer\":{\"enabled\":true,\"screen_sharing\":true}")
        #expect(LiveScreenSettingsView.isShown(machines: [screen], routesOffered: true), "the route's switch applies to it")
        #expect(!LiveScreenSettingsView.isShown(machines: [screen], routesOffered: false), "without routes and video, nothing to set")
    }

    @Test func withoutDirectRoutesTheVideoChannel() throws {
        let video = try machine(",\"computer\":{\"enabled\":true,\"screen_sharing\":true,\"screen_video\":{\"version\":1}}")
        #expect(LiveScreenSettingsView.isShown(machines: [video], routesOffered: false), "quality on mobile data applies to it")
    }
}
