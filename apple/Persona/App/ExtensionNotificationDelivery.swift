import UIKit

/// Keeps the request alive when the user taps a background button; when the system's time runs out, cancels it and calls the delegate's completion exactly once.
@MainActor final class ExtensionNotificationDelivery {
    private var background = UIBackgroundTaskIdentifier.invalid
    private var work: Task<Void, Never>?
    private var completion: (() -> Void)?

    static func run(_ event: ExtensionNotificationEvent, done: @escaping () -> Void) {
        let delivery = ExtensionNotificationDelivery()
        delivery.completion = done
        if event.action != nil, event.action?.foreground != true {
            delivery.background = UIApplication.shared.beginBackgroundTask(withName: "extension-notification") { @Sendable in
                Task { @MainActor in delivery.finish(cancel: true) }
            }
        }
        delivery.work = Task {
            await ExtensionNotificationHandler.handle(event, deps: ExtensionNotificationHandler.live(AppModel.shared))
            delivery.finish(cancel: false)
        }
    }

    private func finish(cancel: Bool) {
        guard let completion else { return }
        self.completion = nil
        if cancel { work?.cancel() }
        work = nil
        if background != .invalid {
            UIApplication.shared.endBackgroundTask(background)
            background = .invalid
        }
        completion()
    }
}
