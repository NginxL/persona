import Foundation
import SwiftUI
import Testing
import UIKit
@testable import Persona

/// The call to Persona's page (#9290, docs/design/DESIGN_SYSTEM.md "Call screen"): its one line and its orb from the
/// call's state, the page shown again when the app comes back, Minimise never ending the call, the + panel's Call
/// Persona beside Voice Chat, and every control going through `PersonaCalls` (read from the sources).
@MainActor
struct CallScreenTests {
    private static let since = Date(timeIntervalSinceReferenceDate: 1_000)

    private static func state(_ phase: CallPhase, held: Bool = false, muted: Bool = false, note: String? = nil,
                              voice: (VoicePhase, VoiceLink)? = nil, reconnecting: Bool = false) -> CallScreenState {
        CallScreenState.make(phase: phase, held: held, muted: muted, note: note, voice: voice.map { (phase: $0.0, link: $0.1) },
                             reconnecting: reconnecting)
    }

    @Test func theLineSaysWhereTheCallIs() {
        #expect(Self.state(.dialing) == CallScreenState(line: .calling, orb: .connecting))
        #expect(Self.state(.connecting) == CallScreenState(line: .connecting, orb: .connecting))
        #expect(Self.state(.connected(since: Self.since), voice: (.live(.listening), .live)).line == .duration(since: Self.since))
        #expect(Self.state(.ended(.failed), note: "Can't reach the hub") == CallScreenState(line: .ended(note: "Can't reach the hub"), orb: .ended))
        #expect(Self.state(.ended(.hungUp)).line == .ended(note: nil))
    }

    /// On hold wins over everything else the voice says; the voice's connection being picked up again shows as
    /// Reconnecting…, from its phase or its link (a sample has neither: its flag stands in).
    @Test func onHoldAndReconnectingTakeTheLine() {
        let on = CallPhase.connected(since: Self.since)
        #expect(Self.state(on, held: true, voice: (.reconnecting(attempt: 1), .live)) == CallScreenState(line: .onHold, orb: .paused))
        #expect(Self.state(on, voice: (.reconnecting(attempt: 2), .live)) == CallScreenState(line: .reconnecting, orb: .connecting))
        #expect(Self.state(on, voice: (.live(.listening), .reconnecting)).line == .reconnecting)
        #expect(Self.state(on, reconnecting: true).line == .reconnecting)
        #expect(Self.state(on).line == .duration(since: Self.since))
    }

    /// The orb moves as the voice bar's does: speaking, hearing, muted; the upstream paused is the call listening, not
    /// on hold (#9887: only the call's own hold is a hold).
    @Test func theOrbFollowsTheVoice() {
        let on = CallPhase.connected(since: Self.since)
        #expect(Self.state(on, voice: (.live(.speaking), .live)).orb == .speaking)
        #expect(Self.state(on, voice: (.live(.hearing), .live)).orb == .hearing)
        #expect(Self.state(on, muted: true, voice: (.live(.listening), .live)).orb == .muted)
        #expect(Self.state(on, voice: (.live(.listening), .paused)).orb == .listening)
        #expect(Self.state(on, voice: (.live(.listening), .paused)).line == .duration(since: Self.since))
        #expect(Self.state(on, muted: true).orb == .muted, "a sample, no voice")
        #expect(Self.state(on).orb == .listening)
    }

    /// Minimise only hides the page; coming back to the app (the Dynamic Island, the Lock Screen's call) shows it again
    /// while the call is on, and never brings back a call that is over or an urgent call.
    @Test func comingBackToTheAppShowsTheCallAgain() async {
        let rig = CallRig()
        let (call, socket) = await rig.connected()
        rig.calls.presented = true
        rig.calls.minimise()
        #expect(!rig.calls.presented && call.phase.isLive && !socket.closed && rig.system.ended.isEmpty)
        rig.calls.returnedToApp()
        #expect(rig.calls.presented)
        rig.calls.minimise()
        rig.calls.hangUp()
        rig.system.act(.end, call.id)
        #expect(call.phase.isEnded)
        rig.calls.returnedToApp()
        #expect(!rig.calls.presented, "an ended call's page does not come back")
    }

    @Test func anUrgentCallIsNotTheCallScreens() async throws {
        let rig = CallRig()
        try await rig.calls.ring(UUID(), callID: "call_1", name: "Urgent")
        #expect(rig.calls.ownerCall == nil)
        rig.calls.returnedToApp()
        rig.calls.present()
        #expect(!rig.calls.presented)
    }

    /// Reading what the cover and the bar read makes no system call manager (#9264): only a call does.
    @Test func theCoverMakesNoCallManager() {
        var made = 0
        let calls = PersonaCalls(system: { made += 1; return FakeCallSystem() }, voiceCalls: VoiceCalls())
        _ = calls.presented
        _ = calls.ownerCall
        _ = calls.refusal
        _ = calls.route.external
        _ = calls.route.speakerOn
        calls.returnedToApp()
        calls.minimise()
        #expect(made == 0 && !calls.systemMade)
    }

    /// The + panel (and the iPad's + menu, the iPhone's touch and hold) has no Voice Chat or Call Persona rows any more
    /// (#9887 one button): the composer's phone icon is the one voice entry. The camera leads its rows.
    @Test func thePlusHasNoVoiceOrCallRows() throws {
        let rows = AttachMoreSection.common(camera: {}, meeting: {}, mention: {}, context: { _ in })
        let ids = rows.flatMap(\.rows).map(\.id)
        #expect(!ids.contains("composer-menu-voice") && !ids.contains("composer-menu-call"))
        let capture = try #require(rows.first { $0.id == "capture" })
        #expect(capture.rows.first?.id == "composer-menu-camera")
        let chat = try Self.code("Persona/Views/Chat/ChatView.swift")
        #expect(!chat.contains("callPersonaEntry") && !chat.contains("AttachMoreSection.common(voice:"))
    }

    /// Call Persona is one glyph wherever it is offered (#9290): the + panel's (above), the App Shortcut's (a literal
    /// there) and the Continue of a call that ended (a new system call), apart from Voice Chat's handset.
    @Test func callPersonaHasOneGlyphEverywhere() throws {
        let shortcuts = try Self.code("Persona/Intents/PersonaShortcuts.swift")
        let call = try #require(shortcuts.range(of: "intent: CallPersonaIntent()"))
        let entry = String(shortcuts[call.upperBound...].prefix(600))
        #expect(entry.contains("systemImageName: \"\(Theme.Icon.callPersona)\""))
        let strip = try Self.code("Persona/Views/Voice/VoiceEntry.swift")
        #expect(strip.contains("systemImage: call.surface == .call ? Theme.Icon.callPersona : Theme.Icon.voiceChannel"))
    }

    /// The return-to-call bar's room at the window root is every page's, the Separate layout's iPad tabs too: only what
    /// a tab is given above the root's top (the tab bar's second room) is taken back off it.
    @Test func theTabsKeepTheCallBarsRoom() {
        let status: CGFloat = 24, bar: CGFloat = 52, tabBar: CGFloat = 64
        #expect(TabBarExtraRoom.extra(given: status + bar + tabBar, rootTop: status + bar) == tabBar)
        #expect(TabBarExtraRoom.extra(given: status + bar, rootTop: status + bar) == 0, "the bar's room stays")
        #expect(TabBarExtraRoom.extra(given: status + tabBar, rootTop: status) == tabBar, "no bar: as before")
    }

    /// The assistant gives way on a short page or at a large text size: to its share of the page, then to the words
    /// around it (the room the middle has less theirs); where not even the voice bar's orb's size fits, it goes and the
    /// words stay.
    @Test func theAssistantGivesWayToTheControlsAndTheWords() throws {
        #expect(CallScreen.avatarSize(page: 0, free: 0, scale: 1) == Theme.Size.callAvatar, "not measured yet")
        #expect(CallScreen.avatarSize(page: 900, free: .infinity, scale: 1) == Theme.Size.callAvatar)
        let large = try #require(CallScreen.avatarSize(page: 740, free: .infinity, scale: 1.4))
        #expect(large * 1.4 <= 740 * Theme.Size.callAvatarShare + 0.01 && large < Theme.Size.callAvatar)
        #expect(CallScreen.avatarSize(page: 900, free: 90, scale: 1) == 90, "what the words leave")
        let grown = try #require(CallScreen.avatarSize(page: 900, free: 90, scale: 1.25))
        #expect(abs(grown * 1.25 - 90) < 0.01)
        #expect(CallScreen.avatarSize(page: 900, free: Theme.Size.voiceOrb, scale: 1) == Theme.Size.voiceOrb)
        #expect(CallScreen.avatarSize(page: 900, free: Theme.Size.voiceOrb - 1, scale: 1) == nil, "no room for even the smallest face")
        #expect(CallScreen.avatarSize(page: 100, free: .infinity, scale: 1) == nil)
    }

    #if DEBUG
    /// Every control stays on the page whatever the window, at the largest interface text size with Resume offered and a
    /// two-line caption, and the call's line and the caption stay in sight between Minimise and the controls: the
    /// narrowest iPad window (Slide Over or Split View on an iPad mini in landscape, 320 × 720 under the status bar; Mute,
    /// Speaker and Resume did not fit one row) and a short phone (an iPhone SE's 375 × 647 under its status bar; the page
    /// was taller than the screen). The page is hosted in a window of exactly that room and read as XCUITest reads it
    /// (`RuntimeCLI.elements`).
    @Test(arguments: [CGSize(width: 320, height: 720), CGSize(width: 375, height: 647)])
    func everyControlStaysOnThePage(window size: CGSize) async throws {
        let rig = CallRig()
        rig.calls.showSample(.resume)
        let call = try #require(rig.calls.ownerCall)
        #expect(call.resumeOffered && call.sampleCaption != nil)
        try automation(true)
        defer { try? automation(false) }
        let scene = try #require(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let window = UIWindow(windowScene: scene)
        defer { window.isHidden = true }
        window.frame = CGRect(origin: CGPoint(x: 0, y: 100), size: size)
        // The assistant's face reads the app's persona, as the call's window gives it (`CallWindowRoot`).
        let host = UIHostingController(rootView: CallScreen(call: call).dynamicTypeSize(.xxxLarge).environment(AppModel.shared))
        // The window is the room under the status bar: the test screen's own insets (its island, its home bar) are not.
        host.safeAreaRegions = []
        window.rootViewController = host
        window.isHidden = false
        window.layoutIfNeeded()

        let controls = ["call-minimise", "call-mute", "call-speaker", "call-resume", "call-end"]
        let ids = controls + ["call-state", "call-caption"]
        func frames() -> [String: CGRect] {
            try? automation(true) // again: a test hosting its own page may have turned it off meanwhile
            var out: [String: CGRect] = [:]
            for e in RuntimeCLI.elements(matching: [:]) where ids.contains(e.id) && RuntimeCLI.view(of: e.object)?.window === window {
                out[e.id] = e.frame
            }
            return out
        }
        var found = frames()
        for _ in 0..<30 where found.count < ids.count {
            try? await Task.sleep(for: .milliseconds(100))
            found = frames()
        }
        // The page measures itself once (the orb's room): its second layout.
        try? await Task.sleep(for: .milliseconds(300))
        found = frames()
        let page = window.frame.insetBy(dx: -0.5, dy: -0.5)
        for id in controls {
            let frame = try #require(found[id], "\(id) is not on the page (\(size))")
            #expect(page.contains(frame), "\(id) at \(frame) is outside the \(size) window \(window.frame)")
        }
        // The line and the caption in sight: under Minimise, over the controls, not scrolled away.
        let top = try #require(found["call-minimise"]).maxY - 0.5
        let bottom = controls.dropFirst().compactMap { found[$0]?.minY }.min() ?? page.maxY
        for id in ["call-state", "call-caption"] {
            let frame = try #require(found[id], "\(id) is not on the page (\(size))")
            #expect(frame.minY >= top && frame.maxY <= bottom + 0.5, "\(id) at \(frame) is not between Minimise and the controls (\(top)…\(bottom))")
        }
        for (a, b) in [("call-mute", "call-speaker"), ("call-speaker", "call-resume"), ("call-mute", "call-resume"), ("call-resume", "call-end")] {
            if let fa = found[a], let fb = found[b] { #expect(!fa.insetBy(dx: 1, dy: 1).intersects(fb), "\(a) over \(b)") }
        }
        shot(window, "call-held-resume-\(Int(size.width))x\(Int(size.height))-ax")
    }

    // MARK: Long texts (#9887 polish)

    /// An Agent session's long title, a long answer and a long reason, as `CallScreenTests.testTheLongTexts` (UI) has them.
    private static let longName = "重构支付服务的订单对账模块并补齐集成测试和回归用例" // i18n-ignore: a test's sample title
    private static let longCaption = "我看了一下，对账模块的问题出在部分退款的金额汇总上，昨晚的批次里有十七笔对不上。" // i18n-ignore: a test's sample answer
        + "这几笔的流水号和订单号我已经整理好，放在会话里了。修复的办法是按支付渠道分别汇总，再补一组覆盖部分退款的集成测试。" // i18n-ignore: a test's sample answer
        + "回归用例我列了二十条，全部跑完大约要四十分钟。要不要我现在就开始改？" // i18n-ignore: a test's sample answer
    private static let longReason = "这台电脑上的语音扩展一直没有响应，已经自动重试了三次仍然失败，请确认电脑在线后再打一次" // i18n-ignore: a test's sample reason

    /// The page at `size` and `type` in a window of exactly that room, its elements as XCUITest reads them once it has
    /// measured itself (the orb's room: its second layout).
    private func hosted(_ view: some View, _ size: CGSize, _ type: DynamicTypeSize, ids: [String]) async throws -> (UIWindow, [String: RuntimeCLI.Element]) {
        try automation(true)
        let scene = try #require(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let window = UIWindow(windowScene: scene)
        window.frame = CGRect(origin: CGPoint(x: 0, y: 100), size: size)
        let host = UIHostingController(rootView: view.dynamicTypeSize(type).environment(AppModel.shared))
        host.safeAreaRegions = []
        window.rootViewController = host
        window.isHidden = false
        window.layoutIfNeeded()
        func found() -> [String: RuntimeCLI.Element] {
            try? automation(true)
            var out: [String: RuntimeCLI.Element] = [:]
            for e in RuntimeCLI.elements(matching: [:]) where ids.contains(e.id) && RuntimeCLI.view(of: e.object)?.window === window {
                out[e.id] = e
            }
            return out
        }
        var elements = found()
        for _ in 0..<30 where elements.count < ids.count {
            try? await Task.sleep(for: .milliseconds(100))
            elements = found()
        }
        try? await Task.sleep(for: .milliseconds(300))
        return (window, found())
    }

    /// How tall `text` is, whole, at `width`: what a line that is never cut takes.
    private func wholeHeight(_ text: some View, width: CGFloat, _ type: DynamicTypeSize) -> CGFloat {
        let host = UIHostingController(rootView: text.dynamicTypeSize(type))
        return host.sizeThatFits(in: CGSize(width: width, height: .greatestFiniteMagnitude)).height
    }

    /// A long Agent title, a long answer and a long reason (#9887 polish): VoiceOver hears each whole (their labels are
    /// the whole texts), the reason's line is as tall as all of it (never cut), the name and the line stay between
    /// Minimize and the controls and the caption begins there, the controls on the page: an iPhone 17 Pro's room under
    /// its status bar at the default and the largest interface text size, and a short phone's (an iPhone SE's) at the
    /// largest.
    @Test(arguments: zip([CGSize(width: 402, height: 812), CGSize(width: 402, height: 812), CGSize(width: 375, height: 647)],
                         [DynamicTypeSize.large, .xxxLarge, .xxxLarge]))
    func theLongTextsStayWhole(window size: CGSize, type: DynamicTypeSize) async throws {
        defer { try? automation(false) }
        let rig = CallRig()
        let controls = ["call-minimise", "call-mute", "call-speaker", "call-end"]
        for ended in [false, true] {
            rig.calls.showSample(ended ? .ended : .connected, name: Self.longName, caption: Self.longCaption, reason: Self.longReason)
            let call = try #require(rig.calls.ownerCall)
            let words = ["call-title", ended ? "call-state" : "call-duration"] + (ended ? [] : ["call-caption"])
            let (window, found) = try await hosted(CallScreen(call: call), size, type, ids: controls + words)
            defer { window.isHidden = true }
            let what = "\(ended ? "ended" : "connected") at \(size), \(type)"
            #expect(found["call-title"]?.label == Self.longName, "the name is not whole for VoiceOver (\(what))")
            if !ended { #expect(found["call-caption"]?.label == Self.longCaption, "the caption is not whole for VoiceOver (\(what))") }
            let page = window.frame.insetBy(dx: -0.5, dy: -0.5)
            for id in controls {
                let frame = try #require(found[id]?.frame, "\(id) is not on the page (\(what))")
                #expect(page.contains(frame), "\(id) at \(frame) is outside the window \(window.frame) (\(what))")
            }
            let top = try #require(found["call-minimise"]).frame.maxY - 0.5
            let bottom = controls.dropFirst().compactMap { found[$0]?.frame.minY }.min() ?? page.maxY
            // The name and the line whole between Minimize and the controls; the caption at least begins there (on a
            // short phone at the largest size the middle scrolls for its last lines: it is whole in the details).
            for id in words {
                let frame = try #require(found[id]?.frame, "\(id) is not on the page (\(what))")
                let end = id == "call-caption" ? frame.minY : frame.maxY
                #expect(frame.minY >= top && end <= bottom + 0.5, "\(id) at \(frame) is not between Minimize and the controls (\(what))")
            }
            if ended {
                let line = try #require(found["call-state"])
                #expect(line.label.contains(Self.longReason), "the reason is not whole for VoiceOver: \(line.label)")
                let whole = wholeHeight(CallStateWords(line: .ended(note: Self.longReason)).font(Theme.Font.body), width: line.frame.width + 1, type)
                #expect(line.frame.height >= whole - 1, "the reason's line is cut: \(line.frame.height) of \(whole) (\(what))")
            }
            shot(window, "call-long-\(ended ? "ended" : "connected")-\(Int(size.width))x\(Int(size.height))-\(type)")
        }
    }

    /// The bar back to a minimised call with a long name (#9887 polish): VoiceOver hears the name whole and the time,
    /// and the bar stays inside the page's width; the name is cut in the middle where it has to be, the time never.
    @Test(arguments: [DynamicTypeSize.large, .xxxLarge])
    func theBarKeepsTheTimeAndTheWholeName(type: DynamicTypeSize) async throws {
        defer { try? automation(false) }
        let rig = CallRig()
        rig.calls.showSample(.connected, name: Self.longName)
        let call = try #require(rig.calls.ownerCall)
        let size = CGSize(width: 402, height: 120)
        let bar = CallReturnPill(call: call) {}.padding(.horizontal, Theme.Space.page)
        let (window, found) = try await hosted(bar, size, type, ids: ["call-return-bar"])
        defer { window.isHidden = true }
        let element = try #require(found["call-return-bar"], "no bar (\(type))")
        #expect(element.label.hasPrefix(Self.longName), "the bar's name is not whole for VoiceOver: \(element.label)")
        #expect(element.label.contains(":"), "the bar's time is not there: \(element.label)")
        #expect(window.frame.insetBy(dx: -0.5, dy: -0.5).contains(element.frame), "the bar at \(element.frame) is wider than the page")
        let words = try Self.code("Persona/Calls/CallScreen.swift")
        let barWords = try #require(words.range(of: "struct CallBarWords"))
        let body = String(words[barWords.upperBound...].prefix(700))
        #expect(body.contains(".truncationMode(.middle)") && body.contains(".fixedSize()"), "the name is not cut in the middle, or the time may be cut")
        let voice = try Self.code("Persona/Views/Voice/VoiceReturnBar.swift")
        #expect(voice.contains("CallBarWords(title:"), "the voice chat's bar has its own words")
        shot(window, "call-long-bar-\(type)")
    }

    /// The composer's phone icon says what its tap does (#9887 review), in both its shapes (round in send's place, plain
    /// beside stop) and in each language: on the iPhone it calls this conversation, in the conversation a call is with it
    /// shows that call, and where it is the voice chat in the app it starts that. The meeting's own button stays Voice Chat.
    @Test func thePhoneIconSaysWhatItDoes() async throws {
        defer { try? automation(false) }
        #expect(VoiceEntry.iconLabel(ownCall: false, systemCalls: true) == String(localized: "Call This Conversation"))
        #expect(VoiceEntry.iconLabel(ownCall: true, systemCalls: true) == String(localized: "Show Call"))
        #expect(VoiceEntry.iconLabel(ownCall: true, systemCalls: false) == String(localized: "Show Call"))
        #expect(VoiceEntry.iconLabel(ownCall: false, systemCalls: false) == String(localized: "Start Voice Chat"))
        let keys = ["Call This Conversation", "Show Call", "Start Voice Chat"]
        let words: [String: [String]] = [
            "en": keys,
            "zh-Hans": ["呼叫这段对话", "显示通话", "开始语音对话"], // i18n-ignore: the catalog's words
            "ja": ["この会話に電話", "通話を表示", "音声チャットを開始"], // i18n-ignore: the catalog's words
        ]
        for (language, expected) in words.sorted(by: { $0.key < $1.key }) {
            let path = try #require(Bundle.main.path(forResource: language, ofType: "lproj"), "no \(language) strings")
            let bundle = try #require(Bundle(path: path))
            let labels = keys.map { bundle.localizedString(forKey: $0, value: nil, table: nil) }
            #expect(labels == expected, "\(language)")
            for round in [true, false] {
                let label = labels[round ? 0 : 2]
                let button = ComposerVoiceButton(round: round, id: "phone-icon", label: label) {}
                let (window, found) = try await hosted(button, CGSize(width: 120, height: 80), .large, ids: ["phone-icon"])
                window.isHidden = true
                #expect(found["phone-icon"]?.label == label, "\(language), \(round ? "round" : "plain"): \(found["phone-icon"]?.label ?? "no icon")")
            }
        }
        let bar = try Self.code("Persona/DesignSystem/ShellComposerBar.swift")
        #expect(bar.contains("ComposerVoiceButton(round: stop == nil, id: \"shell-composer-voice\", label: voiceLabel ??"))
        let chat = try Self.code("Persona/Views/Chat/ChatView.swift")
        #expect(chat.contains("voiceLabel: VoiceEntry.iconLabel(key),"), "the composer passes no label")
        let meeting = try Self.code("Persona/Recording/MeetingPageBar.swift")
        #expect(meeting.contains("ComposerVoiceButton(round: false, id: \"meeting-voice\") {"), "the meeting's button keeps Voice Chat")
    }

    /// The name takes two lines at the regular text sizes and three from xLarge up (as much of a long title as two hold
    /// at the default size), the caption more than the two that cut a long answer, and none once the call is over.
    @Test func theNameAndTheCaptionTakeTheirLines() {
        #expect(CallScreen.nameLines(.medium) == 2 && CallScreen.nameLines(.large) == 2)
        #expect(CallScreen.nameLines(.xLarge) == 3 && CallScreen.nameLines(.xxxLarge) == 3)
        #expect(Theme.Call.captionLines > 2 && Theme.Call.nameShrink < 1)
        #expect(CallScreenState.Line.ended(note: "x").isEnded && !CallScreenState.Line.calling.isEnded, "the caption goes once it is over")
    }

    /// The hosted page's picture, for a person to look at (`PERSONA_SHOTS`, as the UI tests').
    private func shot(_ window: UIWindow, _ name: String) {
        guard let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] else { return }
        let image = UIGraphicsImageRenderer(bounds: window.bounds).image { _ in
            window.drawHierarchy(in: window.bounds, afterScreenUpdates: true)
        }
        try? FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
        try? image.pngData()?.write(to: URL(fileURLWithPath: dir + "/\(name).png"))
    }

    /// With no assistive technology running SwiftUI builds no accessibility elements: on as XCUITest turns it on, and off
    /// again after, for the tests that follow in this process (as `QueueStripHintTests`).
    private func automation(_ on: Bool) throws {
        let handle = try #require(dlopen("/usr/lib/libAccessibility.dylib", RTLD_NOW))
        let set = try #require(dlsym(handle, "_AXSSetAutomationEnabled"))
        typealias Enable = @convention(c) (Int32) -> Void
        unsafeBitCast(set, to: Enable.self)(on ? 1 : 0)
    }
    #endif

    @Test func theAudioControlNamesTheRoute() {
        #expect(CallAudioRouteControl.name(.bluetooth("AirPods Pro")) == "AirPods Pro")
        #expect(!CallAudioRouteControl.name(.speaker).isEmpty && !CallAudioRouteControl.name(.receiver).isEmpty)
        #expect(CallAudioRouteControl.name(.speaker) != CallAudioRouteControl.name(.receiver))
    }

    /// A round control fills only while its toggle is on.
    @Test func onlyAToggleThatIsOnIsOn() {
        #expect(CallControlKind.toggle(on: true).isOn)
        #expect(!CallControlKind.toggle(on: false).isOn && !CallControlKind.action.isOn && !CallControlKind.end.isOn)
    }

    // MARK: The sources

    private static let apple = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()

    private static func code(_ path: String) throws -> String {
        try String(contentsOf: apple.appending(path: path), encoding: .utf8).components(separatedBy: "\n")
            .map { l in l.range(of: "//").map { String(l[..<$0.lowerBound]) } ?? l }
            .joined(separator: "\n")
    }

    /// The call screen and its controls act only through `PersonaCalls` (so the system's call UI and AirPods say the
    /// same): never on the voice's mute, its hold, the in-app calls or the audio session themselves.
    @Test func theCallScreenActsThroughPersonaCalls() throws {
        for file in ["Persona/Calls/CallScreen.swift", "Persona/Calls/CallControls.swift", "Persona/Calls/CallAvatar.swift",
                     "Persona/Calls/CallWindow.swift"] {
            let code = try Self.code(file)
            for direct in ["VoiceCalls", "setCallMuted", "setHeld(", "muted.toggle", "muted = ", "overrideOutputAudioPort",
                           "AVAudioSession", ".end(."] {
                #expect(!code.contains(direct), "\(file) uses \(direct)")
            }
        }
        let screen = try Self.code("Persona/Calls/CallScreen.swift") + Self.code("Persona/Calls/CallWindow.swift")
        for action in ["calls.setMuted(", "calls.setSpeaker(", "calls.resume(", "calls.hangUp(", "calls.minimise()", "calls.present()",
                       "calls.returnedToApp()"] {
            #expect(screen.contains(action), "the call screen has no \(action)")
        }
    }

    /// A system call's voice bar hangs up, mutes and expands through `PersonaCalls`; its ended line calls its conversation
    /// again (#9887).
    @Test func aSystemCallsBarAndEndedLineGoThroughPersonaCalls() throws {
        let bar = try Self.code("Persona/Views/Voice/VoiceBar.swift")
        #expect(bar.contains("if call.surface == .call { return PersonaCalls.shared.setMuted(!call.muted) }"))
        #expect(bar.contains("if call.surface == .call { return PersonaCalls.shared.hangUp() }"))
        #expect(bar.contains("PersonaCalls.shared.present()"))
        let strip = try Self.code("Persona/Views/Voice/VoiceEntry.swift")
        #expect(strip.contains("Task { await PersonaCalls.shared.callConversation(key) }"))
    }

    /// Urgent calls start on the earpiece as calls to Persona do (owner decision 2026-11-17): one call category for both
    /// kinds, with no loudspeaker by default; the urgent page has the Speaker control that moves it.
    @Test func anUrgentCallStartsOnTheEarpiece() throws {
        let calls = try Self.code("Persona/Calls/PersonaCalls.swift")
        let prepare = try #require(calls.range(of: "static func setCallCategory()"))
        let body = String(calls[prepare.upperBound...].prefix(400))
        #expect(body.contains("PhoneVoiceAudio.callSessionOptions") && !body.contains("PhoneVoiceAudio.sessionOptions"))
        #expect(!PhoneVoiceAudio.callSessionOptions.contains(.defaultToSpeaker))
        let owner = try Self.code("Persona/Calls/OwnerCall.swift")
        #expect(owner.contains("await prepareAudio(call)"), "a call to Persona's is the same")
        let page = try Self.code("Persona/Calls/UrgentCallView.swift")
        #expect(page.contains("calls.setSpeaker(") && page.contains("CallAudioRouteControl("))
    }

    /// The page is the app's, over everything (its own window), next to the urgent call's cover.
    @Test func theCoverIsTheAppsNextToTheUrgentCalls() throws {
        let app = try Self.code("Persona/App/PersonaApp.swift").components(separatedBy: "\n").map { $0.trimmingCharacters(in: .whitespaces) }
        let cover = try #require(app.firstIndex(of: ".personaCallCover()"))
        #expect(app[cover + 1] == ".urgentCallCover()")
    }
}
