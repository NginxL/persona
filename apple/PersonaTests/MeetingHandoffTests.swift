import Foundation
import Testing
@testable import Persona

/// Handing a meeting to an Agent after it (#8852, docs/assistant/meeting-minutes.md "Handing a meeting to an Agent
/// (#8852)"): the latest hand-off read from the detail leniently, the one made on the page against it, the problems in
/// words (the tests run in zh-Hans), and the call's body.
@MainActor
struct MeetingHandoffTests {
    private func answer(_ handoffs: String) throws -> RecordingMinutesModel.Answer {
        try JSONDecoder().decode(RecordingMinutesModel.Answer.self, from: Data((#"{"detail":{"meta":{"minutes_state":"ready","handoffs":"# + handoffs + "}}}").utf8))
    }

    private func handoff(_ id: String, at: String, state: String = "sent", problem: String? = nil) -> MeetingHandoff {
        MeetingHandoff(id: id, at: at, machineID: "mac", sessionID: "s1", title: "Website redesign", transcript: nil, state: state, problem: problem)
    }

    @Test func readsTheLatestHandoffLeniently() throws {
        let read = try answer(#"""
        [{"id":"h1","at":"2026-11-15T10:00:00Z","machine_id":"mac","session_id":"s1","title":"Website redesign","state":"sent"},
         {"id":"h2","at":"2026-11-15T11:00:00Z","machine_id":"mac","session_id":"s2","transcript":true,"state":"failed","problem":"unavailable"},
         {"id":"h3","state":"sent"},
         {"id":"h4","at":"2026-11-15T12:00:00Z","machine_id":"mac","session_id":"s3","state":"queued"}]
        """#)
        let last = try #require(read.detail.meta?.handoff)
        #expect(last.id == "h2" && last.failed && last.transcript == true)
        #expect(last.problemText == "它的电脑连不上，或者对话已关闭。")
        #expect(last.name == "Agent")
        // Hand-offs it can't read leave the minutes as they were.
        let odd = try answer(#""not a list""#)
        #expect(odd.detail.meta?.handoff == nil)
        #expect(odd.detail.meta?.state == "ready")
    }

    @Test func theNewerOfMadeAndKept() {
        let kept = handoff("h1", at: "2026-11-15T10:00:00Z")
        let made = handoff("h2", at: "2026-11-15T11:00:00Z", state: "failed", problem: "send_failed")
        #expect(MeetingHandoff.newer(nil, nil) == nil)
        #expect(MeetingHandoff.newer(made, nil) == made)
        #expect(MeetingHandoff.newer(nil, kept) == kept)
        #expect(MeetingHandoff.newer(made, kept) == made)
        // read again later, the detail's newer one wins over what was made here
        #expect(MeetingHandoff.newer(kept, made) == made)
    }

    @Test func problemsInWords() {
        #expect(MeetingHandoff.problemText("not_agent") == "这是 Persona 的对话，不是 Agent。")
        #expect(MeetingHandoff.problemText("not_allowed") == "这个 Agent 不能接收会议内容。")
        #expect(MeetingHandoff.problemText("send_failed") == "消息没发出去。")
        #expect(MeetingHandoff.problemText("new_one") == "消息没发出去。")
    }

    @Test func theCallsBody() throws {
        let body = MeetingHandoff.Body(machineID: "mac", sessionID: "s1", title: "Website redesign", text: "Fix the export", transcript: true)
        let json = try #require(try JSONSerialization.jsonObject(with: JSONEncoder().encode(body)) as? [String: Any])
        #expect(json["machine_id"] as? String == "mac")
        #expect(json["session_id"] as? String == "s1")
        #expect(json["title"] as? String == "Website redesign")
        #expect(json["text"] as? String == "Fix the export")
        #expect(json["transcript"] as? Bool == true)
    }

    @Test func theHubsRefusalsInWords() {
        #expect(MeetingHandoff.callError(APIError(status: 409, message: "x", fields: ["code": "meeting_running"]))
                == "会还在开，结束后再交。")
        #expect(MeetingHandoff.callError(APIError(status: 409, message: "x", fields: ["code": "no_material"]))
                == "这场会还没有可交的内容：没有纪要，也没有逐字稿。")
    }
}
