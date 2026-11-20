import Foundation
import Testing
@testable import Persona

@MainActor struct LarkAvatarCompatibilityTests {
    @Test func oldGatewayAndCDNPicturesStayOnThePairedHub() async {
        let hub = FakeHub(url: "https://" + UUID().uuidString + ".invalid")
        var api = hub.client
        api.managerID = "studio"
        api.larkAccount = 2
        await api.readMessagingSource(features: ["lark"])
        let resource = "/api/v1/lark/media/photo%2Fone?ol_account=2&manager=studio"
        for raw in ["/api/v1/lark/media/photo/one", "/agent/v1/media/photo%2Fone", "/api/media/photo/one",
                    "https://s1.feishucdn.com/static-resource/v1/photo%2Fone~?image_size=thumb", "photo/one"] {
            #expect(LarkAvatarImage.cachedPicture(raw, using: api) == resource)
        }
        #expect(LarkAvatarImage.cachedPicture("https://portraits.example.invalid/someone.jpg", using: api) == nil)
    }

    @Test func listedSlotWinsOverTheCurrentSlotAndUsesOneSize() async throws {
        let hub = FakeHub(url: "https://" + UUID().uuidString + ".invalid")
        var api = hub.client
        api.managerID = "studio"
        api.larkAccount = 3
        let path = "/api/v1/avatars/lark/cGhvdG8vb25l"
        let listed = path + "?lark_account=2&size=64"
        let picture = try #require(LarkAvatarImage.cachedPicture(listed, using: api))
        #expect(picture == path + "?lark_account=2&manager=studio")
        hub.onAsync("GET", path) { request in
            #expect(request.query == ["manager": "studio", "lark_account": "2", "size": "128"])
            return Data([2])
        }
        let bytes = try await LarkMedia().data(picture, size: 128, using: api)
        #expect(bytes == Data([2]))
        #expect(hub.sent("GET", path).count == 1)
    }
}
