import Foundation
import Testing
@testable import Persona

/// The Git tab's writes (#7013): the requests the web's Git panel sends, what a tick box shows, when Push shows and
/// what it asks for, one write at a time, and a refusal said in plain words with the node's own.
@MainActor
struct GitWriteTests {
    private static func status(files: String = "[]", branch: String? = "main", upstream: String? = "origin/main", ahead: Int = 0, gone: Bool? = nil) -> String {
        let b = branch.map { #""branch":"\#($0)","# } ?? ""
        let u = upstream.map { #""upstream":"\#($0)","# } ?? ""
        let g = gone.map { #""gone":\#($0),"# } ?? ""
        return #"{"is_repo":true,"root":"/work/sample",\#(b)\#(u)\#(g)"ahead":\#(ahead),"behind":0,"files":\#(files),"stashes":0,"commits":[]}"#
    }

    private static func decode(_ json: String) throws -> GitStatus { try JSON.decoder.decode(GitStatus.self, from: Data(json.utf8)) }

    private static func file(_ path: String, _ index: String, _ worktree: String) -> GitFile {
        GitFile(path: path, index: index, worktree: worktree, added: 1, deleted: 0)
    }

    // MARK: What a tick box shows (the web's stageState)

    @Test func stageMarkFollowsTheIndexAndTheTree() {
        #expect(GitStageMark(Self.file("a", "M", ".")) == .all)
        #expect(GitStageMark(Self.file("a", "A", ".")) == .all)
        #expect(GitStageMark(Self.file("a", "M", "M")) == .some)
        #expect(GitStageMark(Self.file("a", ".", "M")) == .none)
        #expect(GitStageMark(Self.file("a", "?", "?")) == .none) // new, not tracked yet
        #expect(GitStageMark(Self.file("a", "U", "U")) == .some) // unmerged
    }

    @Test func everyChangeMarkIsAllNoneOrMixed() {
        let staged = Self.file("a", "M", "."), partly = Self.file("b", "M", "M"), not = Self.file("c", ".", "M")
        #expect(GitStageMark(all: [staged, staged]) == .all)
        #expect(GitStageMark(all: [not, Self.file("d", "?", "?")]) == .none)
        #expect(GitStageMark(all: [staged, not]) == .some)
        #expect(GitStageMark(all: [partly]) == .some)
        #expect(GitStageMark(all: []) == .none)
    }

    // MARK: When Push shows (the web's PushButton)

    @Test func pushShowsForCommitsAMissingOrDeletedUpstreamOnly() throws {
        let synced = try Self.decode(Self.status(ahead: 0)), ahead = try Self.decode(Self.status(ahead: 2))
        let none = try Self.decode(Self.status(upstream: nil)), empty = try Self.decode(Self.status(upstream: ""))
        let gone = try Self.decode(Self.status(gone: true)), detached = try Self.decode(Self.status(branch: nil, ahead: 3))
        #expect(GitPushKind(synced) == nil)
        #expect(GitPushKind(ahead) == .commits(2, upstream: "origin/main"))
        #expect(GitPushKind(none) == .fresh)
        #expect(GitPushKind(empty) == .fresh) // the mock hub used to send it empty
        #expect(GitPushKind(gone) == .gone(upstream: "origin/main"))
        #expect(GitPushKind(detached) == nil)
        #expect(GitPushKind.fresh.setsUpstream && !GitPushKind.commits(1, upstream: "origin/main").setsUpstream)
    }

    // MARK: Requests and answers

    @Test func stagesAFileAndUnstagesOneAllStaged() async throws {
        let hub = FakeHub()
        var bodies: [[String: Any]] = []
        hub.on("POST", "/api/v1/machines/mac/git/stage") { req in
            bodies.append(try JSONSerialization.jsonObject(with: req.body!) as! [String: Any])
            return Data(#"{"status":\#(Self.status(files: #"[{"path":"a.swift","index":"M","worktree":".","added":1,"deleted":0}]"#))}"#.utf8)
        }
        let model = GitWriteModel(), deps = GitWriteModel.Deps(api: hub.client)
        let r = await model.toggle(Self.file("a.swift", ".", "M"), machine: "mac", cwd: "/work/sample", deps: deps)
        #expect(r?.status.files.first.map { GitStageMark($0) } == .all)
        _ = await model.toggle(Self.file("a.swift", "M", "."), machine: "mac", cwd: "/work/sample", deps: deps)
        _ = await model.toggle(Self.file("b.swift", "M", "M"), machine: "mac", cwd: "/work/sample", deps: deps)
        #expect(bodies.count == 3)
        #expect(bodies[0]["cwd"] as? String == "/work/sample" && bodies[0]["paths"] as? [String] == ["a.swift"] && bodies[0]["unstage"] as? Bool == false)
        #expect(bodies[1]["unstage"] as? Bool == true)
        #expect(bodies[2]["unstage"] as? Bool == false) // a partly staged file is staged whole
        #expect(model.busy == nil && model.refusal == nil)
    }

    @Test func stageAllSendsEveryPathAndUnstagesWhenAllAreStaged() async throws {
        let hub = FakeHub()
        var bodies: [[String: Any]] = []
        hub.on("POST", "/api/v1/machines/mac/git/stage") { req in
            bodies.append(try JSONSerialization.jsonObject(with: req.body!) as! [String: Any])
            return Data(#"{"status":\#(Self.status())}"#.utf8)
        }
        let model = GitWriteModel(), deps = GitWriteModel.Deps(api: hub.client)
        let mixed = try Self.decode(Self.status(files: #"[{"path":"a","index":"M","worktree":".","added":1,"deleted":0},{"path":"b","index":"?","worktree":"?","added":0,"deleted":0}]"#))
        _ = await model.toggleAll(mixed, machine: "mac", cwd: "/w", deps: deps)
        let all = try Self.decode(Self.status(files: #"[{"path":"a","index":"M","worktree":".","added":1,"deleted":0}]"#))
        _ = await model.toggleAll(all, machine: "mac", cwd: "/w", deps: deps)
        #expect(bodies.map { $0["paths"] as? [String] } == [["a", "b"], ["a"]])
        #expect(bodies.map { $0["unstage"] as? Bool } == [false, true])
    }

    @Test func commitsTheMessageAndAnswersTheCommit() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/machines/mac/git/commit") { req in
            let body = try JSONSerialization.jsonObject(with: req.body!) as! [String: String]
            #expect(body == ["cwd": "/w", "message": "Show the hourly forecast\n\nWith a test."])
            return Data(#"{"status":\#(Self.status(ahead: 1)),"commit":{"hash":"e1e1e1e1e1","short":"e1e1e1e","subject":"Show the hourly forecast","author":"Me"}}"#.utf8)
        }
        let model = GitWriteModel(), deps = GitWriteModel.Deps(api: hub.client)
        let r = await model.commit(message: "Show the hourly forecast\n\nWith a test.", machine: "mac", cwd: "/w", deps: deps)
        #expect(r?.commit?.short == "e1e1e1e" && r?.status.ahead == 1)
        // an empty message sends nothing
        let empty = await model.commit(message: "  \n", machine: "mac", cwd: "/w", deps: deps)
        #expect(empty == nil)
        #expect(hub.sent("POST", "/api/v1/machines/mac/git/commit").count == 1)
    }

    @Test func pushSetsTheUpstreamOnlyForABranchWithoutOne() async throws {
        let hub = FakeHub()
        var bodies: [[String: Any]] = []
        hub.on("POST", "/api/v1/machines/mac/git/push") { req in
            bodies.append(try JSONSerialization.jsonObject(with: req.body!) as! [String: Any])
            return Data(#"{"status":\#(Self.status()),"output":"To example.invalid:weather.git"}"#.utf8)
        }
        let model = GitWriteModel(), deps = GitWriteModel.Deps(api: hub.client)
        _ = await model.push(.fresh, machine: "mac", cwd: "/w", deps: deps)
        _ = await model.push(.commits(2, upstream: "origin/main"), machine: "mac", cwd: "/w", deps: deps)
        #expect(bodies.map { $0["set_upstream"] as? Bool } == [true, false])
    }

    // MARK: Refusals

    @Test func aRefusalIsSaidInPlainWordsWithGitsOwnAndItsFiles() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/machines/mac/git/commit") { _ in
            throw APIError(status: 409, message: "git commit: swiftlint: 2 violations", fields: ["code": "git_hook_failed"],
                           details: .object(["code": .string("git_hook_failed"), "files": .array([.string("Provider.swift")])]))
        }
        let model = GitWriteModel()
        let r = await model.commit(message: "x", machine: "mac", cwd: "/w", deps: .init(api: hub.client))
        #expect(r == nil && model.busy == nil)
        let refusal = try #require(model.refusal)
        #expect(refusal.title == "提交被拒绝了（通常是 pre-commit 之类的 hook）")
        #expect(refusal.detail == "git commit: swiftlint: 2 violations")
        #expect(refusal.files == ["Provider.swift"])
    }

    @Test func anOldNodeAndAnUnknownCodeHaveTheirOwnWords() {
        let old = GitRefusal(APIError(status: 502, message: "node: unknown method git.stage"))
        #expect(old.title == "那台电脑上的 Persona 版本太旧，不支持这个操作，请更新")
        let other = GitRefusal(APIError(status: 409, message: "fatal: something else", fields: ["code": "git_failed"]))
        #expect(other.title == "没有完成" && other.detail == "fatal: something else")
        #expect(GitRefusal.title("git_push_rejected") == "远端有更新的提交：先拉取，再推送")
    }

    @Test func oneWriteAtATime() async throws {
        let model = GitWriteModel()
        var release: CheckedContinuation<GitWriteResult, Never>?
        var calls = 0
        let fresh = GitWriteResult(status: try Self.decode(Self.status()), commit: nil, output: nil)
        let deps = GitWriteModel.Deps(stage: { _, _, _, _ in
            calls += 1
            return await withCheckedContinuation { release = $0 }
        }, commit: { _, _, _ in fresh }, push: { _, _, _ in fresh })
        let task = Task { await model.toggle(Self.file("a", ".", "M"), machine: "mac", cwd: "/w", deps: deps) }
        for _ in 0..<100 where release == nil { await Task.yield() }
        let continuation = try #require(release)
        #expect(model.busy == .stage("a"))
        let commit = await model.commit(message: "x", machine: "mac", cwd: "/w", deps: deps)
        let second = await model.toggle(Self.file("b", ".", "M"), machine: "mac", cwd: "/w", deps: deps)
        #expect(commit == nil && second == nil)
        continuation.resume(returning: fresh)
        _ = await task.value
        #expect(model.busy == nil && calls == 1)
    }
}
