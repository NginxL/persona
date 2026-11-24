import PhotosUI
import SwiftUI

// Photos sent with a message: an agent's composer (`Composer`) and New Agent's first message (#2321). Each is scaled
// down to the hub's long side (1600 px) and sent as JPEG, as many as the hub's `image_limits` allow (#12973, 20 a
// message); the ones past them go to the computer as files. Videos are the composer's own (VideoAttach.swift): they go
// to the computer as files and count toward neither.

@MainActor enum MessagePhotos {
    /// The long side a picture is scaled down to before it goes (the hub's `max_side`).
    static let maxSide = CGFloat(ImageLimits.standard.maxSide)

    /// A picture as a message carries it; nil when it can't be encoded.
    static func payload(_ img: UIImage, maxSide: CGFloat = maxSide) -> (UIImage, ImagePayload)? {
        let scaled = img.scaledDown(maxSide: maxSide)
        guard let jpeg = scaled.jpegData(compressionQuality: 0.75) else { return nil }
        return (scaled, ImagePayload(mime: "image/jpeg", data: jpeg.base64EncodedString()))
    }

    /// img after images when it fits `limits` (#12973). One past them is not dropped: `spill` sends it as a file to the
    /// session's computer (true when it took it) and a toast says which limit it was; with no `spill`, or one that could
    /// not take it, the toast says it was not added.
    static func add(_ img: UIImage, to images: inout [(UIImage, ImagePayload)], model: AppModel, limits: ImageLimits,
                    spill: ((UIImage) -> Bool)? = nil) {
        guard images.count < limits.perMessage, let p = payload(img, maxSide: CGFloat(limits.maxSide)) else {
            if images.count >= limits.perMessage { overflow(img, .count, limits: limits, model: model, spill: spill) }
            return
        }
        let room = limits.room(held: images.map(\.1.data.utf8.count), size: p.1.data.utf8.count)
        guard room == .ok else { return overflow(img, room, limits: limits, model: model, spill: spill) }
        images.append(p)
        Haptics.tap()
    }

    /// The word on pictures sent as files while it is still the toast shown: the next one of the same burst (a pick of
    /// many) adds to its count rather than saying "1" again.
    private static var spilled: (toast: Toast.ID, why: ImageLimits.Room, count: Int)?

    /// A picture past `limits` (`why`): a file through `spill`, else turned away, with a word on which limit it was and,
    /// for files, how many went (as the web says it, #12973).
    static func overflow(_ img: UIImage, _ why: ImageLimits.Room, limits: ImageLimits, model: AppModel, spill: ((UIImage) -> Bool)?) {
        if let spill, spill(img) {
            let count = spilled.flatMap { $0.toast == model.toast?.id && $0.why == why ? $0.count + 1 : nil } ?? 1
            let toast = Toast(why == .count ? String(localized: "A message holds up to \(limits.perMessage) pictures. The other \(count) will go as files.")
                                            : String(localized: "These pictures are too large together. \(count) of them will go as files."))
            model.toast = toast
            spilled = (toast.id, why, count)
        } else {
            model.toast = Toast(why == .count ? String(localized: "Up to \(limits.perMessage) at a time")
                                              : String(localized: "These pictures are too large to send together"), style: .error)
        }
    }

    /// A `spill` for a composer whose files go to `machine` (#12973): the picture is written as a JPEG off the main actor
    /// and joins `files` as the app's own copy (deleted once it reached the computer); false, so the picture is turned
    /// away, with no computer or with the files full.
    static func spill(into files: FileAttachments, machine: MachineRef?, model: AppModel, dest: String? = nil) -> ((UIImage) -> Bool)? {
        guard let machine else { return nil }
        return { img in
            guard files.items.count < FileAttachments.most else { return false }
            Task {
                if let url = await Task.detached(priority: .userInitiated, operation: { fileCopy(img) }).value {
                    files.start([url], machine: machine, model: model, dest: dest, owned: true)
                }
            }
            return true
        }
    }

    /// img as a JPEG of its own in a fresh temporary folder; nil when it can't be written. Off the main actor (P1).
    nonisolated static func fileCopy(_ img: UIImage) -> URL? {
        guard let jpeg = img.jpegData(compressionQuality: 0.9) else { return nil }
        let dir = FileManager.default.temporaryDirectory.appending(path: "photo-files/" + UUID().uuidString)
        let url = dir.appending(path: "Photo-\(UUID().uuidString.prefix(8)).jpg")
        do {
            try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
            try jpeg.write(to: url)
            return url
        } catch {
            return nil
        }
    }

    /// Reads the picked pictures in order and hands each to add (iCloud-only ones are downloaded here, HEIC and the
    /// like re-encoded by `payload`); a toast says how many couldn't be read.
    static func load(_ items: [PhotosPickerItem], model: AppModel, add: (UIImage) -> Void) async {
        var failed = 0
        for item in items {
            guard let data = try? await item.loadTransferable(type: Data.self), let img = UIImage(data: data) else {
                failed += 1
                continue
            }
            add(img)
        }
        if failed > 0 {
            model.toast = Toast(String(localized: "Couldn't read \(failed) photos (they may still be downloading from iCloud) [Chat View 5712c0]"), style: .error)
        }
    }
}
