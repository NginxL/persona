import Foundation
@testable import Persona
import Testing

/// New Chat's check (#2189): a miss is written only when no new-conversation page appeared within the grace, with labels
/// only; a page that appeared in time writes nothing. The start request and private send path have no runtime seam, so
/// #1842's wire and branch regressions read those declarations without prescribing a new helper.
@MainActor struct NewChatCheckTests {
    private func check() -> NewChatCheck { NewChatCheck(grace: .milliseconds(100), more: .milliseconds(400)) }

    @Test func aPageThatComesInTimeWritesNothing() async {
        let c = check()
        var lines: [String] = []
        c.watch(from: "main", current: { "none" }, miss: { lines.append($0) })
        c.homeAppeared()
        try? await Task.sleep(for: .milliseconds(800))
        #expect(lines.isEmpty)
    }

    @Test func noPageWritesOneLineOfLabels() async {
        let c = check()
        var lines: [String] = []
        c.watch(from: "main", current: { "none" }, miss: { lines.append($0) })
        try? await Task.sleep(for: .milliseconds(800))
        #expect(lines == ["new_chat_check from=main current_early=none current_late=none drawn_late=chat"])
    }

    @Test func aPageThatComesLateSaysSo() async {
        let c = check()
        var lines: [String] = []
        c.watch(from: "main", current: { "main" }, miss: { lines.append($0) })
        try? await Task.sleep(for: .milliseconds(250))
        c.homeAppeared()
        try? await Task.sleep(for: .milliseconds(550))
        #expect(lines == ["new_chat_check from=main current_early=main current_late=main drawn_late=home"])
    }

    @Test func aSecondTapStartsOver() async {
        let c = check()
        var lines: [String] = []
        c.watch(from: "main", current: { "none" }, miss: { lines.append("first " + $0) })
        c.watch(from: "other", current: { "none" }, miss: { lines.append("second " + $0) })
        try? await Task.sleep(for: .milliseconds(800))
        #expect(lines.count == 1)
        #expect(lines.first?.hasPrefix("second new_chat_check from=other") == true)
    }

    @Test func namesAreMainOtherOrNone() {
        let main = SessionKey(machineID: "server", sessionID: "a")
        let other = SessionKey(machineID: "server", sessionID: "b")
        #expect(NewChatCheck.name(nil, main: main) == "none")
        #expect(NewChatCheck.name(main, main: main) == "main")
        #expect(NewChatCheck.name(other, main: main) == "other")
    }

    // MARK: - #1842 request and send source guards

    private static let apple = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()

    private static func source(_ path: String) throws -> String {
        try String(contentsOf: apple.appending(path: path), encoding: .utf8)
    }

    /// The balanced-brace declaration beginning at `marker`.
    private static func block(_ marker: String, in source: String) -> String? {
        let lines = source.components(separatedBy: "\n")
        guard let first = lines.firstIndex(where: { $0.contains(marker) }) else { return nil }
        var depth = 0, opened = false, out: [String] = []
        for line in lines[first...] {
            out.append(line)
            for c in line {
                if c == "{" { depth += 1; opened = true }
                if c == "}" { depth -= 1 }
            }
            if opened, depth == 0 { return out.joined(separator: "\n") }
        }
        return nil
    }

    /// The `api.start(.init …)` call whose arguments name Traex.
    private static func traexStartCall(in source: String) -> String? {
        guard let agent = source.range(of: "agentType: \"traex\""),
              let beginning = source[..<agent.lowerBound].range(of: "api.start(.init", options: .backwards) else { return nil }
        let rest = source[beginning.lowerBound...]
        var depth = 0
        for i in rest.indices {
            if rest[i] == "(" { depth += 1 }
            if rest[i] == ")" {
                depth -= 1
                if depth == 0 { return String(rest[...i]) }
            }
        }
        return nil
    }

    /// One top-level argument of a call; nil when the argument is absent.
    private static func argument(_ name: String, in call: String) -> String? {
        guard let label = call.range(of: name + ":") else { return nil }
        let rest = call[label.upperBound...]
        var depth = 0
        for i in rest.indices {
            switch rest[i] {
            case "(", "[", "{": depth += 1
            case ")", "]", "}":
                if depth == 0 { return String(rest[..<i]).trimmingCharacters(in: .whitespacesAndNewlines) }
                depth -= 1
            case "," where depth == 0:
                return String(rest[..<i]).trimmingCharacters(in: .whitespacesAndNewlines)
            default: break
            }
        }
        return String(rest).trimmingCharacters(in: .whitespacesAndNewlines)
    }

    @Test func startRequestHasTraexAgentTypeCoding() throws {
        let api = try Self.source("Shared/APIClient.swift")
        let request = Self.block("struct StartRequest: Encodable", in: api)
        #expect(request?.contains("var agentType: String?") == true)
        #expect(request?.contains("agentType = \"agent_type\"") == true)
    }

    @Test func untouchedStartRequestsOmitAgentType() throws {
        for launcher in ["claude", "codex", "grok", "future-agent"] {
            let request = APIClient.StartRequest(machineID: "box", cwd: "/w", launcher: launcher)
            let body = try JSONSerialization.jsonObject(with: JSONEncoder().encode(request)) as? [String: Any]
            #expect(body?["agent_type"] == nil, "\(launcher)")
        }
    }

    /// Text alone goes in the synchronous start; the generic async start and its separate first message are not reused.
    @Test func terminalTraexTextOnlyStartsSynchronouslyWithoutAFollowUp() throws {
        let view = try Self.source("Persona/Views/NewChat/NewChatView.swift")
        let start = Self.block("private func start() async", in: view)
        let call = start.flatMap { Self.traexStartCall(in: $0) }
        #expect(call != nil, "a known terminal Traex needs a start carrying agent_type=traex")
        guard let start, let call, let callRange = start.range(of: call) else { return }

        // Keep this boundary on the Traex branch: nonempty composed text and no photos, files or uploads.
        let lead = start[..<callRange.lowerBound].suffix(1200)
        #expect(lead.contains("terminalTraex"))
        #expect(lead.contains("!first.isEmpty"))
        #expect(lead.contains("attachments.isEmpty"))
        let composed = start.range(of: "let first = FirstMessage.text(")
        #expect(composed.map { $0.upperBound <= callRange.lowerBound } == true)
        #expect(Self.argument("prompt", in: call) == "first")
        #expect(Self.argument("launcher", in: call) == "runLauncher",
                "a Traex chosen by Model Configuration must not fall back to the hub's empty-launcher Claude default")

        let async = Self.argument("async", in: call)
        #expect(async == nil || async == "nil" || async == "false", "Traex waits for the started session")
        for field in ["permissionMode", "effort", "ultracode"] {
            let value = Self.argument(field, in: call)
            #expect(value == nil || value == "nil", "\(field) must be omitted")
        }

        let rest = start[callRange.upperBound...]
        let returned = rest.range(of: "return")
        let completion = returned.map { String(rest[..<$0.upperBound]) } ?? String(rest)
        #expect(completion.contains("onStarted(s)"))
        #expect(!completion.contains("sendNow"), "the prompt is already in the start; do not send it again")
        #expect(returned != nil, "the Traex branch returns before the ordinary async start and follow-up")
    }

    /// Claude, Paseo, unknown launchers, retries and attachment delivery retain the existing ordinary path.
    @Test func ordinaryStartsKeepTheirAsyncFollowUp() throws {
        let view = try Self.source("Persona/Views/NewChat/NewChatView.swift")
        let start = Self.block("private func start() async", in: view)
        #expect(start?.contains("permissionMode: StartModes.sent(startMode, for: runEntry)") == true)
        #expect(start?.contains("async: true") == true)
        #expect(start?.contains("model.sendNow(s.key, text: first, images: photos)") == true)
    }
}
