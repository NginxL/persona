import Foundation
import Testing
import PersonaWire
@testable import Persona

@MainActor struct LarkAvatarWireTests {
    @Test func thePublicFeishuPagesKeepBothReferences() async throws {
        let hub = FakeHub(url: "https://" + UUID().uuidString + ".invalid")
        let api = hub.client.lark(2)
        let route = "/api/v1/avatars/lark/cGhvdG8vb25l?lark_account=2"
        let legacy = "/agent/v1/media/photo%2Fone"
        let chat = #"{"id":"studio","name":"Studio","unread":0,"is_group":true,"at_me":false,"muted":false,"preview":"","is_mine":false,"avatar":"\#(legacy)","avatar_url":"\#(route)"}"#
        let message = #"{"id":"m","position":"1","sender_name":"River","sender_avatar":"\#(legacy)","sender_avatar_url":"\#(route)","is_mine":false,"type":"TEXT","text":"Hello","recalled":false,"reply_count":0}"#
        hub.on("GET", "/api/v1/lark/accounts", json: #"{"accounts":[{"id":2,"signed_in":true,"unread":0,"avatar":"photo/one","avatar_url":"\#(route)"}],"default":2}"#)
        hub.on("GET", "/api/v1/lark/conversations", json: #"{"conversations":[\#(chat)],"account_id":"user-68"}"#)
        hub.on("GET", "/api/v1/lark/conversations/studio/messages", json: #"{"messages":[\#(message)],"has_older":false}"#)
        hub.on("GET", "/api/v1/lark/flags", json: #"{"conversations":[],"messages":[{"feed_id":"studio","chat_name":"Studio","chat_avatar":"\#(legacy)","chat_avatar_url":"\#(route)","message":\#(message)}]}"#)
        hub.on("GET", "/api/v1/lark/conversations/studio/members", json: #"{"members":[{"user_id":"river","name":"River","avatar":"\#(legacy)","avatar_url":"\#(route)"}]}"#)
        hub.on("GET", "/api/v1/lark/users/river", json: #"{"displayName":"River","avatarUrl":"photo/one","avatar_url":"\#(route)","coverUrl":"/api/media/cover"}"#)
        hub.on("GET", "/api/v1/inbox", json: #"{"items":[{"id":"l","source":"lark","kind":"mention","account":"feishu","account_name":"Lark","title":"Studio","preview":"Hello","count":1,"avatar":"\#(legacy)","avatar_url":"\#(route)","lark":{"feed_id":"studio","is_group":true}}]}"#)

        let account = try #require(try await api.larkAccounts().accounts.first)
        let conversation = try #require(try await api.larkConversations().conversations.first)
        let sender = try #require(try await api.larkMessages("studio").messages.first)
        let flagged = try #require(try await api.larkFlags().messages.first)
        let member = try #require(try await api.larkMembers("studio", query: "").members.first)
        let profile = try await api.larkProfile("river")
        let inbox = try #require(try await api.inbox().first)
        #expect([account.picture, conversation.picture, sender.senderPicture, member.picture, profile.picture, flagged.chatAvatarURL,
                 inbox.avatarURL, inbox.larkConversation?.picture].allSatisfy { $0 == route })
        #expect(account.avatar == "photo/one" && profile.avatarUrl == "photo/one")
        #expect([conversation.avatar, sender.senderAvatar, member.avatar, inbox.avatar, flagged.chatAvatar].allSatisfy { $0 == legacy })
        #expect(profile.coverUrl == "/api/media/cover")

        let cached = try JSON.decoder.decode(LarkMessage.self, from: JSON.encoder.encode(sender))
        #expect(cached.senderPicture == route && cached.senderAvatar == legacy)
        let old = try JSON.decoder.decode(LarkConversation.self, from: Data(chat.replacingOccurrences(of: #","avatar_url":"\#(route)""#, with: "").utf8))
        #expect(old.picture == legacy)
        let oldMember = try JSON.decoder.decode(LarkMember.self, from: Data(#"{"user_id":"river","name":"River","avatarUrl":"photo/one"}"#.utf8))
        let oldProfile = try JSON.decoder.decode(LarkProfile.self, from: Data(#"{"avatar":"photo/one"}"#.utf8))
        #expect(oldMember.picture == "photo/one" && oldProfile.picture == "photo/one")
        let legacySlot = "/agent/v1/media/photo%2Fone?ol_account=3"
        #expect(LarkAvatarImage.cachedPicture(legacySlot, using: hub.client, account: 0) == "/api/v1/lark/media/photo%2Fone?ol_account=3")
    }

    @Test func anOldAccountLoadsItsBareKeyAtTheExistingGatewayEndpoint() async throws {
        let hub = FakeHub(url: "https://" + UUID().uuidString + ".invalid")
        let api = hub.client.manager("studio").lark(3)
        hub.on("GET", "/api/v1/lark/accounts", json: #"{"accounts":[{"id":2,"signed_in":true,"unread":0,"avatar":"photo/one"}],"default":2}"#)
        let endpoint = "/api/v1/lark/media/photo%2Fone"
        hub.onAsync("GET", endpoint) { req in
            #expect(req.query == ["manager": "studio", "ol_account": "2", "size": "144"])
            #expect(req.repeated.isEmpty)
            return Data([2])
        }
        let account = try #require(try await api.larkAccounts().accounts.first)
        let raw = try #require(account.picture)
        let path = try #require(LarkAvatarImage.cachedPicture(raw, using: api, account: account.id))
        let media = LarkMedia()
        for _ in 0..<2 {
            #expect(try await media.data(path, size: ChatAvatar.edge(pixels: 120, path: path), using: api) == Data([2]))
        }
        #expect(hub.sent("GET", endpoint).count == 1)
    }

    @Test func theFeishuAndMessagingNamespacesCanShareOneManager() async {
        let hub = FakeHub(url: "https://" + UUID().uuidString + ".invalid")
        let api = hub.client.manager("north").lark(2)
        hub.on("GET", "/api/v1/messaging", json: #"{"sources":[{"id":"studio","extension":"example","name":"Studio"}]}"#)
        await api.readMessagingSource(features: ["messaging"])
        #expect(LarkAvatarImage.cachedPicture("/api/media/native", using: api) == "/api/v1/lark/media/native?ol_account=2&manager=north")
        #expect(LarkAvatarImage.cachedPicture("photo/one", using: api) == "/api/v1/lark/media/photo%2Fone?ol_account=2&manager=north")
        let signed = "https://s1.feishucdn.com/static-resource/v1/photo%2Fone~?expires=1"
        #expect(LarkAvatarImage.cachedPicture(signed, using: api) == "/api/v1/lark/media/photo%2Fone?ol_account=2&manager=north")
        #expect(LarkAvatarImage.cachedPicture("v3:photo", using: api) == "/api/v1/lark/media/v3:photo?ol_account=2&manager=north")
        let native = "/api/v1/avatars/lark/cA?lark_account=3"
        #expect(LarkAvatarImage.cachedPicture(native + "&manager=south&size=512", using: api) == native + "&manager=north")
        let other = "/api/v1/avatars/studio/cA?messaging=1&account=two"
        #expect(LarkAvatarImage.cachedPicture(other, using: api) == other + "&manager=north")
    }

    @Test func oldMediaEndpointsReceiveTheGatewayThumbnailSizes() {
        #expect([45, 120, 220, 500].map { ChatAvatar.edge(pixels: CGFloat($0), path: "/api/v1/lark/media/p") } == [72, 144, 264, 528])
        #expect([45, 120, 220, 500].map { ChatAvatar.edge(pixels: CGFloat($0), path: "/api/v1/avatars/lark/cA?lark_account=2") } == [64, 128, 256, 512])
    }

    @Test func hubManagerAndGatewaySlotHaveIndependentPictureCaches() async throws {
        let media = LarkMedia()
        let hubs = (0..<2).map { _ in FakeHub(url: "https://" + UUID().uuidString + ".invalid") }
        let path = "/api/v1/avatars/lark/cGhvdG8vb25l"
        for (index, hub) in hubs.enumerated() {
            hub.onAsync("GET", path) { req in
                let slot = try #require(Int(req.query["lark_account"] ?? ""))
                #expect(req.query["size"] == "64")
                return Data([UInt8(index * 20 + (req.query["manager"] == "north" ? 10 : 0) + slot)])
            }
        }
        for index in [0, 1, 0] {
            for manager in ["north", "south"] {
                let client = hubs[index].client.manager(manager)
                for slot in [2, 3] {
                    let row = path + "?lark_account=\(slot)"
                    let picture = try #require(LarkAvatarImage.cachedPicture(row, using: client))
                    #expect(try await media.data(picture, size: 64, using: client) == Data([UInt8(index * 20 + (manager == "north" ? 10 : 0) + slot)]))
                }
            }
        }
        #expect(hubs.allSatisfy { $0.sent("GET", path).count == 4 })
    }
}
