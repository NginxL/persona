import Foundation

extension AppModel {
    /// Answers the session's shell startup question, then resends only what that start definitely refused.
    /// The visible session starts again before the outbox task begins; a refusal changes no local start state.
    func continueStart(_ key: SessionKey, choice: String, features featureSet: Set<String>? = nil,
                       api injected: APIClient? = nil) async throws {
        let allowed = featureSet ?? features(key.group)
        guard allowed.contains(TaskStartRemedy.continueFeature) else {
            throw APIError(status: 0, message: String(localized: "This hub can't answer this startup question yet."))
        }
        guard let api = injected ?? api(key.group) else {
            throw APIError(status: 0, message: String(localized: "Can't reach the hub"))
        }
        let returned = try await api.continueStart(key, choice: choice)
        if let returned {
            adopt(returned)
            patchListedSession(returned.key) { $0 = returned }
        }
        patchListedSession(key) {
            $0.starting = true
            $0.status = .idle
            $0.startError = nil
            $0.startErrorCode = nil
            $0.startErrorArgs = nil
        }
        Haptics.success()
        Task { await resendShellWaiting(key) }
    }

    /// Resends the failed shell_waiting entries in their original pending order. A New Agent's first message is already
    /// that order's first pending entry on Apple, so no parallel launch store is needed. Fresh op IDs avoid a cached refusal.
    /// The first renewed failure or wait stops the pass so later context cannot overtake it.
    func resendShellWaiting(_ key: SessionKey) async {
        guard resendingShellStarts.insert(key).inserted else { return }
        defer { resendingShellStarts.remove(key) }
        let eligible = chat(key).pending.filter {
            $0.state == .failed && $0.failureCode == TaskStartRemedy.shellWaiting && $0.mayHaveArrived != true
        }
        for candidate in eligible {
            guard let index = chat(key).pending.firstIndex(where: {
                $0.id == candidate.id && $0.state == .failed && $0.failureCode == TaskStartRemedy.shellWaiting && $0.mayHaveArrived != true
            }) else { continue }
            chat(key).pending[index].state = .sending
            chat(key).pending[index].failure = nil
            chat(key).pending[index].failureCode = nil
            chat(key).pending[index].stalledSince = nil
            if chat(key).pending[index].refused == true {
                chat(key).pending[index].id = UUID()
                chat(key).pending[index].refused = nil
            }
            saveOutbox()
            let message = chat(key).pending[index]
            chat(key).beginSendFeedback(message)
            let outcome = await sendPending(key, message)
            settleSend(key, message.id, outcome)
            guard outcome == .sent || outcome == .kept else { return }
        }
    }
}
