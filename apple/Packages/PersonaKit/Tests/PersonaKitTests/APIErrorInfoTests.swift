import Testing
@testable import PersonaKit

/// Model API errors in a chat (`APIErrorInfo`, `ChatView.foldAPIErrors`). The texts follow real ones with the ids,
/// hosts and regions replaced.
struct APIErrorInfoTests {
    @Test func sealedCompactionCannotBeRetried() throws {
        let sealed = "This conversation was compacted natively by <account>: the compaction is sealed (encrypted) to that account and can't go to it now"
        for text in [sealed, "API Error: 503 upstream: \(sealed)", sealed.uppercased()] {
            let error = try #require(APIErrorInfo.parse(text))
            #expect(error.transient == false)
            #expect(error.reason.contains("new conversation"))
        }
        #expect(APIErrorInfo.parse("API Error: 503 encrypted connection failed")?.transient == true)
        #expect(APIErrorInfo.parse("A reply explaining: \(sealed)") == nil)
    }

    private let gpu = "API Error: 502 upstream stream error: rpc error: code = 13 desc = Req 202610302115A1B2C3D4E5F6 Session 1ebd0000:7e470000 streamingcall loadbalance failed (target=default, roles=ROLE_DECODE,ROLE_PREFILL): no available GPU pod [biz error]. This is a server-side issue, usually temporary — try again in a moment."

    @Test func readsStatusReasonAndIds() throws {
        let e = try #require(APIErrorInfo.parse(gpu))
        #expect(e.status == 502)
        #expect(e.reason == "The inference service has no GPU available right now") // English: `swift test` has no app catalog
        #expect(e.transient == true)
        #expect(e.requestID == "202610302115A1B2C3D4E5F6")
        #expect(e.session == "1ebd0000:7e470000")
        #expect(e.raw == gpu)
    }

    @Test func loginErrorsNeedSomeone() throws {
        let e = try #require(APIErrorInfo.parse(#"API Error: 401 {"type":"error","error":{"type":"authentication_error","message":"OAuth token has expired."},"request_id":"req_011CUdExampleExample01"} · Please run /login"#))
        #expect(e.status == 401)
        #expect(e.transient == false)
        #expect(e.requestID == "req_011CUdExampleExample01")
    }

    @Test func hubNoticesAndOtherCauses() throws {
        #expect(APIErrorInfo.parse("API 错误：Overloaded（正在重试）")?.reason == "The model service is overloaded")
        #expect(APIErrorInfo.parse("API Error: 429 rate_limit_error")?.transient == true)
        #expect(APIErrorInfo.parse("API Error: Request timed out.")?.reason == "The request timed out")
        #expect(APIErrorInfo.parse("API Error: 400 messages: text content blocks must be non-empty")?.transient == false)
    }

    @Test func ordinaryTextIsNotAnError() {
        #expect(APIErrorInfo.parse("API 的错误处理已经改好了") == nil)
        #expect(APIErrorInfo.parse(nil) == nil)
    }
}
