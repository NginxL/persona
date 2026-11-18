import Intents

// "Call Persona using Persona" (#9290, docs/apple/siri.md「Calling Persona」): Siri's call intent, handed to the app. Built
// only when config.json app.siri_calls is on (apple/siri-calls.yml): the app is single-scene, and SiriKit asks an app
// itself for a handler only when it supports multiple scenes, so the intent comes to this extension.
// The extension places nothing (Apple: never start a call from an Intents extension). Whoever was named, the one person
// to call is Persona; `handle` answers continue-in-app, so Siri opens the app with an `INStartCallIntent` user activity
// and the app places the call as it does a redial from Recents (`PersonaCalls.continueActivity`): the selected hub's
// main conversation, the same refusals, the same call page. Intents, not CallKit (App Review rejects CallKit for China).

/// The extension's principal class (Info.plist `NSExtensionPrincipalClass`): every intent it supports goes to it.
final class IntentHandler: INExtension, INStartCallIntentHandling {
    override func handler(for intent: INIntent) -> Any { self }

    /// The only person there is to call: Persona, by the product's name in every language. The app's own calls carry the
    /// assistant's name (`CallHandle.name`), which this extension cannot read; any handle calls the same main conversation.
    static var persona: INPerson {
        INPerson(personHandle: INPersonHandle(value: "Persona", type: .unknown), nameComponents: nil,
                 displayName: "Persona", image: nil, contactIdentifier: nil, customIdentifier: "persona")
    }

    // The completion-handler forms SiriKit calls (on its own queue), not async witnesses: an async witness of an @objc
    // protocol is nonisolated(nonsending) under approachable concurrency, which Swift 6.2 (Xcode 26.0) handles less well.
    func resolveContacts(for intent: INStartCallIntent, with completion: @escaping ([INStartCallContactResolutionResult]) -> Void) {
        completion([.success(with: Self.persona)])
    }

    /// A voice call whatever was asked (FaceTime or video included): Persona has no video.
    func resolveCallCapability(for intent: INStartCallIntent, with completion: @escaping (INStartCallCallCapabilityResolutionResult) -> Void) {
        completion(.success(with: .audioCall))
    }

    /// Not an emergency, voicemail or redial number: a call to Persona.
    func resolveDestinationType(for intent: INStartCallIntent, with completion: @escaping (INCallDestinationTypeResolutionResult) -> Void) {
        completion(.success(with: .normal))
    }

    /// The call goes on in the app. No activity of ours: Siri makes one named after the intent, with the intent in its
    /// interaction, which is what the app reads.
    func handle(intent: INStartCallIntent, completion: @escaping (INStartCallIntentResponse) -> Void) {
        completion(INStartCallIntentResponse(code: .continueInApp, userActivity: nil))
    }
}
