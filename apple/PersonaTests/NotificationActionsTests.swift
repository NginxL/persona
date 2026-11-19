import Foundation
import Testing
import UserNotifications
@testable import Persona

struct NotificationActionsTests {
    /// The hub names categories "INTERACTION_" + upper-cased kind (push.go); every kind must have one registered, beside
    /// the app's own calendar meeting reminder (#8731 G2).
    @Test func categoriesMatchWhatTheHubSends() {
        let ids = Set(NotificationActions.categories.map(\.identifier))
        #expect(ids == ["INTERACTION_PERMISSION", "INTERACTION_PLAN", "INTERACTION_QUESTION", "INTERACTION_SCREEN",
                        "INTERACTION_ELICITATION", "SESSION_DONE", "TODO_DECIDE", "pair_receipt", "computer_joining",
                        "MEETING_REMINDER"])
    }

    /// A decision's push (#2056) has one button, Take Recommendation, unlocked first: it answers the to-do's round.
    @Test func aDecisionPushTakesTheRecommendation() throws {
        let decide = try #require(NotificationActions.categories.first { $0.identifier == NotificationActions.decideCategory })
        #expect(decide.actions.map(\.identifier) == [NotificationActions.takeRecommendation])
        let take = try #require(decide.actions.first)
        #expect(take.options.contains(.authenticationRequired))
        #expect(!take.options.contains(.foreground))
    }

    /// The button answers the round the push carried, exactly as sent, and names the to-do's manager.
    @Test func takeRecommendationAnswersThePushedRound() {
        let at = "2026-11-12T08:30:00.123456789Z"
        let info: [AnyHashable: Any] = ["machine_id": "m1", "session_id": "s1",
                                        "todo": ["id": "t1", "asked_at": at, "recommend": "Vendor B", "manager": "work"]]
        #expect(NotificationActions.decision(action: NotificationActions.takeRecommendation, info: info)
                == .init(todoID: "t1", askedAt: at, manager: "work"))
        // From a hub that names no manager: the default one.
        let plain: [AnyHashable: Any] = ["todo": ["id": "t1", "asked_at": at, "manager": ""]]
        #expect(NotificationActions.decision(action: NotificationActions.takeRecommendation, info: plain)?.manager == nil)
        // A tap on the push opens it; another category's button and a push without its round answer nothing.
        #expect(NotificationActions.decision(action: UNNotificationDefaultActionIdentifier, info: info) == nil)
        #expect(NotificationActions.decision(action: NotificationActions.allow, info: info) == nil)
        #expect(NotificationActions.decision(action: NotificationActions.takeRecommendation, info: ["todo": ["id": "t1"]]) == nil)
        #expect(NotificationActions.decision(action: NotificationActions.takeRecommendation, info: [:]) == nil)
        #expect(NotificationActions.resolution(action: NotificationActions.takeRecommendation) == nil)
        #expect(NotificationActions.failureTitle(action: NotificationActions.takeRecommendation)
                != NotificationActions.failureTitle(action: "something else"))
    }

    /// A finished turn's push takes a typed reply, unlocked first: it is an instruction to the agent (#3449).
    @Test func aDonePushTakesAReply() throws {
        let done = try #require(NotificationActions.categories.first { $0.identifier == NotificationActions.doneCategory })
        let reply = try #require(done.actions.first { $0.identifier == NotificationActions.reply } as? UNTextInputNotificationAction)
        #expect(reply.options.contains(.authenticationRequired))
        #expect(!reply.options.contains(.foreground))
        #expect(NotificationActions.replyText(action: NotificationActions.reply, text: "  继续 \n") == "继续")
        #expect(NotificationActions.replyText(action: NotificationActions.reply, text: "  ") == nil)
        #expect(NotificationActions.replyText(action: UNNotificationDefaultActionIdentifier, text: "继续") == nil)
        #expect(NotificationActions.resolution(action: NotificationActions.reply, text: "继续") == nil)
    }

    @Test func sayingYesNeedsUnlockSayingNoDoesNot() throws {
        let cats = Dictionary(uniqueKeysWithValues: NotificationActions.categories.map { ($0.identifier, $0) })
        let permission = try #require(cats["INTERACTION_PERMISSION"])
        let allow = try #require(permission.actions.first { $0.identifier == NotificationActions.allow })
        let deny = try #require(permission.actions.first { $0.identifier == NotificationActions.deny })
        #expect(allow.options.contains(.authenticationRequired))
        #expect(!deny.options.contains(.authenticationRequired))
        #expect(!allow.options.contains(.foreground))
        let plan = try #require(cats["INTERACTION_PLAN"])
        #expect(plan.actions.allSatisfy { $0.options.contains(.authenticationRequired) })
        #expect(plan.actions.contains { $0 is UNTextInputNotificationAction })
        #expect(cats["INTERACTION_QUESTION"]?.actions.isEmpty == true)
        #expect(cats["INTERACTION_SCREEN"]?.actions.isEmpty == true)
        #expect(cats["INTERACTION_ELICITATION"]?.actions.isEmpty == true) // a form or a link needs the app (#2433)
    }

    /// The hub pushes MCP forms and links (#2433) only to an app that registers `elicitation`, and waits for it: the iOS
    /// app, which declares the cards in its hello too, never the visionOS app or an extension; every one takes withdrawals.
    @Test func onlyTheIPhoneAppRegistersForMCPForms() {
        #expect(PushRegistrar.features(RequestClientMetadata(platform: .iOS, surface: .app)) == ["withdraw", "elicitation"])
        #expect(PushRegistrar.features(RequestClientMetadata(platform: .visionOS, surface: .app)) == ["withdraw"])
        #expect(PushRegistrar.features(RequestClientMetadata(platform: .iOS, surface: .systemExtension)) == ["withdraw"])
        #expect(PushRegistrar.features(RequestClientMetadata(platform: .macOS, surface: .app)) == ["withdraw"])
    }

    @Test func buttonsMapToResolutions() {
        #expect(NotificationActions.resolution(action: NotificationActions.allow)?.allow == true)
        #expect(NotificationActions.resolution(action: NotificationActions.deny)?.allow == false)
        let approve = NotificationActions.resolution(action: NotificationActions.approvePlan)
        #expect(approve?.approve == true)
        // Never auto-run everything from a notification.
        #expect(approve?.mode == "acceptEdits")
        let reject = NotificationActions.resolution(action: NotificationActions.rejectPlan, text: "  先写测试 \n")
        #expect(reject?.approve == false)
        #expect(reject?.feedback == "先写测试")
        #expect(NotificationActions.resolution(action: UNNotificationDefaultActionIdentifier) == nil)
        #expect(NotificationActions.resolution(action: UNNotificationDismissActionIdentifier) == nil)
    }
}
