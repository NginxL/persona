import Observation
import SwiftUI
#if os(iOS)
import UIKit
#endif

/// One flow for the iPhone chat notice and the Agents row menu.
@MainActor @Observable
final class CodexTakeoverCoordinator {
    enum Pending {
        case takeover(Session, APIClient.CodexTakeoverInfo)
        case continuation(Session, APIClient.CodexTakeoverInfo)
    }

    var pending: Pending?
    var error: String?
    private(set) var isBusy = false

    var needsTakeoverConfirmation: Bool {
        if case .takeover = pending { return true }
        return false
    }

    var needsContinuationConfirmation: Bool {
        if case .continuation = pending { return true }
        return false
    }

    static func continuationOnlyInfo(for error: Error, original: APIClient.CodexTakeoverInfo) -> APIClient.CodexTakeoverInfo? {
        guard (error as? APIError)?.code == "codex_takeover_continue_only" else { return nil }
        var info = original
        info.canResume = false
        info.reason = "encrypted_context"
        return info
    }

    func takeOver(_ session: Session, model: AppModel) async -> Session? {
        guard !isBusy else { return nil }
        guard session.isCodex, !session.canSend, session.capabilities?.takeover != nil else { return nil }
        guard let api = model.api(session.key.group) else { return fail(String(localized: "Can't reach this computer's hub. Try again later.")) }
        isBusy = true
        defer { isBusy = false }
        do {
            let info = try await api.codexTakeoverInfo(machine: session.machineID, session: session.sessionID)
            guard info.canResume else {
                pending = .continuation(session, info)
                return nil
            }
            guard info.running else { return try await executeTakeover(session, info: info, confirmStop: false, api: api) }
            pending = .takeover(session, info)
            return nil
        } catch { return fail(error.localizedDescription) }
    }

    func continueInNewSession(_ session: Session, model: AppModel) async -> Session? {
        guard !isBusy else { return nil }
        guard session.isCodex, !session.canSend, session.capabilities?.takeover != nil else { return nil }
        guard let api = model.api(session.key.group) else { return fail(String(localized: "Can't reach this computer's hub. Try again later.")) }
        isBusy = true
        defer { isBusy = false }
        do {
            let info = try await api.codexTakeoverInfo(machine: session.machineID, session: session.sessionID)
            if !info.canResume {
                pending = .continuation(session, info)
                return nil
            }
            return try await executeContinuation(session, info: info, api: api)
        } catch { return fail(error.localizedDescription) }
    }

    func confirm(model: AppModel) async -> Session? {
        guard !isBusy else { return nil }
        guard let pending else { return nil }
        self.pending = nil
        let session: Session
        let info: APIClient.CodexTakeoverInfo
        switch pending {
        case let .takeover(s, i), let .continuation(s, i): session = s; info = i
        }
        guard let api = model.api(session.key.group) else { return fail(String(localized: "Can't reach this computer's hub. Try again later.")) }
        isBusy = true
        defer { isBusy = false }
        do {
            switch pending {
            case .takeover: return try await executeTakeover(session, info: info, confirmStop: true, api: api)
            case .continuation: return try await executeContinuation(session, info: info, api: api)
            }
        } catch { return fail(error.localizedDescription) }
    }

    private func executeTakeover(_ session: Session, info: APIClient.CodexTakeoverInfo, confirmStop: Bool, api: APIClient) async throws -> Session? {
        do {
            return try await api.codexTakeover(machine: session.machineID, session: session.sessionID, token: info.token, confirmStop: confirmStop)
        } catch {
            guard let continuation = Self.continuationOnlyInfo(for: error, original: info) else { throw error }
            pending = .continuation(session, continuation)
            return nil
        }
    }

    private func executeContinuation(_ session: Session, info: APIClient.CodexTakeoverInfo, api: APIClient) async throws -> Session {
        try await api.start(.init(machineID: session.machineID, cwd: info.cwd, launcher: "codex",
                                  prompt: info.continuationPrompt, async: false, todoID: session.todo?.id))
    }

    private func fail(_ message: String) -> Session? { error = message; return nil }
}

struct CodexTakeoverControl: View {
    @Environment(AppModel.self) private var model
    var session: Session
    var coordinator: CodexTakeoverCoordinator
    var onSession: (Session) -> Void

    var body: some View {
        #if os(iOS)
        if UIDevice.current.userInterfaceIdiom == .phone, let capability = session.capabilities?.takeover {
            HStack(spacing: Theme.Space.s) {
                if capability.canResume {
                    Button(role: .destructive) { run { await coordinator.takeOver(session, model: model) } } label: {
                        Text("Take over").lineLimit(1)
                    }
                    .themedButton()
                }
                Button { run { await coordinator.continueInNewSession(session, model: model) } } label: {
                    Text("Continue in a new session").lineLimit(1)
                }
                .themedButton()
            }
        }
        #else
        EmptyView()
        #endif
    }

    private func run(_ operation: @escaping () async -> Session?) {
        Task { if let result = await operation() { onSession(result) } }
    }
}

struct CodexTakeoverDialogs: ViewModifier {
    @Environment(AppModel.self) private var model
    var coordinator: CodexTakeoverCoordinator
    var onSession: (Session) -> Void

    func body(content: Content) -> some View {
        #if os(iOS)
        content
            .accessibleConfirmationDialog("Take over this Codex session?", isPresented: Binding(
                get: { coordinator.needsTakeoverConfirmation }, set: { if !$0 { coordinator.pending = nil } }
            ), titleVisibility: .visible, hasCancel: true) {
                Button("Take over", role: .destructive) { confirm() }
                Button("Cancel", role: .cancel) { coordinator.pending = nil }
            } message: { Text("This will end the turn currently running in the terminal.") }
            .accessibleConfirmationDialog("Continue in a new session?", isPresented: Binding(
                get: { coordinator.needsContinuationConfirmation }, set: { if !$0 { coordinator.pending = nil } }
            ), titleVisibility: .visible, hasCancel: true) {
                Button("Continue in a new session") { confirm() }
                Button("Cancel", role: .cancel) { coordinator.pending = nil }
            } message: { Text(continuationMessage) }
            .alert("Session note", isPresented: Binding(
                get: { coordinator.error != nil }, set: { if !$0 { coordinator.error = nil } }
            )) {
                Button("OK", role: .cancel) { coordinator.error = nil }
            } message: { Text(coordinator.error ?? "") }
        #else
        content
        #endif
    }

    private func confirm() {
        Task { if let result = await coordinator.confirm(model: model) { onSession(result) } }
    }

    private var continuationMessage: String {
        guard case let .continuation(_, info) = coordinator.pending,
              info.reason == "encrypted_context" else {
            return String(localized: "This session cannot be safely resumed. Continue in a new session instead.")
        }
        return String(localized: "This conversation's context is sealed. Continue in a new session instead.")
    }
}

extension View {
    func codexTakeoverDialogs(coordinator: CodexTakeoverCoordinator, onSession: @escaping (Session) -> Void) -> some View {
        modifier(CodexTakeoverDialogs(coordinator: coordinator, onSession: onSession))
    }
}
