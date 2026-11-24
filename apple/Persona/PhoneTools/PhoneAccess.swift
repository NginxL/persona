import Contacts
import CoreLocation
import EventKit
import Foundation
import HealthKit
import Photos
import UserNotifications

// MARK: - Contacts

enum ContactsAccess {
    static func status() -> PhoneAuthStatus {
        switch CNContactStore.authorizationStatus(for: .contacts) {
        case .authorized: .granted
        case .limited: .limited
        case .denied, .restricted: .denied
        case .notDetermined: .notDetermined
        @unknown default: .notDetermined
        }
    }

    static func request() async -> PhoneAuthStatus {
        let store = CNContactStore()
        do {
            _ = try await store.requestAccess(for: .contacts)
        } catch {
            // The prompt could not be shown; status() reports the real answer.
        }
        return status()
    }

    static func requireAccess() throws {
        switch status() {
        case .granted, .limited: return
        case .denied: throw ToolFailure(.denied, "通讯录 / Contacts") // i18n-ignore
        case .notDetermined:
            throw ToolFailure(.denied, "通讯录尚未授权 / Contacts access has not been requested yet") // i18n-ignore
        case .notNeeded: return
        }
    }
}

// MARK: - Calendar and reminders

enum CalendarAccess {
    static func status() -> PhoneAuthStatus {
        classify(EKEventStore.authorizationStatus(for: .event))
    }

    static func reminderStatus() -> PhoneAuthStatus {
        classify(EKEventStore.authorizationStatus(for: .reminder))
    }

    /// Both, asked off the main thread: each is a synchronous call to EventKit's daemon, and asking them as the app
    /// connected held the main thread 50–150 ms on a Mac (the first call opens the connection, #1337).
    static func statusesOffMain() async -> (events: PhoneAuthStatus, reminders: PhoneAuthStatus) {
        let (events, reminders) = await Task.detached {
            (EKEventStore.authorizationStatus(for: .event), EKEventStore.authorizationStatus(for: .reminder))
        }.value
        return (classify(events), classify(reminders))
    }

    static func requestEvents() async -> PhoneAuthStatus {
        let store = EKEventStore()
        do {
            _ = try await store.requestFullAccessToEvents()
        } catch {}
        return status()
    }

    static func requestReminders() async -> PhoneAuthStatus {
        let store = EKEventStore()
        do {
            _ = try await store.requestFullAccessToReminders()
        } catch {}
        return reminderStatus()
    }

    static func requireEvents() throws {
        switch status() {
        case .granted, .limited: return
        case .denied: throw ToolFailure(.denied, "日历 / Calendar") // i18n-ignore
        case .notDetermined: throw ToolFailure(.denied, "日历尚未授权 / Calendar access has not been requested yet") // i18n-ignore
        case .notNeeded: return
        }
    }

    static func requireReminders() throws {
        switch reminderStatus() {
        case .granted, .limited: return
        case .denied: throw ToolFailure(.denied, "提醒事项 / Reminders") // i18n-ignore
        case .notDetermined: throw ToolFailure(.denied, "提醒事项尚未授权 / Reminders access has not been requested yet") // i18n-ignore
        case .notNeeded: return
        }
    }

    private static func classify(_ status: EKAuthorizationStatus) -> PhoneAuthStatus {
        switch status {
        case .fullAccess: .granted
        case .writeOnly: .limited
        case .denied, .restricted: .denied
        case .notDetermined: .notDetermined
        @unknown default: .notDetermined
        }
    }
}

// MARK: - Location

enum LocationAccess {
    static func status() -> PhoneAuthStatus {
        switch CLLocationManager().authorizationStatus {
        case .authorizedWhenInUse, .authorizedAlways: .granted
        case .denied, .restricted: .denied
        case .notDetermined: .notDetermined
        @unknown default: .notDetermined
        }
    }

    /// While Using or Always (`status` calls both granted): whether a read can work while Persona isn't open (#13200).
    static func level() -> LocationLevel {
        LocationLevel(CLLocationManager().authorizationStatus)
    }

    /// Shows the system prompt and waits for the user's answer (or for an
    /// existing answer to be reported).
    static func request() async -> PhoneAuthStatus {
        if status() != .notDetermined { return status() }
        let waiter = LocationAuthorizationWaiter()
        await waiter.wait()
        return status()
    }
}

/// Bridges `CLLocationManagerDelegate.locationManagerDidChangeAuthorization`
/// to async/await. CoreLocation creates its manager on the main thread, so the
/// waiter has to stay there too.
final class LocationAuthorizationWaiter: NSObject, CLLocationManagerDelegate {
    private let manager = CLLocationManager()
    private var resume: (() -> Void)?

    func wait() async {
        await withCheckedContinuation { (continuation: CheckedContinuation<Void, Never>) in
            resume = { continuation.resume() }
            manager.delegate = self
            if manager.authorizationStatus == .notDetermined {
                manager.requestWhenInUseAuthorization()
            } else {
                finish()
            }
        }
    }

    func locationManagerDidChangeAuthorization(_ manager: CLLocationManager) {
        guard manager.authorizationStatus != .notDetermined else { return }
        finish()
    }

    private func finish() {
        let pending = resume
        resume = nil
        pending?()
    }
}

// MARK: - Health

/// HealthKit deliberately never tells an app whether *read* access was granted,
/// so the best we can do is remember that the user was asked. A denied read
/// permission shows up later as empty results, not as an error.
enum HealthAccess {
    private static let askedKey = "phoneAuth.health.asked"
    private static let store = HKHealthStore()

    static func cachedStatus() -> PhoneAuthStatus {
        guard HKHealthStore.isHealthDataAvailable() else { return .notNeeded }
        return UserDefaults.standard.bool(forKey: askedKey) ? .granted : .notDetermined
    }

    static func request() async -> PhoneAuthStatus {
        guard HKHealthStore.isHealthDataAvailable() else { return .notNeeded }
        let read = HealthTool.readTypes
        do {
            // The AI write tool only: intake sync requests its own two write types when explicitly enabled.
            _ = try await store.requestAuthorization(toShare: HealthTool.writeTypes, read: read)
            UserDefaults.standard.set(true, forKey: askedKey)
        } catch {
            // Leave the flag alone so the settings screen can offer to try again.
        }
        return cachedStatus()
    }

    static func requireAccess() throws {
        guard HKHealthStore.isHealthDataAvailable() else {
            throw ToolFailure(.unavailable, "此设备不支持健康数据 / Health data is not available on this device") // i18n-ignore
        }
        guard cachedStatus() != .notDetermined else {
            throw ToolFailure(.denied, "健康数据尚未授权 / Health access has not been requested yet") // i18n-ignore
        }
    }
}

// MARK: - Photos

enum PhotosAccess {
    static func status() -> PhoneAuthStatus {
        switch PHPhotoLibrary.authorizationStatus(for: .readWrite) {
        case .authorized: .granted
        case .limited: .limited
        case .denied, .restricted: .denied
        case .notDetermined: .notDetermined
        @unknown default: .notDetermined
        }
    }

    static func request() async -> PhoneAuthStatus {
        _ = await PHPhotoLibrary.requestAuthorization(for: .readWrite)
        return status()
    }

    static func requireAccess() throws {
        switch status() {
        case .granted, .limited: return
        case .denied: throw ToolFailure(.denied, "照片 / Photos") // i18n-ignore
        case .notDetermined: throw ToolFailure(.denied, "照片尚未授权 / Photos access has not been requested yet") // i18n-ignore
        case .notNeeded: return
        }
    }
}

// MARK: - Notifications

enum NotificationsAccess {
    /// Last known answer, refreshed at launch, after prompting and when the app
    /// comes back to the foreground. Notifications are also useful while the
    /// answer is "not determined": the first post asks for permission.
    static private(set) var cached: PhoneAuthStatus = .notDetermined

    static func cachedStatus() -> PhoneAuthStatus { cached }

    static func refresh() async {
        let settings = await UNUserNotificationCenter.current().notificationSettings()
        switch settings.authorizationStatus {
        case .authorized, .provisional, .ephemeral: cached = .granted
        case .denied: cached = .denied
        case .notDetermined: cached = .notDetermined
        @unknown default: cached = .notDetermined
        }
    }

    static func request() async -> PhoneAuthStatus {
        _ = try? await UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound, .badge])
        await refresh()
        return cached
    }
}
