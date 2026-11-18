import Foundation
import PushKit
import Testing
@testable import Persona

/// An iPhone or iPad app running on a Mac (#9948) makes none of LiveCommunicationKit's objects, registers for no urgent
/// calls, and does not offer their Settings. Each path receives the one platform fact explicitly; false keeps the existing
/// iPhone/iPad behavior. Fixtures and defaults suites are synthetic.
@MainActor
struct MacCallAvailabilityTests {
    @Test func macMakesNoConversationManagerOrDelegate() {
        // LCKCallSystem owns both objects, so its factory must decline before initializing one on a Mac.
        #expect(LCKCallSystem.make(isiOSAppOnMac: true) == nil)
    }

    @Test func iPhoneOrIPadMakesLiveCommunicationKitWhenNeeded() {
        let system = LCKCallSystem.make(isiOSAppOnMac: false)
        #expect(system != nil)
    }

    @Test func macRegistersNoUrgentCallPushRegistry() throws {
        let suite = "urgent-call-mac-" + UUID().uuidString
        let defaults = try #require(UserDefaults(suiteName: suite))
        defer { defaults.removePersistentDomain(forName: suite) }
        defaults.set(true, forKey: UrgentCallPush.onKey) // left on by this device's last Settings read
        var registries: [PKPushRegistry] = []
        let push = UrgentCallPush(isiOSAppOnMac: true, defaults: defaults, makeRegistry: {
            let registry = PKPushRegistry(queue: nil)
            registries.append(registry)
            return registry
        })

        push.start() // launch
        push.set(on: true) // a Settings read or change

        #expect(registries.isEmpty)
    }

    @Test func iPhoneOrIPadRegistersForUrgentCallsOnce() throws {
        let suite = "urgent-call-phone-" + UUID().uuidString
        let defaults = try #require(UserDefaults(suiteName: suite))
        defer { defaults.removePersistentDomain(forName: suite) }
        defaults.set(true, forKey: UrgentCallPush.onKey)
        var registries: [PKPushRegistry] = []
        let push = UrgentCallPush(isiOSAppOnMac: false, defaults: defaults, makeRegistry: {
            let registry = PKPushRegistry(queue: nil)
            registries.append(registry)
            return registry
        })
        defer {
            for registry in registries {
                registry.delegate = nil
                registry.desiredPushTypes = []
            }
        }

        push.start()
        push.set(on: true)
        push.start()

        let registry = try #require(registries.count == 1 ? registries.first : nil)
        #expect(registry.delegate === push)
        #expect(registry.desiredPushTypes == Set([PKPushType.voIP]))
    }

    @Test func macSettingsDoNotOfferUrgentCalls() {
        #expect(!NotificationSettingsView.offersUrgentCalls(isiOSAppOnMac: true))
    }

    @Test func iPhoneOrIPadSettingsOfferUrgentCalls() {
        #expect(NotificationSettingsView.offersUrgentCalls(isiOSAppOnMac: false))
    }
}
