import CoreLocation
import MapKit
import SwiftUI

/// Settings › Assistant › Saved Places (#1598): home, the office and the other places the user names. A reminder by place
/// ("remind me to buy milk on the way home") goes off at the coordinates kept here; the assistant adds one only after
/// the user confirmed it in a chat, and the user can correct any of them here.
struct SavedPlacesView: View {
    @State private var page = SavedPlacesModel()
    @State private var editing: APIClient.SavedPlace?
    @State private var creating: APIClient.SavedPlace?
    /// The place whose swipe or context-menu Delete waits for the same confirmation as the editor's (#4248, HIG-004).
    @State private var deleting: APIClient.SavedPlace?

    var body: some View {
        List {
            if !page.places.isEmpty {
                Section {
                    ForEach(page.places) { p in row(p) }
                } footer: {
                    Text("Persona uses these for reminders like “remind me to buy milk on the way home”. It adds a place here only after you confirm it in a chat.")
                }
            }
        }
        .themedPage()
        .overlay {
            if !page.loaded {
                ProgressView()
            } else if let error = page.error, page.places.isEmpty {
                ContentUnavailableView("Couldn't Load Saved Places", systemImage: Theme.Icon.error, description: Text(error))
            } else if page.places.isEmpty {
                ContentUnavailableView("No Saved Places", systemImage: Theme.Icon.place,
                                       description: Text("Add home, the office and other places you go, so a reminder like “remind me to buy milk on the way home” goes off at the right spot."))
            }
        }
        .pageTitle(String(localized: "Saved Places"))
        .pageToolbar {
            ToolbarItem(placement: .primaryAction) {
                Button { creating = APIClient.SavedPlace() } label: { ThemedIcon(Theme.Icon.add) }
                    .accessibilityLabel("New Place")
                    .accessibilityIdentifier("saved-places-add")
            }
        }
        .navigationDestination(item: $editing) { p in
            SavedPlaceEditor(place: p, save: page.save, delete: page.delete)
        }
        .sheet(item: $creating) { p in
            NavigationStack { SavedPlaceEditor(place: p, save: page.save, delete: page.delete) }
        }
        .accessibleConfirmationDialog(Text("Delete “\(deleting?.name ?? "")”?"), isPresented: deletingShown, titleVisibility: .visible,
                            presenting: deleting) { p in
            Button("Delete", role: .destructive) { Task { await page.delete(p) } }
        } message: { _ in
            Text("Reminders already set for this place keep going off where they are.")
        }
        .task { await load() }
        .refreshable { await page.load() }
    }

    private var deletingShown: Binding<Bool> {
        Binding(get: { deleting != nil }, set: { if !$0 { deleting = nil } })
    }

    /// Delete only asks here: the swipe's button is not destructive (a destructive one hides the row before the answer)
    /// and a full swipe does nothing.
    private func row(_ p: APIClient.SavedPlace) -> some View {
        Button { editing = p } label: { SavedPlaceRow(place: p) }
            .foregroundStyle(.primary)
            .accessibilityIdentifier("saved-place-\(p.id)")
            .swipeActions(edge: .trailing, allowsFullSwipe: false) {
                Button { deleting = p } label: { Label("Delete", systemImage: Theme.Icon.delete) }
                    .tint(Theme.danger)
            }
            .contextMenu {
                Button { editing = p } label: { Label("Edit [Message Menu 051836]", systemImage: Theme.Icon.compose) }
                Button(role: .destructive) { deleting = p } label: { Label("Delete", systemImage: Theme.Icon.delete) }
            }
    }

    private func load() async {
        await page.load()
        #if DEBUG
        switch UserDefaults.standard.string(forKey: "openAssistantPage") {
        case "place" where editing == nil: editing = page.places.first
        case "newPlace" where creating == nil: creating = APIClient.SavedPlace()
        case "deletePlace" where deleting == nil: deleting = page.places.first
        default: break
        }
        #endif
    }
}

/// One saved place: its mark, its name, its address (or coordinates) and the reminder's radius.
struct SavedPlaceRow: View {
    let place: APIClient.SavedPlace

    var body: some View {
        HStack(spacing: Theme.Space.m) {
            ThemedIcon(Theme.Icon.place)
                .font(Theme.Font.heading)
                .foregroundStyle(.white)
                .scaledFrame(width: Theme.Size.wellM, height: Theme.Size.wellM)
                .background(Theme.accent.gradient, in: .rect(cornerRadius: Theme.Radius.control, style: .continuous))
                .accessibilityHidden(true)
            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                Text(place.name).font(Theme.Font.heading).lineLimit(1)
                Text(SavedPlaceText.detail(place)).font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(2)
            }
            Spacer(minLength: Theme.Space.s)
            Text(SavedPlaceText.radius(place.radiusMeters)).font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(1)
        }
        .contentShape(.rect)
    }
}

/// Adding or changing a saved place (#4248): its name; where it is, on a map that is there from the start, found by a
/// search that suggests places as the user types, from the current location, or by tapping the map; and the reminder's
/// radius, drawn as the circle on the map.
struct SavedPlaceEditor: View {
    @Environment(\.dismiss) private var dismiss
    @State private var draft: APIClient.SavedPlace
    /// It has coordinates: a saved place always does, a new one once a search, the current location or a tap set them.
    @State private var located: Bool
    @State private var camera: MapCameraPosition
    /// What the map shows, once it has said: suggestions and searches favor it, and a tap decides from it whether to zoom.
    @State private var visible: MKCoordinateRegion?
    @State private var suggestions = PlaceSuggestions()
    @State private var query = ""
    /// What the keyboard's Search key found; the suggestions show while there is none.
    @State private var results: [LocationTool.Found] = []
    @State private var searching = false
    @State private var locating = false
    /// The address of the spot tapped on the map is being looked up.
    @State private var addressing = false
    /// Why the search or the current location gave nothing.
    @State private var findError: String?
    @State private var saving = false
    @State private var confirmDelete = false
    /// Presented as the "new place" sheet: nothing is saved unless Add is tapped.
    private let creating: Bool
    private let opened: APIClient.SavedPlace
    var save: (APIClient.SavedPlace) async -> APIClient.SavedPlace?
    var delete: (APIClient.SavedPlace) async -> Void

    init(place: APIClient.SavedPlace, save: @escaping (APIClient.SavedPlace) async -> APIClient.SavedPlace?,
         delete: @escaping (APIClient.SavedPlace) async -> Void) {
        _draft = State(initialValue: place)
        _located = State(initialValue: !place.id.isEmpty)
        _camera = State(initialValue: place.id.isEmpty ? .automatic : .region(Self.region(around: place)))
        creating = place.id.isEmpty
        opened = place
        self.save = save
        self.delete = delete
    }

    private var canSave: Bool { SavedPlaceText.canSave(draft, located: located) && (creating || draft != opened) }

    var body: some View {
        List {
            Section {
                TextField("Name", text: $draft.name, prompt: Text("Home, Office…"))
                    .accessibilityIdentifier("saved-place-name")
            } footer: {
                Text("What you call it in a chat, as in “remind me when I get home”.")
            }
            Section {
                map
                if located { whereItIs }
                searchField
                if searching {
                    ProgressView().frame(maxWidth: .infinity)
                }
                if results.isEmpty {
                    ForEach(suggestions.found, id: \.self) { s in
                        Button { Task { await pick(s) } } label: { candidate(s.title, detail: s.subtitle) }
                            .foregroundStyle(.primary)
                            .disabled(searching)
                    }
                } else {
                    ForEach(results, id: \.self) { f in
                        Button { pick(f) } label: {
                            candidate(f.name ?? f.address ?? SavedPlaceText.coordinates(APIClient.SavedPlace(latitude: f.latitude, longitude: f.longitude)),
                                      detail: f.address)
                        }
                        .foregroundStyle(.primary)
                    }
                }
                Button { Task { await useCurrentLocation() } } label: {
                    if locating {
                        ProgressView()
                    } else {
                        Label("Use Current Location", systemImage: Theme.Icon.currentLocation).lineLimit(1)
                    }
                }
                .disabled(locating)
                .accessibilityIdentifier("saved-place-current")
            } header: {
                Text("Location")
            } footer: {
                Text(locationNote)
            }
            Section {
                Picker("Reminder Radius", selection: $draft.radiusMeters) {
                    ForEach(SavedPlaceText.radii(with: draft.radiusMeters), id: \.self) { r in
                        Text(SavedPlaceText.radius(r)).tag(r)
                    }
                }
                .lineLimit(1)
            } footer: {
                Text("A reminder goes off when you cross this circle. Indoors your phone's location is often 50–100 m off, so a smaller circle can miss.")
            }
            if !creating {
                Section {
                    Button(role: .destructive) { confirmDelete = true } label: {
                        Label("Delete Place", systemImage: Theme.Icon.delete).lineLimit(1)
                    }
                }
            }
        }
        .themedPage()
        .navigationTitle(creating ? String(localized: "New Place") : (draft.name.isEmpty ? String(localized: "Saved Places") : draft.name))
        .navigationBarTitleDisplayMode(.inline)
        .sheetCancel(unsaved: draft != opened, when: creating) { dismiss() }
        .toolbar {
            SheetConfirm(id: "saved-place-save", disabled: !canSave, busy: saving) { Task { await persist() } }
        }
        .interactiveDismissDisabled(saving)
        .onAppear {
            // A new place opens on the user when Persona may already see where they are; it never asks by itself.
            if SavedPlaceText.opening(located: located, location: LocationAccess.status()) == .user {
                camera = .userLocation(fallback: .automatic)
            }
        }
        .onChange(of: query) { _, q in
            results = []
            findError = nil
            suggestions.update(q, near: visible)
        }
        .onChange(of: draft.radiusMeters) { if located { recenter() } }
        .accessibleConfirmationDialog("Delete “\(opened.name)”?", isPresented: $confirmDelete, titleVisibility: .visible) {
            Button("Delete", role: .destructive) {
                Task { await delete(opened) }
                dismiss()
            }
        } message: {
            Text("Reminders already set for this place keep going off where they are.")
        }
    }

    /// The map, there from the start: the place and the reminder's circle once it has coordinates, and the user's own
    /// location when Persona may see it. It pans and zooms; a tap puts the place where it landed.
    private var map: some View {
        MapReader { proxy in
            Map(position: $camera) {
                UserAnnotation()
                if located {
                    MapCircle(center: center, radius: CLLocationDistance(draft.radiusMeters))
                        .foregroundStyle(Theme.accent.opacity(0.18))
                        .stroke(Theme.accent, lineWidth: Theme.Size.lineMedium)
                    Marker(draft.name, systemImage: Theme.Icon.place, coordinate: center)
                        .tint(Theme.accent)
                }
            }
            .onMapCameraChange(frequency: .onEnd) { visible = $0.region }
            .onTapGesture { point in
                guard let spot = proxy.convert(point, from: .local) else { return }
                Task { await move(to: spot) }
            }
        }
        .scaledFrame(height: Theme.Size.placeMap)
        .clipShape(.rect(cornerRadius: Theme.Radius.control, style: .continuous))
        .listRowInsets(EdgeInsets(top: Theme.Space.s, leading: Theme.Space.s, bottom: Theme.Space.s, trailing: Theme.Space.s))
        .accessibilityIdentifier("saved-place-map")
    }

    /// The place's address and coordinates, under the map.
    private var whereItIs: some View {
        HStack(spacing: Theme.Space.s) {
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                if !draft.address.isEmpty { Text(draft.address).font(Theme.Font.body).lineLimit(2) }
                Text(SavedPlaceText.coordinates(draft)).font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(1)
            }
            Spacer(minLength: Theme.Space.s)
            if addressing { ProgressView() }
        }
        .accessibilityElement(children: .combine)
    }

    /// A search field like the sidebar's: suggestions follow what is typed, the Search key looks further.
    private var searchField: some View {
        HStack(spacing: Theme.Space.s) {
            ThemedIcon(Theme.Icon.search).foregroundStyle(.secondary).accessibilityHidden(true)
            TextField("Search for an Address or Place", text: $query)
                .submitLabel(.search)
                .autocorrectionDisabled()
                .onSubmit { Task { await search() } }
                .accessibilityIdentifier("saved-place-search")
            if !query.isEmpty {
                Button { query = "" } label: { ThemedIcon(Theme.Icon.clear).foregroundStyle(.tertiary) }
                    .buttonStyle(.plain)
                    .accessibilityLabel("Clear Search")
            }
        }
    }

    private func candidate(_ title: String, detail: String?) -> some View {
        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
            Text(title).font(Theme.Font.body).lineLimit(1)
            if let detail, !detail.isEmpty, detail != title {
                Text(detail).font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(2)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .contentShape(.rect)
    }

    /// The location section's footer: what went wrong, else the ways to set the place.
    private var locationNote: String {
        findError
            ?? (suggestions.failed ? SavedPlaceText.searchNote(.failed, query: query) : nil)
            ?? String(localized: "Search, use your current location, or tap the map where the place is.")
    }

    private var center: CLLocationCoordinate2D { CLLocationCoordinate2D(latitude: draft.latitude, longitude: draft.longitude) }

    private static func region(around p: APIClient.SavedPlace) -> MKCoordinateRegion {
        let span = SavedPlaceText.mapSpan(radius: p.radiusMeters)
        return MKCoordinateRegion(center: CLLocationCoordinate2D(latitude: p.latitude, longitude: p.longitude),
                                  latitudinalMeters: span, longitudinalMeters: span)
    }

    /// The map back on the place and its whole circle: after a search, the current location or a new radius.
    private func recenter() {
        withAnimation(Theme.Motion.standard) { camera = .region(Self.region(around: draft)) }
    }

    private func search() async {
        let q = query.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !q.isEmpty else { return }
        searching = true
        defer { searching = false }
        findError = nil
        let outcome: SavedPlaceText.SearchOutcome
        var found: [LocationTool.Found] = []
        do {
            found = try await LocationTool.find(q, limit: 6, near: visible)
            outcome = found.isEmpty ? .nothing : .found
        } catch {
            outcome = SavedPlaceText.SearchOutcome(error)
        }
        // Typing since the Search key started a new search; this one's answer is stale.
        guard query.trimmingCharacters(in: .whitespacesAndNewlines) == q else { return }
        results = found
        findError = SavedPlaceText.searchNote(outcome, query: q)
    }

    /// A suggestion is only a name: MapKit looks up where it is.
    private func pick(_ s: MKLocalSearchCompletion) async {
        searching = true
        defer { searching = false }
        findError = nil
        do {
            if let f = try await LocationTool.find(s) {
                pick(f)
            } else {
                findError = SavedPlaceText.searchNote(.nothing, query: s.title)
            }
        } catch {
            findError = SavedPlaceText.searchNote(SavedPlaceText.SearchOutcome(error), query: s.title)
        }
    }

    private func pick(_ f: LocationTool.Found) {
        Haptics.tap()
        draft.latitude = f.latitude
        draft.longitude = f.longitude
        draft.address = f.address ?? f.name ?? ""
        if draft.name.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty, let name = f.name { draft.name = String(name.prefix(40)) }
        located = true
        addressing = false
        results = []
        query = ""
        findError = nil
        recenter()
    }

    /// A tap on the map: the place goes there, and its address follows once MapKit says what is there.
    private func move(to spot: CLLocationCoordinate2D) async {
        Haptics.tap()
        draft.latitude = spot.latitude
        draft.longitude = spot.longitude
        located = true
        query = ""
        findError = nil
        if SavedPlaceText.zoomsIn(shownLatitudeDelta: visible?.span.latitudeDelta, radius: draft.radiusMeters) {
            recenter()
        } else if camera.followsUserLocation, let visible {
            camera = .region(visible) // stays where the user tapped instead of following them
        }
        addressing = true
        let address = await LocationTool.address(of: CLLocation(latitude: spot.latitude, longitude: spot.longitude))
        // A later tap, search or the current location moved the place again; its own address wins.
        guard draft.latitude == spot.latitude, draft.longitude == spot.longitude else { return }
        addressing = false
        draft.address = address ?? ""
    }

    private func useCurrentLocation() async {
        locating = true
        defer { locating = false }
        findError = nil
        switch await LocationAccess.request() {
        case .granted, .limited, .notNeeded: break
        case .denied, .notDetermined:
            findError = String(localized: "Allow Persona to use your location in Settings › Privacy & Security › Location Services, or search for the address.")
            return
        }
        do {
            let location = try await OneShotLocation.fetch(timeout: 15)
            draft.latitude = location.coordinate.latitude
            draft.longitude = location.coordinate.longitude
            located = true
            addressing = false
            results = []
            recenter()
            let address = await LocationTool.address(of: location)
            // A tap or a search while the address was looked up moved the place; its own address wins.
            guard draft.latitude == location.coordinate.latitude, draft.longitude == location.coordinate.longitude else { return }
            draft.address = address ?? ""
        } catch {
            findError = String(localized: "Couldn't get your location. Try again outdoors, or search for the address.")
        }
    }

    private func persist() async {
        saving = true
        defer { saving = false }
        if await save(draft) != nil {
            Haptics.success()
            dismiss()
        }
    }
}

/// Places suggested while the user types where a saved place is, as Maps suggests them (#4248): MapKit's completer,
/// favoring what the editor's map shows. Like the location manager, it answers on the main thread.
@Observable final class PlaceSuggestions: NSObject, MKLocalSearchCompleterDelegate {
    private(set) var found: [MKLocalSearchCompletion] = []
    /// The last query couldn't be completed (offline, too many requests): the editor says the search couldn't run.
    private(set) var failed = false
    @ObservationIgnored private let completer = MKLocalSearchCompleter()

    /// As many as fit above the keyboard on a phone.
    private static let shown = 6

    override init() {
        super.init()
        completer.delegate = self
        completer.resultTypes = [.address, .pointOfInterest]
    }

    func update(_ query: String, near region: MKCoordinateRegion?) {
        let q = query.trimmingCharacters(in: .whitespacesAndNewlines)
        failed = false
        guard !q.isEmpty else {
            completer.cancel()
            found = []
            return
        }
        if let region { completer.region = region }
        completer.queryFragment = q
    }

    func completerDidUpdateResults(_ completer: MKLocalSearchCompleter) {
        found = Array(completer.results.prefix(Self.shown))
    }

    func completer(_ completer: MKLocalSearchCompleter, didFailWithError error: any Error) {
        found = []
        failed = SavedPlaceText.SearchOutcome(error) == .failed
    }
}
