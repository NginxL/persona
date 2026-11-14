import Foundation
import Observation

/// Whether all of a changed file is staged (its tick box is ticked), part of it, or none: as the web's `stageState`
/// (`web/src/lib/git-write.ts`). A new file Git doesn't track yet has nothing staged.
nonisolated enum GitStageMark: Equatable, Sendable {
    case none, some, all

    init(_ f: GitFile) {
        let inIndex = f.index != "." && f.index != "?"
        let inTree = f.worktree != "." && f.worktree != "?" ? true : f.index == "?"
        self = inIndex && !inTree ? .all : inIndex ? .some : .none
    }

    /// The tick box of every change at once (the commit bar's): all staged, none, or a mix.
    init(all files: [GitFile]) {
        let marks = files.map { GitStageMark($0) }
        self = !marks.isEmpty && marks.allSatisfy { $0 == .all } ? .all : marks.allSatisfy { $0 == .none } ? .none : .some
    }
}

/// A refused write said for the user (#7013): what happened in plain words (the web's wording, by the node's code),
/// the files it is about, and git's own words.
nonisolated struct GitRefusal: Identifiable, Equatable, Sendable {
    var id = UUID()
    var title: String
    var detail: String
    var files: [String] = []

    init(title: String, detail: String, files: [String] = []) {
        self.title = title
        self.detail = detail
        self.files = files
    }

    init(_ error: Error) {
        guard let e = error as? APIError else {
            self.init(title: String(localized: "It didn't go through"), detail: error.shown ?? error.localizedDescription)
            return
        }
        let files = e.details?["files"]?.array?.compactMap(\.string) ?? []
        if let title = Self.title(e.code) {
            self.init(title: title, detail: e.message, files: files)
        } else if e.message.range(of: "unknown method git.") != nil {
            // A node from before the web's Git writes does not know the method: Persona there needs updating.
            self.init(title: String(localized: "Persona on that computer is too old for this: update it"), detail: e.message)
        } else {
            self.init(title: String(localized: "It didn't go through"), detail: e.localizedDescription, files: files)
        }
    }

    /// The refusals the stage, commit and push writes answer with (docs/protocol/protocol.md, `git/stage` …).
    static func title(_ code: String?) -> String? {
        switch code {
        case "git_not_repo": String(localized: "Not a Git Repository")
        case "git_detached": String(localized: "HEAD is detached: switch to a branch first")
        case "git_nothing_staged": String(localized: "Nothing is staged: tick the files to commit")
        case "git_hook_failed": String(localized: "The commit was refused (a hook, usually pre-commit)")
        case "git_identity": String(localized: "Git on that computer has no name and email to commit with")
        case "git_no_upstream": String(localized: "This branch has no upstream yet")
        case "git_no_remote": String(localized: "No remote named origin to push to")
        case "git_push_rejected": String(localized: "The remote has newer commits: pull first, then push")
        case "git_auth": String(localized: "The remote refused the credentials on that computer")
        case "git_conflict": String(localized: "Uncommitted changes would be overwritten: commit or stash these first")
        case "git_busy": String(localized: "Another Git command is running there (the Agent's?): try again in a moment")
        default: nil
        }
    }

    static func == (a: Self, b: Self) -> Bool { a.title == b.title && a.detail == b.detail && a.files == b.files }
}

/// What Push would do now (#7013): nothing (no button), push the commits to the upstream, make the upstream on origin
/// for a branch without one, or make it again after it was deleted on the remote. As the web's `PushButton`.
nonisolated enum GitPushKind: Equatable, Sendable {
    case commits(Int, upstream: String)
    case fresh
    case gone(upstream: String)

    init?(_ s: GitStatus) {
        guard s.branch != nil else { return nil }
        guard let up = s.upstreamName else { self = .fresh; return }
        if s.gone == true { self = .gone(upstream: up) } else if s.ahead > 0 { self = .commits(s.ahead, upstream: up) } else { return nil }
    }

    /// The push makes the upstream on origin (`set_upstream`).
    var setsUpstream: Bool { self == .fresh }
}

/// The Git tab's writes (#7013, hub feature `git_write`): stage or unstage a file or all of them, commit what is
/// staged, push. One at a time; each answers the repository as it is after it, and a refusal is kept until it is read.
@MainActor @Observable
final class GitWriteModel {
    struct Deps {
        var stage: (_ machine: String, _ cwd: String, _ paths: [String], _ unstage: Bool) async throws -> GitWriteResult
        var commit: (_ machine: String, _ cwd: String, _ message: String) async throws -> GitWriteResult
        var push: (_ machine: String, _ cwd: String, _ setUpstream: Bool) async throws -> GitWriteResult

        init(api: APIClient) {
            stage = { try await api.gitStage(machine: $0, cwd: $1, paths: $2, unstage: $3) }
            commit = { try await api.gitCommit(machine: $0, cwd: $1, message: $2) }
            push = { try await api.gitPush(machine: $0, cwd: $1, setUpstream: $2) }
        }

        init(stage: @escaping (String, String, [String], Bool) async throws -> GitWriteResult,
             commit: @escaping (String, String, String) async throws -> GitWriteResult,
             push: @escaping (String, String, Bool) async throws -> GitWriteResult) {
            self.stage = stage
            self.commit = commit
            self.push = push
        }
    }

    /// What is running: a file's tick (its path), every change's, the commit or the push.
    enum Work: Hashable {
        case stage(String), stageAll, commit, push
    }

    private(set) var busy: Work?
    /// The last refusal, until the user reads it.
    var refusal: GitRefusal?

    /// Ticks or unticks one file: unticks one that is all staged, stages the rest of one that is partly.
    func toggle(_ f: GitFile, machine: String, cwd: String, deps: Deps) async -> GitWriteResult? {
        await run(.stage(f.path)) { try await deps.stage(machine, cwd, [f.path], GitStageMark(f) == .all) }
    }

    /// Stages every change, or unstages them all when every one is staged already.
    func toggleAll(_ s: GitStatus, machine: String, cwd: String, deps: Deps) async -> GitWriteResult? {
        guard !s.files.isEmpty else { return nil }
        return await run(.stageAll) { try await deps.stage(machine, cwd, s.files.map(\.path), GitStageMark(all: s.files) == .all) }
    }

    /// Commits what is staged with message (never `-a`; hooks run).
    func commit(message: String, machine: String, cwd: String, deps: Deps) async -> GitWriteResult? {
        let text = message.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty else { return nil }
        return await run(.commit) { try await deps.commit(machine, cwd, message) }
    }

    /// Pushes once the user said yes; never forced.
    func push(_ kind: GitPushKind, machine: String, cwd: String, deps: Deps) async -> GitWriteResult? {
        await run(.push) { try await deps.push(machine, cwd, kind.setsUpstream) }
    }

    private func run(_ work: Work, _ call: () async throws -> GitWriteResult) async -> GitWriteResult? {
        guard busy == nil else { return nil }
        busy = work
        defer { busy = nil }
        do {
            return try await call()
        } catch {
            // A cancelled write (the page went away) says nothing; the status is read again when it comes back.
            if !error.isCancellation { refusal = GitRefusal(error) }
            return nil
        }
    }
}
