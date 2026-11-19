import Foundation
import Testing
@testable import Persona

/// Asking before a pairing link pairs (#9929): which hub the question names, and whether it is paired already.
struct PairConfirmationTests {
    private func config(_ s: String) -> ServerConfig { ServerConfig(url: URL(string: s)!, token: "t") }

    @Test func namesTheHubAsTheLinkGivesIt() {
        #expect(PairConfirmation.address(URL(string: "http://hub.example.com:18660")!) == "hub.example.com:18660")
        #expect(PairConfirmation.address(URL(string: "https://hub.example.com")!) == "hub.example.com")
        #expect(PairConfirmation.address(URL(string: "https://hub.example.com:443/persona")!) == "hub.example.com")
        #expect(PairConfirmation.address(URL(string: "http://hub.example.com:80")!) == "hub.example.com")
        #expect(PairConfirmation.address(URL(string: "https://hub.example.com:80")!) == "hub.example.com:80")
        #expect(PairConfirmation.address(URL(string: "http://[2001:db8::1]:18660")!) == "[2001:db8::1]:18660")
    }

    @Test func saysWhenTheHubIsPairedAlready() {
        let hub = config("https://hub.example.com")
        let fresh = PairConfirmation(hub, paired: [config("https://other.example.com")])
        let again = PairConfirmation(hub, paired: [config("https://other.example.com"), hub])
        #expect(!fresh.alreadyPaired)
        #expect(again.alreadyPaired)
        #expect(fresh.message != again.message)
        #expect(fresh.title.contains("hub.example.com"))
        #expect(fresh.config == hub) // Connect pairs with exactly what the link gave
    }
}
