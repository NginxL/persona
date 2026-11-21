import Foundation
import os
import UIKit

extension AppModel {
    /// Registry reads that failed before first unlock must not clear an existing protected system snapshot.
    func syncExtensionSurfaces() {
        guard !registryUnreadable, !isDemo else { return }
        let configs = Dictionary(uniqueKeysWithValues: registry.groups.compactMap { group in registry.primary(group).map { (group, $0.config) } })
        let names = Dictionary(uniqueKeysWithValues: registry.groups.map { ($0, groupName($0)) })
        let devices = Dictionary(uniqueKeysWithValues: registry.groups.compactMap { group in registry.primary(group)?.deviceID.map { (group, $0) } })
        try? ExtensionSurfaceSync.credentials(configs, names: names, devices: devices, capabilities: extensionSurfaceCapabilities)
    }

    func refreshExtensionSurfaces() {
        guard !registryUnreadable, !isDemo else { return }
        syncExtensionSurfaces()
        ExtensionSurfaceForegroundRefresh.shared.start()
    }
}

/// Foreground signals can overlap registry changes. Coalesce read-only refreshes; the store fences late replies.
/// Going to the background cancels the refresh (#11360): coming straight back starts a new one instead of waiting on
/// the cancelled one, which stops before its next store write.
@MainActor final class ExtensionSurfaceForegroundRefresh {
    static let shared = ExtensionSurfaceForegroundRefresh { await ExtensionSurfaceSync.refresh() }

    private let work: @Sendable () async -> Void
    private(set) var task: Task<Void, Never>?
    private var generation = 0

    init(_ work: @escaping @Sendable () async -> Void) { self.work = work }

    func start() {
        guard task == nil else { return }
        generation &+= 1
        let mine = generation
        task = Task { [work] in
            await work()
            if self.generation == mine { self.task = nil }
        }
    }

    func cancel() {
        task?.cancel()
        task = nil
    }
}

/// The app's background time around `ExtensionSurfaceStore`'s lock in the App Group (`ExtensionSurfaceLockLease`,
/// #11360). `beginBackgroundTask` is main-actor API, so a transaction off the main thread asks the main thread for it
/// before it locks; it is given back once the lock is released. No grant (`.invalid`: the app is on its way to being
/// suspended) means no lock.
nonisolated enum ExtensionSurfaceAppLease {
    static func install() {
        ExtensionSurfaceLockLease.provider = { begin() }
    }

    static func begin() -> ExtensionSurfaceLockLease.End? {
        if Thread.isMainThread { return MainActor.assumeIsolated { start() } }
        // The caller holds no lock yet, so waiting for the main thread here cannot deadlock against it.
        return DispatchQueue.main.sync { MainActor.assumeIsolated { start() } }
    }

    /// Ended exactly once: by the transaction, or by the expiration handler, which must end it before returning.
    @MainActor private static func start() -> ExtensionSurfaceLockLease.End? {
        let held = OSAllocatedUnfairLock(initialState: UIBackgroundTaskIdentifier.invalid)
        let take: @Sendable () -> UIBackgroundTaskIdentifier = {
            held.withLock { id in
                defer { id = .invalid }
                return id
            }
        }
        let id = UIApplication.shared.beginBackgroundTask(withName: "extension-surfaces") { @Sendable in
            MainActor.assumeIsolated {
                let id = take()
                if id != .invalid { UIApplication.shared.endBackgroundTask(id) }
            }
        }
        guard id != .invalid else { return nil }
        held.withLock { $0 = id }
        return {
            let id = take()
            guard id != .invalid else { return }
            if Thread.isMainThread {
                MainActor.assumeIsolated { UIApplication.shared.endBackgroundTask(id) }
            } else {
                DispatchQueue.main.async { UIApplication.shared.endBackgroundTask(id) }
            }
        }
    }
}
