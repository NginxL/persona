#if os(iOS)
import HealthKit
import Observation

/// Foreground-only authorization belongs to the page; the Shared reader never asks for access.
@Observable final class HealthActivityModel {
    enum State: Equatable {
        case loading, needsAccess, unavailable, noData, failed
        case loaded(HealthActivityReading)
    }

    struct Deps {
        var permission: () async throws -> HealthActivityReader.Permission
        var read: () async throws -> HealthActivityReading?
        var request: () async throws -> Void

        static var live: Deps {
            Deps(permission: { try await HealthActivityReader.permission() },
                 read: { try await HealthActivityReader.today() },
                 request: { try await HKHealthStore().requestAuthorization(toShare: [], read: [HKObjectType.activitySummaryType()]) })
        }
    }

    private(set) var state: State = .loading
    private(set) var requesting = false
    @ObservationIgnored private let deps: Deps

    init(deps: Deps = .live) { self.deps = deps }

    func load() async {
        do {
            let permission = try await deps.permission()
            guard !Task.isCancelled else { return }
            switch permission {
            case .shouldRequest: state = .needsAccess
            case .unavailable: state = .unavailable
            case .unknown: state = .failed
            case .unnecessary:
                let reading = try await deps.read()
                guard !Task.isCancelled else { return }
                state = reading.map(State.loaded) ?? .noData
            }
        } catch {
            if !Task.isCancelled { state = .failed }
        }
    }

    /// Only the visible Allow button calls this; repeated taps cannot open multiple authorization requests.
    func requestAccess() async {
        guard state == .needsAccess, !requesting else { return }
        requesting = true
        defer { requesting = false }
        do {
            try await deps.request()
            await load()
            HomeWidgetData.shared.refresh(force: true)
        } catch {
            if !Task.isCancelled { state = .failed }
        }
    }
}
#endif
