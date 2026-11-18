import AppIntents
import Foundation
import Testing
@testable import Persona

/// Siri's and Shortcuts' ways into a call to Persona (#9290, docs/apple/siri.md「Calling Persona」): the App Shortcut Call
/// Persona, Talk to Persona with no car, and the opt-in SiriKit extension's switch as the app sees it.
struct CallPersonaIntentTests {
    /// The call's Start needs the app in front, so Call Persona opens it before it runs.
    @Test func callPersonaOpensTheApp() {
        if #available(iOS 26.0, *) {
            #expect(CallPersonaIntent.supportedModes == .foreground(.immediate))
        } else {
            #expect(CallPersonaIntent.openAppWhenRun)
        }
    }

    /// The car's conversation still starts in the background (the phone may stay locked in the car); only the phone's
    /// call comes to the front.
    @Test func talkToPersonaStaysInTheBackgroundUntilItCalls() {
        if #available(iOS 26.0, *) {
            #expect(TalkToPersonaIntent.supportedModes.contains(.background))
            #expect(TalkToPersonaIntent.supportedModes.contains(.foreground(.dynamic)))
            #expect(!TalkToPersonaIntent.supportedModes.contains(.foreground(.immediate)))
        } else {
            #expect(!TalkToPersonaIntent.openAppWhenRun)
        }
    }

    /// Apple allows ten App Shortcuts; more and the build's App Intents metadata is refused.
    @Test func atMostTenAppShortcuts() {
        #expect(PersonaShortcuts.appShortcuts.count == 10)
    }

    /// Talk to Persona promises a call only where it places one: the phone's description and keywords; elsewhere
    /// (Apple Vision Pro, where `PersonaCalls` does not exist and it only says to connect CarPlay) CarPlay's alone.
    @Test func talkToPersonaPromisesACallOnlyWhereItPlacesOne() throws {
        let path = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
            .appending(path: "Persona/Intents/TalkToPersonaIntent.swift")
        // The code alone: a comment may say what it does not promise.
        let source = try String(contentsOf: path, encoding: .utf8).components(separatedBy: "\n")
            .filter { !$0.trimmingCharacters(in: .whitespaces).hasPrefix("//") }.joined(separator: "\n")
        let phone = try #require(source.range(of: "#if os(iOS)"))
        let other = try #require(source.range(of: "#else", range: phone.upperBound..<source.endIndex))
        let end = try #require(source.range(of: "#endif", range: other.upperBound..<source.endIndex))
        let elsewhere = String(source[other.upperBound..<end.lowerBound])
        #expect(String(source[phone.upperBound..<other.lowerBound]).contains("a call to Persona on this device"))
        #expect(elsewhere.contains("IntentDescription(") && !elsewhere.contains("call to Persona") && !elsewhere.contains(#""call""#))
    }

    @Test func siriSaysItIsCalling() {
        #expect(CallPersonaIntent.calling == String(localized: "Calling Persona"))
        #expect(CallPersonaIntent.calling != CarPlayTalk.notConnected.sentence)
    }

    /// A build has the SiriKit extension exactly when apple/siri-calls.yml was merged in, which brings Siri's purpose
    /// string with it: off by default, and then the app never asks for Siri. The raw Info.plist: the translations sit in
    /// InfoPlist.strings either way, and `object(forInfoDictionaryKey:)` would find them there.
    @MainActor @Test func siriCallsFollowTheBuildsSwitch() {
        let purpose = Bundle.main.infoDictionary?["NSSiriUsageDescription"] as? String
        #expect(SiriCalls.builtIn == (purpose != nil))
        let plugins = Bundle.main.builtInPlugInsURL?.appending(path: "PersonaCallIntents.appex").path ?? ""
        #expect(SiriCalls.builtIn == FileManager.default.fileExists(atPath: plugins))
    }
}
