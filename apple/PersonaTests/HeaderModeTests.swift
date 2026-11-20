import Foundation
import Testing
@testable import Persona
import PersonaWire

/// The permission mode at the end of the Agents header's second row (#3719): how each mode is drawn, which agents show
/// one, what can be picked, and which switches ask first.
struct HeaderModeTests {
    private func session(_ extra: String = "", status: String = "idle") throws -> Session {
        try JSON.decoder.decode(Session.self, from: Data(
            #"{"machine_id":"box","session_id":"s1","status":"\#(status)","state":"idle","pane_id":"p1"\#(extra)}"#.utf8))
    }

    @Test func eachModeHasItsMenuSymbolAndATone() {
        #expect(SessionModes.headerLook("default") == .init(symbol: Theme.Icon.askFirst, tone: .asks))
        #expect(SessionModes.headerLook("acceptEdits") == .init(symbol: Theme.Icon.editFile, tone: .acts))
        #expect(SessionModes.headerLook("plan") == .init(symbol: Theme.Icon.plan, tone: .plans))
        #expect(SessionModes.headerLook("auto") == .init(symbol: Theme.Icon.autoMode, tone: .acts))
        #expect(SessionModes.headerLook("bypassPermissions") == .init(symbol: Theme.Icon.allowEverything, tone: .allowsAll))
        // The header and the mode menus draw a mode with the same symbol.
        for o in SessionModes.permissionModes {
            #expect(SessionModes.headerLook(o.id).symbol == o.symbol, "\(o.id)")
        }
    }

    /// Not reported yet, or a mode this app does not know: the plain permissions symbol, never a guessed Ask First.
    @Test func anUnknownModeIsNeverDrawnAsAskFirst() {
        #expect(SessionModes.headerLook(nil) == .init(symbol: Theme.Icon.permissions, tone: .unknown))
        #expect(SessionModes.headerLook("dontAsk") == .init(symbol: Theme.Icon.permissions, tone: .unknown))
    }

    /// The mode menus' title and the mode buttons' accessibility label (#5596): the mode when the agent reported one, else
    /// just "Permission Mode", as the capsule says it, never "Permission Mode: Unknown".
    @Test func anUnknownModeIsNamedWithoutAValue() throws {
        let plain = String(localized: "Permission Mode")
        #expect(SessionModes.modeHeading(nil) == plain)
        #expect(SessionModes.modeHeading("") == plain)
        #expect(!SessionModes.modeHeading(nil).contains(SessionModes.unknown))
        let ask = try #require(SessionModes.mode("default"))
        #expect(SessionModes.modeHeading("default") == String(localized: "Permission Mode: \(ask.title)"))
        #expect(SessionModes.modeHeading("default") != plain && SessionModes.modeHeading("default").contains(ask.title))
        // A mode this app does not list (a newer Claude Code's, a Codex policy) is named as it came.
        #expect(SessionModes.modeHeading("dontAsk") == String(localized: "Permission Mode: \("dontAsk")"))
    }

    @Test func claudeShowsItsModeEvenBeforeItReportsOne() throws {
        #expect(SessionModes.showsInHeader(try session(#","agent":"claude","permission_mode":"plan""#)))
        #expect(SessionModes.showsInHeader(try session(#","agent":"claude""#))) // running, not reported yet: it can be set
        #expect(SessionModes.showsInHeader(try session(#","permission_mode":"default""#, status: "offline"))) // the last one it had
    }

    @Test func noModeWhereThereIsNothingToSeeOrChange() throws {
        #expect(!SessionModes.showsInHeader(try session(#","agent":"codex""#)))
        #expect(!SessionModes.showsInHeader(try session(#","agent":"traex""#)))
        #expect(!SessionModes.showsInHeader(try session(#","agent":"claude""#, status: "offline")))
        #expect(!SessionModes.showsInHeader(try session(#","assistant":true,"permission_mode":"default""#)))
    }

    @Test func allowEverythingIsOfferedOnlyWhereTheAgentCanTakeIt() throws {
        // Not in a pane: the node can't restart it into Allow Everything (#6513), so only a flag would let it switch.
        let plain = try JSON.decoder.decode(Session.self, from: Data(
            #"{"machine_id":"box","session_id":"s1","status":"idle","state":"idle","permission_mode":"default"}"#.utf8))
        #expect(!plain.bypassAvailable)
        #expect(!SessionModes.switchableModes(plain).contains { $0.id == "bypassPermissions" })
        #expect(SessionModes.bypassUnavailable(plain)?.id == "bypassPermissions")
        let able = try session(#","permission_mode":"default","bypass_available":true"#)
        #expect(able.bypassAvailable)
        #expect(SessionModes.switchableModes(able).contains { $0.id == "bypassPermissions" })
        #expect(SessionModes.bypassUnavailable(able) == nil)
        // In it already: listed as the current one, no row saying it is unavailable.
        let inIt = try session(#","permission_mode":"bypassPermissions""#)
        #expect(SessionModes.listed(SessionModes.switchableModes(inIt), current: inIt.permissionMode).contains { $0.id == "bypassPermissions" })
        #expect(SessionModes.bypassUnavailable(inIt) == nil)
    }

    @Test func goingIntoAllowEverythingAsksFirst() throws {
        let s = try session(#","permission_mode":"default","bypass_available":true"#)
        #expect(SessionModes.confirmation(s, to: "bypassPermissions") == .allowEverything)
        #expect(SessionModes.confirmation(s, to: "acceptEdits") == nil)
        #expect(SessionModes.confirmation(s, to: "default") == nil) // what it runs already
    }

    /// Leaving Allow Everything asks only when the agent cannot come back to it from here.
    @Test func leavingAllowEverythingAsksWhenThereIsNoWayBack() throws {
        let oneWay = try session(#","permission_mode":"bypassPermissions""#)
        #expect(SessionModes.confirmation(oneWay, to: "plan") == .leaveAllowEverything)
        let canReturn = try session(#","permission_mode":"bypassPermissions","bypass_available":true"#)
        #expect(SessionModes.confirmation(canReturn, to: "plan") == nil)
    }

    /// An assistant conversation switches without asking, as on the web (its process restarts in the mode, #2416).
    @Test func theAssistantNeverAsks() throws {
        let s = try session(#","assistant":true,"permission_mode":"bypassPermissions""#)
        #expect(SessionModes.confirmation(s, to: "default") == nil)
        let t = try session(#","assistant":true,"permission_mode":"default""#)
        #expect(SessionModes.confirmation(t, to: "bypassPermissions") == nil)
        #expect(SessionModes.confirmation(nil, to: "bypassPermissions") == nil)
    }

    /// An odd `bypass_available` never drops the session; omitted (an older hub) is false.
    @Test func bypassAvailableDecodesLeniently() throws {
        #expect(try session().bypassAvailable == false)
        #expect(try session(#","bypass_available":"yes""#).bypassAvailable == false)
        #expect(try session(#","bypass_available":true"#).bypassAvailable)
    }

    // MARK: Allow Everything by a restart (#6513, the web's #6476)

    /// A terminal Claude Code started without a bypass flag takes Allow Everything by a restart once its turn ends: the
    /// row is enabled and says so, and nothing says it is unavailable.
    @Test func aPaneClaudeWithoutTheFlagRestartsIntoAllowEverything() throws {
        let s = try session(#","agent":"claude","permission_mode":"default""#)
        #expect(SessionModes.bypassRestarts(s))
        let row = try #require(SessionModes.switchableModes(s).first { $0.id == "bypassPermissions" })
        #expect(row.detail == String(localized: "Restarts the session to apply it once this turn ends; the conversation carries on"))
        #expect(SessionModes.bypassUnavailable(s) == nil)
        let rows = SessionModes.commandRows(s)
        #expect(rows.allSatisfy { $0.enabled })
        // It still asks first, and the question carries the note.
        #expect(SessionModes.confirmation(s, to: "bypassPermissions") == .allowEverything)
        #expect(SessionModes.bypassNote(s) == row.detail)
    }

    /// No restart where the agent takes the mode at once or can't be restarted from here: the old rules hold.
    @Test func noRestartWhereItIsNotNeededOrNotPossible() throws {
        // started with the flag: switches at once, no note
        let able = try session(#","agent":"claude","permission_mode":"default","bypass_available":true"#)
        #expect(!SessionModes.bypassRestarts(able))
        #expect(SessionModes.bypassNote(able) == nil)
        #expect(SessionModes.switchableModes(able).first { $0.id == "bypassPermissions" }?.detail == SessionModes.assistantModes.last?.detail)
        // in it already
        #expect(!SessionModes.bypassRestarts(try session(#","agent":"claude","permission_mode":"bypassPermissions""#)))
        // not in a pane, a source's thread, Codex, Antigravity, the assistant, a terminal the node won't drive, offline
        let cannot = [
            try JSON.decoder.decode(Session.self, from: Data(#"{"machine_id":"box","session_id":"s1","status":"idle","state":"idle","agent":"claude","permission_mode":"default"}"#.utf8)),
            try session(#","agent":"claude","source_kind":"claude_app","permission_mode":"default""#),
            try session(#","agent":"codex""#),
            try session(#","agent":"agy","permission_mode":"default","capabilities":{"send":true,"interrupt":true,"approve":true,"terminal":true,"modes":true}"#),
            try session(#","assistant":true,"permission_mode":"default""#),
            try session(#","agent":"claude","permission_mode":"default","capabilities":{"send":true,"interrupt":true,"approve":true,"terminal":false}"#),
            try session(#","agent":"claude","permission_mode":"default""#, status: "offline"),
        ]
        for s in cannot { #expect(!SessionModes.bypassRestarts(s), "\(s.agent ?? "-") \(s.status)") }
        // where it can't, a running Claude Code still shows Allow Everything disabled with its reason
        let notPane = cannot[0]
        #expect(!SessionModes.switchableModes(notPane).contains { $0.id == "bypassPermissions" })
        #expect(SessionModes.bypassUnavailable(notPane)?.id == "bypassPermissions")
        #expect(SessionModes.commandRows(notPane).last.map { !$0.enabled && $0.option.id == "bypassPermissions" } == true)
    }

    /// While the restart waits for the turn to end the mode stays the one it runs in, and every mode button says it.
    @Test func aWaitingRestartKeepsTheModeAndSaysSo() throws {
        let s = try session(#","agent":"claude","permission_mode":"acceptEdits","bypass_pending":true"#, status: "working")
        #expect(s.bypassPending)
        #expect(s.permissionMode == "acceptEdits")
        #expect(SessionModes.headerLook(s.permissionMode).tone == .acts) // not drawn as Allow Everything
        #expect(SessionModes.pending(s) == SessionModes.pendingNote)
        #expect(SessionModes.bypassNote(s) == SessionModes.pendingNote)
        #expect(SessionModes.switchableModes(s).first { $0.id == "bypassPermissions" }?.detail == SessionModes.pendingNote)
        #expect(SessionModes.pending(try session(#","agent":"claude","permission_mode":"acceptEdits""#)) == nil)
    }

    /// Picking the checked mode while a restart into Allow Everything waits sends it without a confirmation so the node
    /// calls off the restart directly instead of forcing a switch to a third mode (#7996 review).
    @MainActor @Test func pickingTheCheckedModeCallsOffAWaitingRestart() throws {
        let idle = try session(#","agent":"claude","permission_mode":"acceptEdits""#)
        #expect(!SessionModes.appliesPick(idle, to: "acceptEdits"))
        #expect(SessionModes.appliesPick(idle, to: "plan"))
        var pending = try session(#","agent":"claude","permission_mode":"acceptEdits","bypass_pending":true"#, status: "working")
        #expect(SessionModes.appliesPick(pending, to: "acceptEdits"))
        #expect(SessionModes.confirmation(pending, to: "acceptEdits") == nil)
        #expect(SessionModes.appliesPick(pending, to: "bypassPermissions"))
        #expect(SessionModes.confirmation(pending, to: "bypassPermissions") == .allowEverything)
        // Optimistic mode update mirrors the web: a restart keeps the running mode and marks bypassPending; picking the
        // checked mode (or another non-bypass mode) clears bypassPending.
        var restart = try session(#","agent":"claude","permission_mode":"acceptEdits""#, status: "working")
        SessionModes.applyOptimistic(&restart, permissionMode: "bypassPermissions")
        #expect(restart.permissionMode == "acceptEdits" && restart.bypassPending)
        SessionModes.applyOptimistic(&pending, permissionMode: "acceptEdits")
        #expect(pending.permissionMode == "acceptEdits" && !pending.bypassPending)
        // Summary rows carry bypass_pending (#10921) while omitting permission_mode / bypass_available; preservingDetails
        // keeps the omitted fields and lets the summary row's own bypass_pending win.
        let summary = try session(#","waiting":true,"bypass_pending":false"#).preservingDetails(from: restart)
        #expect(summary.permissionMode == "acceptEdits" && !summary.bypassPending)
        let stillPending = try session(#","waiting":true,"bypass_pending":true"#).preservingDetails(from: restart)
        #expect(stillPending.permissionMode == "acceptEdits" && stillPending.bypassPending)
    }

    /// An assistant conversation's Allow Everything takes effect with its next message, as on the web.
    @Test func theAssistantsAllowEverythingStartsWithTheNextMessage() throws {
        let s = try session(#","assistant":true,"permission_mode":"default""#)
        #expect(SessionModes.switchableModes(s).first { $0.id == "bypassPermissions" }?.detail == String(localized: "Takes effect from the next message"))
        let inIt = try session(#","assistant":true,"permission_mode":"bypassPermissions""#)
        #expect(SessionModes.bypassNote(inIt) == nil)
    }

    /// An odd `bypass_pending` never drops the session; omitted (an older hub) is false.
    @Test func bypassPendingDecodesLeniently() throws {
        #expect(try session().bypassPending == false)
        #expect(try session(#","bypass_pending":"yes""#).bypassPending == false)
        #expect(try session(#","bypass_pending":true"#).bypassPending)
    }

    // MARK: /permissions (#6513)

    /// Offered where a session's mode can be switched from here, with every mode (Plan Only too), Persona's own
    /// conversations included (#7996 review: as on the web and Android); never for Codex or an agent that isn't running.
    @Test func permissionsIsOfferedWhereTheModeCanBeSwitched() throws {
        let s = try session(#","agent":"claude","permission_mode":"default""#)
        #expect(SessionModes.offersPermissionsCommand(s))
        #expect(Set(SessionModes.commandRows(s).map(\.option.id)) == ["default", "acceptEdits", "plan", "auto", "bypassPermissions"])
        #expect(!SessionModes.offersPermissionsCommand(try session(#","agent":"codex""#)))
        #expect(!SessionModes.offersPermissionsCommand(try session(#","agent":"claude""#, status: "offline")))
        let persona = try session(#","assistant":true,"permission_mode":"default""#)
        #expect(SessionModes.offersPermissionsCommand(persona))
        // A Persona conversation's list: all five modes, Allow Everything taking effect from the next message.
        let rows = SessionModes.commandRows(persona)
        #expect(Set(rows.map(\.option.id)) == ["default", "acceptEdits", "plan", "auto", "bypassPermissions"])
        #expect(rows.first { $0.option.id == "bypassPermissions" }?.option.detail == String(localized: "Takes effect from the next message"))
        let allEnabled = rows.allSatisfy { $0.enabled }
        #expect(allEnabled)
        #expect(!SessionModes.offersPermissionsCommand(nil))
        #expect(SessionModes.isPermissionsCommand(" /Permissions\n"))
        #expect(!SessionModes.isPermissionsCommand("/permissions please"))
    }

    /// Sending `/permissions` opens the list only with nothing attached: a quoted context, a review comment, a photo or a
    /// video makes it a message, so what was quoted or commented is never dropped (#7996 review).
    @Test func permissionsWithSomethingAttachedIsSentAsAMessage() {
        #expect(SessionModes.opensPermissions("/permissions", offered: true, attached: false))
        #expect(!SessionModes.opensPermissions("/permissions", offered: true, attached: true))
        #expect(!SessionModes.opensPermissions("/permissions", offered: false, attached: false))
        #expect(!SessionModes.opensPermissions("/permissions now", offered: true, attached: false))
    }

    /// The "/" list's row (the composer's, main-actor bound).
    @MainActor @Test func theSlashListCarriesPermissionsWhereOffered() {
        #expect(SlashMenuRow.rows(model: false, permissions: true, clear: false).contains(.permissions))
        #expect(!SlashMenuRow.rows(model: false, clear: false).contains(.permissions))
        #expect(SlashMenuRow.matching("/perm", in: SlashMenuRow.rows(model: true, permissions: true, clear: true)) == [.permissions])
    }

    /// Antigravity (`agy`) switches all five modes including Allow Everything directly via ACP (no restart, no
    /// confirmation, matching the web), and keeps `/permissions` even when the session also reports thinking `efforts`.
    @Test func antigravityOffersAllowEverythingAndPermissionsDirectly() throws {
        let agy = try session(#","agent":"agy","source_kind":"runtime","permission_mode":"auto","efforts":["low","high"],"capabilities":{"read":true,"send":true,"modes":true}"#)
        #expect(agy.canChangeModes)
        #expect(!agy.setsEffortOnly)
        #expect(SessionModes.showsInHeader(agy))
        #expect(SessionModes.offersPermissionsCommand(agy))
        #expect(!SessionModes.bypassRestarts(agy))
        #expect(SessionModes.bypassNote(agy) == nil)
        #expect(SessionModes.bypassUnavailable(agy) == nil)
        let rows = SessionModes.commandRows(agy)
        #expect(Set(rows.map(\.option.id)) == ["default", "acceptEdits", "plan", "auto", "bypassPermissions"])
        #expect(rows.allSatisfy { $0.enabled })
        #expect(rows.first { $0.option.id == "bypassPermissions" }?.option.detail == SessionModes.assistantModes.last?.detail)
        #expect(SessionModes.confirmation(agy, to: "bypassPermissions") == nil)
        let inBypass = try session(#","agent":"agy","source_kind":"runtime","permission_mode":"bypassPermissions","capabilities":{"read":true,"send":true,"modes":true}"#)
        #expect(SessionModes.confirmation(inBypass, to: "default") == nil)
    }
}
