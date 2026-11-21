import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Settings › Devices, "Primary Device" (`PhonePrimaryChoices`): when the choice shows, which devices it lists and in which
/// order, and which one is chosen. Made-up devices; asserts ids and order, never the localized words.
struct PhonePrimaryChoicesTests {
    private static let tokensJSON = """
    {"tokens":[
      {"id":"dev_a","name":"Phone A","kind":"phone","role":"full"},
      {"id":"dev_b","name":"Tablet B","kind":"phone","role":"full"},
      {"id":"dev_watch","name":"Watch","kind":"watch","role":"answer"},
      {"id":"dev_old","name":"Old Phone","kind":"phone","role":"full","revoked_at":"2026-01-01T00:00:00Z"}
    ],"current":"dev_a"%@}
    """

    private func decode(phone: String?) throws -> APIClient.DeviceTokens {
        let tail = phone.map { ",\"phone\":" + $0 } ?? ""
        return try JSON.decoder.decode(APIClient.DeviceTokens.self, from: Data(Self.tokensJSON.replacingOccurrences(of: "%@", with: tail).utf8))
    }

    private func options(_ r: APIClient.DeviceTokens) -> [PhonePrimaryChoices.Option] {
        PhonePrimaryChoices.options(phone: r.phone, tokens: r.tokens, current: r.current)
    }

    @Test func anOlderHubHasNoPhoneAndShowsNothing() throws {
        let r = try decode(phone: nil)
        #expect(r.phone == nil)
        #expect(!PhonePrimaryChoices.isShown(r.phone))
        #expect(options(r).isEmpty)
        #expect(PhonePrimaryChoices.selection(phone: r.phone, options: []) == PhonePrimaryChoices.automatic)
    }

    @Test func theChoiceShowsFromTwoDevicesOrOnceOneIsChosen() throws {
        #expect(!PhonePrimaryChoices.isShown(try decode(phone: #"{"primary":"","candidates":[]}"#).phone))
        #expect(!PhonePrimaryChoices.isShown(try decode(phone: #"{"primary":"","candidates":["dev_a"]}"#).phone), "one device: nothing to choose")
        #expect(PhonePrimaryChoices.isShown(try decode(phone: #"{"primary":"","candidates":["dev_a","dev_b"]}"#).phone))
        #expect(PhonePrimaryChoices.isShown(try decode(phone: #"{"primary":"dev_a","candidates":["dev_a"]}"#).phone), "chosen: it can be changed back")
    }

    @Test func automaticComesFirstThenTheCandidatesInTheHubsOrder() throws {
        let r = try decode(phone: #"{"primary":"","candidates":["dev_b","dev_a"]}"#)
        let o = options(r)
        #expect(o.map(\.id) == ["", "dev_b", "dev_a"])
        #expect(o.map(\.isCurrent) == [false, false, true], "only this device's own credential is marked")
        #expect(o.first?.device == nil)
        #expect(o.dropFirst().compactMap { $0.device?.id } == ["dev_b", "dev_a"])
        #expect(PhonePrimaryChoices.selection(phone: r.phone, options: o) == "", "none chosen: Automatic")
    }

    @Test func theChosenDeviceIsSelected() throws {
        let r = try decode(phone: #"{"primary":"dev_b","candidates":["dev_a","dev_b"]}"#)
        #expect(PhonePrimaryChoices.selection(phone: r.phone, options: options(r)) == "dev_b")
    }

    @Test func aChosenDeviceThatIsNotACandidateIsStillListed() throws {
        let r = try decode(phone: #"{"primary":"dev_b","candidates":["dev_a"]}"#)
        let o = options(r)
        #expect(o.map(\.id) == ["", "dev_a", "dev_b"], "after the candidates, so the current choice can always show")
        #expect(PhonePrimaryChoices.selection(phone: r.phone, options: o) == "dev_b")
    }

    @Test func aDeviceTheListCannotNameIsLeftOut() throws {
        // Revoked since, or not in the list: nothing to show a name from. The choice then reads as Automatic.
        let r = try decode(phone: #"{"primary":"dev_old","candidates":["dev_a","dev_old","dev_gone","dev_a"]}"#)
        let o = options(r)
        #expect(o.map(\.id) == ["", "dev_a"], "revoked and unknown credentials out, a repeated candidate once")
        #expect(PhonePrimaryChoices.selection(phone: r.phone, options: o) == "")
    }

    @Test func choosingChangesOnlyThePrimary() throws {
        let r = try decode(phone: #"{"primary":"","candidates":["dev_a","dev_b"]}"#)
        let chosen = try #require(PhonePrimaryChoices.choosing("dev_b", in: r.phone))
        #expect(chosen.primary == "dev_b")
        #expect(chosen.candidates == ["dev_a", "dev_b"])
        #expect(PhonePrimaryChoices.selection(phone: chosen, options: PhonePrimaryChoices.options(phone: chosen, tokens: r.tokens, current: r.current)) == "dev_b")
        let back = try #require(PhonePrimaryChoices.choosing(PhonePrimaryChoices.automatic, in: chosen))
        #expect(back == r.phone, "Automatic puts the hub's own answer back")
        #expect(PhonePrimaryChoices.choosing("dev_b", in: nil) == nil, "an older hub has nothing to change")
    }
}
