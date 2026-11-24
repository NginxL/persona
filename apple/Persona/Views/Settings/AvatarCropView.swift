import SwiftUI

/// Settings › Appearance › Avatar › New from Photo (#2405): the picked image in a square, a circle over it showing what the
/// avatar keeps; drag to move it, pinch (or, on the Mac, the slider) to zoom. A name (required) and a description, then
/// Upload (`AvatarUploadModel`): the hub makes the avatar and this device chooses it.
struct AvatarCropView: View {
    @Bindable var model: AvatarUploadModel
    @Environment(\.dismiss) private var dismiss
    /// The drag so far: each change moves the square by what was added.
    @State private var dragged: CGSize = .zero
    /// The zoom when the pinch began.
    @State private var pinchedFrom: Double?
    @FocusState private var field: Field?

    private enum Field { case name, subtitle }

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    cropArea
                        .frame(maxWidth: .infinity)
                        .listRowBackground(Color.clear)
                        .listRowInsets(EdgeInsets())
                } footer: {
                    Text("Drag to move the photo. Pinch to zoom.")
                }
                // Made already: the crop and the names would not be sent again.
                .disabled(model.made != nil)
                Section {
                    TextField(String(localized: "Name"), text: $model.name)
                        .focused($field, equals: .name)
                        .submitLabel(.next)
                        .onSubmit { field = .subtitle }
                        .accessibilityIdentifier("avatar-upload-name")
                    TextField(String(localized: "Description (Optional)"), text: $model.subtitle)
                        .focused($field, equals: .subtitle)
                        .submitLabel(.done)
                        .accessibilityIdentifier("avatar-upload-subtitle")
                } footer: {
                    Text("Persona goes by this name in the top bar and the chat. The description shows only here in Settings.")
                }
                .disabled(model.made != nil)
                if let error = model.error {
                    Section { LoadProblem(text: error) }
                        .accessibilityIdentifier("avatar-upload-error")
                }
            }
            .themedPage()
            .navigationTitle(String(localized: "New Avatar"))
            .navigationBarTitleDisplayMode(.inline)
            // No Cancel while it uploads: the avatar would be made all the same. Once made, closing loses nothing: it is
            // in the list.
            .sheetCancel(unsaved: !model.trimmedName.isEmpty && model.made == nil, when: !model.uploading) { dismiss() }
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button {
                        field = nil
                        Task {
                            if await model.upload() {
                                Haptics.success()
                                dismiss()
                            } else {
                                Haptics.error()
                            }
                        }
                    } label: {
                        if model.uploading {
                            ProgressView()
                        } else if model.made != nil {
                            // Made but not downloaded here: the download again, not another upload.
                            Text("Try Again").lineLimit(1)
                        } else {
                            // Its own key: Japanese says Create, short enough to leave the title whole.
                            Text(String(localized: "avatar.upload", defaultValue: "Upload")).lineLimit(1)
                        }
                    }
                    .disabled(!model.canUpload)
                    .accessibilityIdentifier("avatar-upload")
                }
            }
            .interactiveDismissDisabled(model.uploading)
        }
    }

    /// The image under a square window, dimmed outside the circle the avatar keeps.
    private var cropArea: some View {
        GeometryReader { geo in
            let side = min(geo.size.width, geo.size.height)
            let crop = model.crop
            let k = side / crop.side // points per pixel of the image
            Theme.Fill.well
                .overlay {
                    Image(decorative: model.image, scale: 1)
                        .resizable()
                        .interpolation(.high)
                        .frame(width: crop.width * k, height: crop.height * k)
                        .offset(x: (crop.width / 2 - crop.center.x) * k, y: (crop.height / 2 - crop.center.y) * k)
                }
                .overlay { CropShade().fill(Theme.Crop.outside, style: FillStyle(eoFill: true)) }
                .overlay { Circle().strokeBorder(Theme.Crop.edge, lineWidth: Theme.Size.lineMedium) }
                .frame(width: side, height: side)
                .clipShape(.rect(cornerRadius: Theme.Radius.card, style: .continuous))
                .contentShape(.rect)
                .gesture(drag(side).simultaneously(with: pinch))
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
        .aspectRatio(1, contentMode: .fit)
        .frame(maxWidth: Theme.Size.avatarCrop)
        .accessibilityElement()
        .accessibilityLabel(String(localized: "Crop"))
        .accessibilityValue(String(localized: "Zoom \(model.crop.zoom.formatted(.percent.precision(.fractionLength(0))))"))
        .accessibilityAdjustableAction { direction in
            switch direction {
            case .increment: model.crop = model.crop.zoomed(to: model.crop.zoom * Self.zoomStep)
            case .decrement: model.crop = model.crop.zoomed(to: model.crop.zoom / Self.zoomStep)
            @unknown default: break
            }
        }
        .accessibilityIdentifier("avatar-crop")
    }

    /// A step of the adjustable action (VoiceOver's swipe up and down).
    private static let zoomStep = 1.25

    /// The picture follows the finger; each change moves it by what the drag added (stopping at the image's edges).
    private func drag(_ side: CGFloat) -> some Gesture {
        DragGesture(minimumDistance: 0)
            .onChanged { v in
                let step = CGSize(width: v.translation.width - dragged.width, height: v.translation.height - dragged.height)
                dragged = v.translation
                model.crop = model.crop.panned(by: step, viewSide: side)
            }
            .onEnded { _ in dragged = .zero }
    }

    private var pinch: some Gesture {
        MagnifyGesture()
            .onChanged { v in
                let from = pinchedFrom ?? model.crop.zoom
                pinchedFrom = from
                model.crop = model.crop.zoomed(to: from * v.magnification)
            }
            .onEnded { _ in pinchedFrom = nil }
    }
}

/// The square with the circle cut out (even-odd): what the avatar leaves out.
private nonisolated struct CropShade: Shape {
    func path(in rect: CGRect) -> Path {
        var p = Path(rect)
        p.addEllipse(in: rect)
        return p
    }
}
