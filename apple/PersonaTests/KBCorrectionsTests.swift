import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Correcting an entry by hand (#6997): confirmed facts and removing a link, the same writes and the same rule for which
/// links can go as the web's (`web/src/lib/kb-relationships.ts`, cases from `Relationships.test.tsx`).
@MainActor
struct KBCorrectionsTests {
    private func entry(_ kind: String, _ id: String? = nil) -> KBEntry {
        KBEntry(kind: kind, id: id ?? (kind == "recordings" ? "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa" : kind), name: "Example \(kind)", relevance: 0)
    }

    private func detail(_ e: KBEntry, _ meta: [String: JSONValue], links: [KBEntry] = [], removed: [KBEntry]? = nil) -> KBDetail {
        KBDetail(kind: e.kind, id: e.id, path: "", meta: .object(meta), links: links, removed: removed)
    }

    private func list(_ s: [String]) -> JSONValue { .array(s.map { .string($0) }) }

    private func member(_ e: KBEntry, _ field: String) -> String { field == "items" ? "\(e.kind)/\(e.id)" : e.id }

    private func json(_ c: KBCorrection) throws -> String {
        let enc = JSONEncoder()
        enc.outputFormatting = .sortedKeys
        return String(decoding: try enc.encode(c), as: UTF8.self)
    }

    private struct Mapping {
        var a: KBEntry, b: KBEntry, af: String, bf: String, delta: String, reverse: Bool
    }

    private var mappings: [Mapping] {
        let project = entry("projects"), person = entry("people"), chat = entry("chats")
        return [
            Mapping(a: project, b: person, af: "people", bf: "projects", delta: "remove_people", reverse: false),
            Mapping(a: project, b: chat, af: "chats", bf: "projects", delta: "remove_chats", reverse: false),
            Mapping(a: chat, b: person, af: "people", bf: "chats", delta: "remove_people", reverse: false),
            Mapping(a: person, b: chat, af: "chats", bf: "people", delta: "remove_chats", reverse: false),
        ] + ["people", "chats", "docs", "meetings", "events", "recordings"].map { kind in
            Mapping(a: entry(kind), b: project, af: "projects", bf: kind == "people" || kind == "chats" ? kind : "items",
                    delta: "remove_projects", reverse: false)
        } + ["docs", "meetings", "events", "recordings"].map { kind in
            Mapping(a: project, b: entry(kind), af: "items", bf: "projects", delta: "remove_projects", reverse: true)
        }
    }

    @Test func aLinkHeldInTheEntrysListIsRemovedWithTheWebsUpdate() throws {
        for m in mappings {
            let d = detail(m.a, [m.af: list([member(m.b, m.af), "unrelated"])], links: [m.b])
            let r = try #require(KBRelationship.removal(d, name: m.a.name, linked: m.b), "\(m.a.kind) → \(m.b.kind)")
            let target = m.reverse ? m.b : m.a
            let targetMember = m.reverse ? m.a.id : m.b.id
            #expect(r.target.kind == target.kind && r.target.id == target.id)
            for lock in [false, true] {
                #expect(try json(r.removalUpdate(lockLists: lock)) == "{\"lock_lists\":\(lock),\"\(m.delta)\":[\"\(targetMember)\"]}")
            }
            #expect(r.first.field.rawValue == m.af && r.second.field.rawValue == m.bf)
            #expect(r.first.member == member(m.b, m.af) && r.second.member == member(m.a, m.bf))
            #expect(r.question.contains(m.a.name) && r.question.contains(m.b.name))
        }
    }

    @Test func derivedMalformedOrUnsupportedLinksStayNavigationOnly() {
        let project = entry("projects"), person = entry("people"), chat = entry("chats")
        let cases: [(KBEntry, KBEntry, [String: JSONValue])] = [
            (project, person, [:]),
            (project, person, ["people": .string("people")]),
            (project, person, ["people": .array([.string(person.id), .number(7)])]),
            (project, person, ["people": list(["someone-else"])]),
            (project, entry("projects", "other-project"), ["projects": list(["other-project"])]),
            (entry("docs"), chat, ["where": .string(chat.id), "chats": list([chat.id])]),
            (entry("meetings"), person, ["people": list([person.id])]),
        ]
        for (a, b, meta) in cases {
            #expect(KBRelationship.removal(detail(a, meta, links: [b]), name: a.name, linked: b) == nil, "\(a.kind) → \(b.kind) \(meta)")
        }
    }

    @Test func requestsCarryTheSourceIDAndListsTheFolderName() throws {
        // The entry's own id is in meta.id; its folder (the detail's id) is the hub's SafeID of it.
        let a = KBDetail(kind: "people", id: "ou_a_b", path: "", meta: .object(["id": .string("ou:a b"), "projects": list(["p"])]))
        let r = try #require(KBRelationship.removal(a, name: "A", linked: KBEntry(kind: "projects", id: "p", name: "P", relevance: 0)))
        #expect(r.target.id == "ou:a b" && r.first.canonicalID == "ou_a_b" && r.second.member == "ou:a b")
        #expect(KBRelationship.canonicalID(kind: "docs", id: "._a b/c?._") == "a_b_c")
        #expect(KBRelationship.canonicalID(kind: "recordings", id: "AAAA-BB") == "aaaa-bb")
        #expect(KBRelationship.canonicalID(kind: "people", id: "张三@x.com") == "张三@x.com")
        // 120 bytes at most, never half a character: 41 three-byte characters keep 40.
        #expect(KBRelationship.canonicalID(kind: "chats", id: String(repeating: "群", count: 41)) == String(repeating: "群", count: 40))
    }

    @Test func removalRequiresExclusionOnBothEndsAndLocksOnlyWhenRequested() throws {
        let project = entry("projects"), person = entry("people")
        let r = try #require(KBRelationship.removal(detail(project, ["people": list([person.id])]), name: project.name, linked: person))
        let projectExcluded: JSONValue = .object(["people": list([person.id])])
        let personExcluded: JSONValue = .object(["projects": list([project.id])])
        let first = detail(project, ["people": list(["unrelated"]), "excluded": projectExcluded])
        let second = detail(person, ["excluded": personExcluded])
        let firstLocked = detail(project, ["excluded": projectExcluded, "locked_fields": list(["name", "people"])])
        let secondLocked = detail(person, ["excluded": personExcluded, "locked_fields": list(["projects"])])
        #expect(r.removalConfirmed(first, second, lockLists: false))
        #expect(r.removalConfirmed(firstLocked, secondLocked, lockLists: true))
        #expect(r.removalConfirmed(firstLocked, secondLocked, lockLists: false))
        #expect(!r.removalConfirmed(firstLocked, second, lockLists: true))
        // Old hubs lock both ends but do not exclude either one.
        #expect(!r.removalConfirmed(detail(project, ["locked_fields": list(["people"])]),
                                    detail(person, ["locked_fields": list(["projects"])]), lockLists: true))
        #expect(!r.removalConfirmed(firstLocked, detail(person, [:]), lockLists: false))
        #expect(!r.removalConfirmed(first, detail(person, ["excluded": personExcluded, "projects": list([project.id])]), lockLists: false))
        #expect(!r.removalConfirmed(first, detail(person, ["excluded": personExcluded, "projects": .bool(false)]), lockLists: false))
        #expect(!r.removalConfirmed(first, detail(person, ["excluded": .object(["projects": .array([.string(project.id), .number(7)])])]), lockLists: false))
        #expect(!r.removalConfirmed(first, detail(person, ["excluded": personExcluded, "provenance": .string("extension")]), lockLists: false))
        #expect(!r.removalConfirmed(first, detail(person, ["excluded": personExcluded, "provenance": .object(["projects": .object([:])])]), lockLists: false))
        #expect(!r.removalConfirmed(first, detail(entry("people", "someone-else"), ["excluded": personExcluded]), lockLists: false))
        #expect(!r.removalConfirmed(first, nil, lockLists: false))
    }

    @Test func allowancesWriteToTheSameEndAsRemoval() throws {
        for m in mappings {
            let d = detail(m.a, ["excluded": .object([m.af: list([member(m.b, m.af)])])], removed: [m.b])
            let r = try #require(KBRelationship.allowance(d, name: m.a.name, removed: m.b), "\(m.a.kind) → \(m.b.kind)")
            let target = m.reverse ? m.b : m.a
            let field = m.delta.replacingOccurrences(of: "remove_", with: "allow_")
            let targetMember = m.reverse ? m.a.id : m.b.id
            #expect(r.target.kind == target.kind && r.target.id == target.id)
            #expect(try json(r.allowanceUpdate) == "{\"\(field)\":[\"\(targetMember)\"]}")
            #expect(KBRelationship.removal(d, name: m.a.name, linked: m.b) == nil)
        }
    }

    @Test func aProjectsDocumentUsesItsOriginalIDForTheWrite() throws {
        let project = entry("projects", "example")
        let document = entry("docs", "source:doc / v2")
        let d = detail(project, ["excluded": .object(["items": list(["docs/source_doc_v2"])])], removed: [document])
        let r = try #require(KBRelationship.allowance(d, name: project.name, removed: document))
        #expect(r.target.kind == "docs" && r.target.id == "source:doc / v2")
        #expect(try json(r.allowanceUpdate) == #"{"allow_projects":["example"]}"#)
        #expect(r.second.canonicalID == "source_doc_v2")
    }

    @Test func allowanceNeedsBothTheRemovedRowAndExclusionToClear() throws {
        let project = entry("projects"), person = entry("people")
        let excluded: JSONValue = .object(["people": list([person.id])])
        let removed = detail(project, ["excluded": excluded], removed: [person, person])
        let rows = KBRemovedLink.rows(in: removed, name: project.name)
        #expect(rows.map(\.id) == ["people/people"])
        let r = try #require(rows.first?.relationship)
        #expect(KBRemovedLink.rows(in: detail(project, [:], removed: [person]), name: project.name).isEmpty)
        #expect(!r.allowanceConfirmed(removed))
        #expect(!r.allowanceConfirmed(detail(project, ["excluded": excluded])))
        #expect(!r.allowanceConfirmed(detail(project, [:], removed: [person])))
        #expect(!r.allowanceConfirmed(detail(project, ["excluded": .string("people")])))
        #expect(!r.allowanceConfirmed(detail(entry("projects", "other-project"), [:])))
        #expect(r.allowanceConfirmed(detail(project, ["locked_fields": list(["people"])])))
        #expect(r.allowanceConfirmed(detail(project, ["excluded": .object(["people": list(["unrelated"])])], removed: [entry("people", "unrelated")])))
    }

    @Test func olderDetailsDecodeWithoutRemovedLinks() throws {
        let detail = try JSONDecoder().decode(KBDetail.self, from: Data(#"{"kind":"people","id":"example","path":"","links":[]}"#.utf8))
        #expect(detail.links?.isEmpty == true)
        #expect(detail.removed == nil)
    }

    @Test func confirmedFactsAreWrittenExplicitlyAndClearedWithAnEmptyString() throws {
        #expect(try json(KBCorrection(pinned: "")) == #"{"pinned":""}"#)
        #expect(try json(KBCorrection(pinned: "Works on Tuesdays")) == #"{"pinned":"Works on Tuesdays"}"#)
        #expect(try json(KBCorrection()) == "{}")
        let r = KBDetailResponse(detail: detail(entry("people"), ["name": .string("A"), "pinned": .string("old")]))
        #expect(KBFacts.text(r.detail.meta) == "old")
        let cleared = KBFacts.with("", in: r)
        #expect(KBFacts.text(cleared.detail.meta) == "" && cleared.detail.meta?["name"] == .string("A"))
        #expect(KBFacts.text(.object(["pinned": .number(3)])) == "" && KBFacts.text(nil) == "")
    }
}
