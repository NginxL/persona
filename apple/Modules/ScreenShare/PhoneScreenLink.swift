import Foundation
import Observation

/// Phone Screen (#9314, docs/extensions/computer-use.md "Phone Screen"): one live screen's portrait display on its Mac
/// and the window on it, through the hub. The page starts it for its RFB connection (`pair`) with the picture's size in
/// points; the Mac binds it to that connection, so a new connection starts it again and a closed one ends it on the Mac
/// even when `stop` never arrives.
@MainActor @Observable
final class PhoneScreenLink {
    enum Phase: Equatable {
        case off
        /// Adding the display (a second or two).
        case starting
        case on
        /// Why it couldn't start, in plain words; the page shows it and stays in Mouse Mode's touches.
        case failed(String)
    }

    let ref: MachineRef
    private(set) var phase: Phase = .off
    /// The display on the Mac (global points, with its scale) and every display then, to find it on the picture.
    private(set) var display: MachineDisplay?
    private(set) var displays: [MachineDisplay] = []
    private(set) var windows: [PhoneScreenWindow] = []
    private(set) var loadingWindows = false
    /// The window being put on the display, while it moves.
    private(set) var placing: String?
    /// The window on the display.
    private(set) var placed: PhoneScreenWindow?
    /// The last refusal of a window request, for the sheet.
    private(set) var windowError: String?

    /// The connection and size it was started for: the same again is nothing to do.
    private var started: (pair: String, width: Int, height: Int)?
    private var task: Task<Void, Never>?
    /// Stands in for the hub in tests and screenshots.
    @ObservationIgnored var call: @MainActor (PhoneScreenRequest) async throws -> PhoneScreenReply

    init(ref: MachineRef, call: (@MainActor (PhoneScreenRequest) async throws -> PhoneScreenReply)? = nil) {
        self.ref = ref
        self.call = call ?? { body in
            guard let api = AppModel.shared.api(ref.group) else { throw URLError(.notConnectedToInternet) }
            return try await api.phoneScreen(machine: ref.id, body)
        }
    }

    /// The display for this connection at this size (points); again for the same does nothing.
    func start(pair: String, size: CGSize) {
        let w = Int(size.width.rounded()), h = Int(size.height.rounded())
        guard w > 0, h > 0 else { return }
        // The same connection at nearly the same shape (a bar's line came or went): the display stays as it is.
        if let s = started, s.pair == pair, phase != .off,
           abs(Double(h) / Double(w) - Double(s.height) / Double(s.width)) < 0.05 * Double(s.height) / Double(s.width) { return }
        let fresh = started?.pair != pair
        started = (pair, w, h)
        if fresh { placed = nil; windows = [] }
        phase = .starting
        task?.cancel()
        task = Task { [weak self] in
            guard let self else { return }
            do {
                let r = try await call(PhoneScreenRequest(pair: pair, op: "start", width: Double(w), height: Double(h)))
                guard !Task.isCancelled, started?.pair == pair else { return }
                display = r.display
                displays = r.displays ?? []
                phase = r.display == nil ? .failed(Self.message(nil)) : .on
            } catch {
                guard !Task.isCancelled, started?.pair == pair else { return }
                phase = .failed(Self.message(error))
            }
        }
    }

    /// Ends it: the Mac puts its windows back and lets the display go. Nothing to wait for.
    func stop() {
        task?.cancel()
        task = nil
        guard let pair = started?.pair else { return }
        started = nil
        phase = .off
        display = nil
        placed = nil
        let call = call
        Task { _ = try? await call(PhoneScreenRequest(pair: pair, op: "stop")) }
    }

    /// The Mac's windows now, front to back.
    func loadWindows() async {
        guard let pair = started?.pair, phase == .on else { return }
        loadingWindows = true
        defer { loadingWindows = false }
        do {
            windows = try await call(PhoneScreenRequest(pair: pair, op: "windows")).windows ?? []
            windowError = nil
            placed = windows.first { $0.placed == true } ?? placed
        } catch {
            if !Self.cancelled(error) { windowError = Self.message(error) }
        }
    }

    /// Puts `w` on the display (the one there goes back); true once it is there.
    @discardableResult
    func place(_ w: PhoneScreenWindow) async -> Bool {
        guard let pair = started?.pair, phase == .on else { return false }
        placing = w.id
        defer { placing = nil }
        do {
            let r = try await call(PhoneScreenRequest(pair: pair, op: "place", pid: w.pid, window: w.window))
            // The display may have changed width for this window (#9500): the arrangement that explains the picture now.
            if let ds = r.displays, !ds.isEmpty { displays = ds } else if let d = r.display { displays = Self.replacing(d, in: displays) }
            if let d = r.display { display = d }
            placed = r.placed ?? w
            windowError = nil
            return true
        } catch {
            if !Self.cancelled(error) { windowError = Self.message(error) }
            return false
        }
    }

    /// `d` in place of the display with its id (a helper before #9500 sent the display alone).
    static func replacing(_ d: MachineDisplay, in displays: [MachineDisplay]) -> [MachineDisplay] {
        displays.map { $0.id == d.id ? d : $0 }
    }

    private static func cancelled(_ error: Error) -> Bool {
        error is CancellationError || (error as? URLError)?.code == .cancelled
    }

    /// A refusal in plain words, by the Mac's code.
    static func message(_ error: Error?) -> String {
        switch (error as? APIError)?.code {
        case "permission"?: String(localized: "Allow Persona Input on this Mac to use Phone Screen (Privacy & Security › Accessibility).")
        case "busy"?: String(localized: "Another device is using this Mac's phone screen.")
        case "unavailable"?: String(localized: "This Mac can't show a phone screen yet. Update Persona Link on it.")
        case "not_signed_in"?: String(localized: "The screen is still connecting. Try again in a moment.")
        case "window_gone"?, "no_session"?, "no_display"?: String(localized: "That window is gone. Pick another.")
        case "not_responding"?: String(localized: "That app isn't responding. Pick another window.")
        default: String(localized: "The Mac couldn't set up the phone screen.")
        }
    }
}
