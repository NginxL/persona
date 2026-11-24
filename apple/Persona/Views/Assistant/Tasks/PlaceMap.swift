import MapKit
import SwiftUI

// Places a location tool found, drawn on a map under the turn's work line (#5543), the way QuickResults draws a
// reminder it added: the call itself stays folded in "Tools N" (docs/SUPER_RULES.md, rule 2). The chat shows a still
// snapshot, so scrolling never drives a live map; tapping it opens one.

/// Where the phone is, the places a search found, a route worked out, or where directions were handed to Maps, read
/// from the tool call's result.
nonisolated struct PlaceMapResult: Hashable, Sendable {
    /// `route`: `phone_maps_route` worked out the way to a place without opening anything (#5544). `handoff`:
    /// `phone_maps_navigate` opened directions in Apple Maps (#5545); Maps does the navigating.
    enum Kind: Sendable { case here, places, route, handoff }

    /// How a route or the directions handed to Maps go, as the maps tools name it.
    enum Mode: String, Sendable { case driving, walking, transit }

    /// A point of a route's line.
    struct Point: Hashable, Sendable {
        var latitude: Double
        var longitude: Double

        var coordinate: CLLocationCoordinate2D { CLLocationCoordinate2D(latitude: latitude, longitude: longitude) }
    }

    struct Place: Hashable, Sendable {
        var name: String?
        var address: String?
        var latitude: Double
        var longitude: Double
        /// From the search center, when the tool measured it (`phone_maps_search_nearby`).
        var distance: Double?

        var coordinate: CLLocationCoordinate2D { CLLocationCoordinate2D(latitude: latitude, longitude: longitude) }
    }

    var kind: Kind
    var callID: String
    /// In the order the tool returned them, so pin 2 is the place the answer calls the second. A hand-off has one: the
    /// destination.
    var places: [Place]
    /// A route's or a hand-off's mode; nil when the result names none Persona knows.
    var mode: Mode?
    /// A route's line, from where it starts to the destination; empty for transit, which Apple Maps gives apps no line
    /// for, and for the other kinds.
    var line: [Point] = []
    /// How long a route takes, in seconds.
    var seconds: Double?

    /// The calls drawn this way, by the tool's own name (an MCP name ends in it: mcp__<server>__<name>).
    static func kind(of name: String) -> Kind? {
        switch name.components(separatedBy: "__").last ?? name {
        case "phone_location_current": .here
        case "phone_location_search", "phone_maps_search_nearby": .places
        case "phone_maps_route": .route
        case "phone_maps_navigate": .handoff
        default: nil
        }
    }

    /// The most pins a card draws: `phone_maps_search_nearby` returns up to 20, more than a small map can tell apart.
    static let most = 10

    init?(_ call: ToolCall) {
        guard let kind = Self.kind(of: call.name), let result = call.result, result.isError != true,
              let data = result.text?.data(using: .utf8),
              let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else { return nil }
        let found: [Place]
        switch kind {
        case .here:
            let address = obj["address"] as? [String: Any]
            found = Self.place(obj, name: nil, address: (address?["formatted"] as? String) ?? (address?["name"] as? String)).map { [$0] } ?? []
        case .places:
            found = ((obj["places"] as? [[String: Any]]) ?? []).compactMap {
                Self.place($0, name: $0["name"] as? String, address: $0["address"] as? String)
            }
        case .route:
            // The destination, with the route's length as its distance.
            found = Self.place(obj, name: obj["destination"] as? String, address: nil).map { [$0] } ?? []
        case .handoff:
            // Only directions Maps really opened: the tool says so with `opened: maps`.
            guard obj["opened"] as? String == "maps" else { return nil }
            found = Self.place(obj, name: obj["destination"] as? String, address: nil).map { [$0] } ?? []
        }
        guard !found.isEmpty else { return nil }
        self.kind = kind
        callID = call.id
        places = Array(found.prefix(Self.most))
        mode = kind == .route || kind == .handoff ? (obj["mode"] as? String).flatMap(Mode.init(rawValue:)) : nil
        if kind == .route {
            line = Self.line(obj["line"])
            seconds = (obj["expected_seconds"] as? NSNumber)?.doubleValue
        }
    }

    /// A route's `line`, `[[latitude, longitude], …]`: a line needs two good points, and one bad point spoils it.
    private static func line(_ raw: Any?) -> [Point] {
        guard let pairs = raw as? [[NSNumber]], pairs.count >= 2 else { return [] }
        let points = pairs.compactMap { pair -> Point? in
            guard pair.count == 2, (-90...90).contains(pair[0].doubleValue), (-180...180).contains(pair[1].doubleValue) else { return nil }
            return Point(latitude: pair[0].doubleValue, longitude: pair[1].doubleValue)
        }
        return points.count == pairs.count ? points : []
    }

    private static func place(_ obj: [String: Any], name: String?, address: String?) -> Place? {
        guard let lat = (obj["latitude"] as? NSNumber)?.doubleValue, (-90...90).contains(lat),
              let lon = (obj["longitude"] as? NSNumber)?.doubleValue, (-180...180).contains(lon) else { return nil }
        func text(_ s: String?) -> String? { s.flatMap { $0.isEmpty ? nil : $0 } }
        return Place(name: text(name), address: text(address), latitude: lat, longitude: lon,
                     distance: (obj["distance_meters"] as? NSNumber)?.doubleValue)
    }

    /// The region that shows every place (and a route's whole line) with room around it; at least `span` meters
    /// across, so one place is not a street-level close-up.
    func region(span: Double = 600) -> MKCoordinateRegion {
        let lats = places.map(\.latitude) + line.map(\.latitude), lons = places.map(\.longitude) + line.map(\.longitude)
        let (minLat, maxLat, minLon, maxLon) = (lats.min()!, lats.max()!, lons.min()!, lons.max()!)
        let center = CLLocationCoordinate2D(latitude: (minLat + maxLat) / 2, longitude: (minLon + maxLon) / 2)
        let metersPerDegree = 111_000.0
        let least = span / metersPerDegree
        return MKCoordinateRegion(center: center, span: MKCoordinateSpan(latitudeDelta: max((maxLat - minLat) * 1.5, least),
                                                                      longitudeDelta: max((maxLon - minLon) * 1.5, least)))
    }
}

/// What a place is called in a row: its name, else its address, else "Place N".
nonisolated enum PlaceMapText {
    static func title(_ place: PlaceMapResult.Place, number: Int) -> String {
        place.name ?? place.address ?? String(localized: "Place \(number) [Place Map]")
    }

    /// The line under a place's name: distance and address, whichever it has.
    static func detail(_ place: PlaceMapResult.Place) -> String? {
        var parts: [String] = []
        if let d = place.distance { parts.append(distance(d)) }
        if place.name != nil, let a = place.address { parts.append(a) }
        return parts.isEmpty ? nil : parts.joined(separator: " · ")
    }

    static func distance(_ meters: Double) -> String {
        Measurement(value: meters, unit: UnitLength.meters)
            .formatted(.measurement(width: .abbreviated, usage: .road, numberFormatStyle: .number.precision(.fractionLength(0...1))))
    }

    /// Where directions were handed to Maps: its name, else its address, else "Destination".
    static func destination(_ place: PlaceMapResult.Place) -> String {
        place.name ?? place.address ?? String(localized: "Destination [Place Map]")
    }

    /// "Walking", "Driving", "Transit".
    static func mode(_ mode: PlaceMapResult.Mode) -> String {
        switch mode {
        case .driving: String(localized: "Driving [Place Map]")
        case .walking: String(localized: "Walking [Place Map]")
        case .transit: String(localized: "Transit [Place Map]")
        }
    }

    /// The line under a route's destination: "Walking · 15 min · 1.2 km", leaving out what the result doesn't have.
    static func route(_ result: PlaceMapResult) -> String? {
        let parts = [result.mode.map(Self.mode), result.seconds.map(Self.duration), result.places.first?.distance.map(Self.distance)]
            .compactMap { $0 }
        return parts.isEmpty ? nil : parts.joined(separator: " · ")
    }

    /// "15 min", "1 hr, 5 min": hours and minutes, at least one minute.
    static func duration(_ seconds: Double) -> String {
        Duration.seconds(max(60, seconds)).formatted(.units(allowed: [.hours, .minutes], width: .abbreviated,
                                                             fractionalPart: .hide(rounded: .toNearestOrAwayFromZero)))
    }

    /// The line under a hand-off's destination: "Walking · Opened in Maps", the mode left out when the result has none.
    static func handoff(_ how: PlaceMapResult.Mode?) -> String {
        let opened = String(localized: "Opened in Maps [Place Map]")
        return how.map { mode($0) + " · " + opened } ?? opened
    }

    /// Hands a place to Apple Maps, with directions there from wherever the phone is: by `mode` when given (a hand-off
    /// opens again the way it was opened), else the way Maps prefers.
    @MainActor static func directions(to place: PlaceMapResult.Place, named name: String, mode: PlaceMapResult.Mode? = nil) {
        let item = MKMapItem(placemark: MKPlacemark(coordinate: place.coordinate))
        item.name = name
        let how = switch mode {
        case .driving: MKLaunchOptionsDirectionsModeDriving
        case .walking: MKLaunchOptionsDirectionsModeWalking
        case .transit: MKLaunchOptionsDirectionsModeTransit
        case nil: MKLaunchOptionsDirectionsModeDefault
        }
        item.openInMaps(launchOptions: [MKLaunchOptionsDirectionsModeKey: how])
    }
}

/// A still picture of the map and where each place (and a route's line) falls on it (0…1 across and down), made off
/// the main thread and kept for the chat's next pass over the same card.
nonisolated final class PlaceMapSnapshot: @unchecked Sendable {
    let image: UIImage
    let points: [CGPoint]
    let line: [CGPoint]

    init(image: UIImage, points: [CGPoint], line: [CGPoint] = []) {
        self.image = image
        self.points = points
        self.line = line
    }

    nonisolated(unsafe) private static let cache: NSCache<NSString, PlaceMapSnapshot> = { // NSCache is thread-safe
        let c = NSCache<NSString, PlaceMapSnapshot>()
        c.countLimit = 24
        return c
    }()

    static func key(_ result: PlaceMapResult, size: CGSize, dark: Bool) -> NSString {
        "\(result.callID)|\(Int(size.width))x\(Int(size.height))|\(dark ? "d" : "l")" as NSString
    }

    static func cached(_ key: NSString) -> PlaceMapSnapshot? { cache.object(forKey: key) }

    static func make(_ result: PlaceMapResult, size: CGSize, dark: Bool, scale: CGFloat) async -> PlaceMapSnapshot? {
        let key = key(result, size: size, dark: dark)
        if let hit = cache.object(forKey: key) { return hit }
        let options = MKMapSnapshotter.Options()
        options.region = result.region()
        options.size = size
        options.traitCollection = UITraitCollection { traits in
            traits.userInterfaceStyle = dark ? .dark : .light
            traits.displayScale = scale
        }
        options.pointOfInterestFilter = .excludingAll
        guard let shot = try? await MKMapSnapshotter(options: options).start() else { return nil }
        func spot(_ coordinate: CLLocationCoordinate2D) -> CGPoint {
            let p = shot.point(for: coordinate)
            return CGPoint(x: p.x / size.width, y: p.y / size.height)
        }
        let made = PlaceMapSnapshot(image: shot.image, points: result.places.map { spot($0.coordinate) },
                                    line: result.line.map { spot($0.coordinate) })
        cache.setObject(made, forKey: key)
        return made
    }
}

/// The card in the chat: the map with a numbered pin for each place (a dot for where the phone is), then the first few
/// places by number. Tapping the map opens `PlaceMapSheet`.
struct PlaceMapCard: View {
    var result: PlaceMapResult
    @Environment(\.colorScheme) private var scheme
    @Environment(\.displayScale) private var scale
    @State private var size: CGSize = .zero
    @State private var snapshot: PlaceMapSnapshot?
    @State private var open = false

    /// Rows under the map; the rest are on the map and in the sheet.
    static let rows = 3

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            Button { open = true } label: { map }
                .buttonStyle(.plain)
                .accessibilityLabel(Self.spoken(result))
                .accessibilityHint(String(localized: "Opens a larger map [Place Map]"))
                .accessibilityIdentifier("chat.place-map.\(result.kind).\(result.callID)")
            VStack(alignment: .leading, spacing: Theme.Space.s) {
                ForEach(result.places.indices.prefix(Self.rows), id: \.self) { i in
                    PlaceMapRow(result: result, place: result.places[i], number: i + 1)
                }
                if result.places.count > Self.rows {
                    Text(String(localized: "\(result.places.count - Self.rows) more on the map [Place Map]"))
                        .font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(1)
                }
            }
            .padding(Theme.Space.m)
        }
        .background(Theme.Fill.well, in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
        .clipShape(.rect(cornerRadius: Theme.Radius.card, style: .continuous))
        .sheet(isPresented: $open) { PlaceMapSheet(result: result) }
    }

    private var map: some View {
        Color.clear
            .scaledFrame(height: Theme.Size.chatMap)
            .frame(maxWidth: .infinity)
            .onGeometryChange(for: CGSize.self) { $0.size } action: { size = $0 }
            .background {
                if let snapshot {
                    Image(uiImage: snapshot.image).resizable().transition(.opacity)
                } else {
                    Theme.Fill.well
                }
            }
            .overlay {
                if let snapshot {
                    GeometryReader { box in
                        // A route's line, and the phone's dot where it starts, under the destination's pin (#5544).
                        if let start = snapshot.line.first {
                            Path { path in path.addLines(snapshot.line.map { CGPoint(x: $0.x * box.size.width, y: $0.y * box.size.height) }) }
                                .stroke(Theme.accent, style: StrokeStyle(lineWidth: Theme.Size.barWide, lineCap: .round, lineJoin: .round))
                            PlaceMapPin(kind: .here, number: 0)
                                .position(x: start.x * box.size.width, y: start.y * box.size.height)
                        }
                        ForEach(snapshot.points.indices, id: \.self) { i in
                            PlaceMapPin(kind: result.kind, number: i + 1)
                                .position(x: snapshot.points[i].x * box.size.width, y: snapshot.points[i].y * box.size.height)
                        }
                    }
                    .accessibilityHidden(true)
                }
            }
            .clipped()
            .animation(Theme.Motion.state, value: snapshot != nil)
            .task(id: PlaceMapSnapshot.key(result, size: size, dark: scheme == .dark)) {
                guard size.width > 0, size.height > 0 else { return }
                let key = PlaceMapSnapshot.key(result, size: size, dark: scheme == .dark)
                if let hit = PlaceMapSnapshot.cached(key) { snapshot = hit; return }
                snapshot = await PlaceMapSnapshot.make(result, size: size, dark: scheme == .dark, scale: scale)
            }
    }

    /// "Map: 1 Blue Bottle, 2 % Arabica, 3 Manner", "Map: your location, <address>", "Map: route to <place>, Walking ·
    /// 15 min · 1.2 km" or "Map: directions to <place>, opened in Maps".
    static func spoken(_ result: PlaceMapResult) -> String {
        switch result.kind {
        case .here:
            let address = result.places.first?.address ?? ""
            return String(localized: "Map: your location \(address) [Place Map]")
        case .places:
            let names = result.places.enumerated().map { "\($0.offset + 1) \(PlaceMapText.title($0.element, number: $0.offset + 1))" }
            return String(localized: "Map: \(names.joined(separator: ", ")) [Place Map]")
        case .route:
            let destination = result.places.first.map(PlaceMapText.destination) ?? ""
            return String(localized: "Map: route to \(destination), \(PlaceMapText.route(result) ?? "") [Place Map]")
        case .handoff:
            let destination = result.places.first.map(PlaceMapText.destination) ?? ""
            return String(localized: "Map: directions to \(destination), opened in Maps [Place Map]")
        }
    }
}

/// A place's number (or the phone's dot), name and distance, and Directions, which hands it to Maps. A route's row is
/// the destination with its mode, time and distance (#5544), with a labeled Start in Maps under it (#6177); a
/// hand-off's is Maps' icon, the destination, the mode, and a button that opens the same directions again (#5545).
private struct PlaceMapRow: View {
    var result: PlaceMapResult
    var place: PlaceMapResult.Place
    var number: Int

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            row
            if result.kind == .route { start }
        }
    }

    private var row: some View {
        HStack(spacing: Theme.Space.m) {
            Group {
                switch result.kind {
                case .here:
                    ThemedIcon(Theme.Icon.currentLocation).font(Theme.Font.secondaryStrong).foregroundStyle(Theme.accent)
                case .places:
                    CountBadge(count: number, look: .accent)
                case .route:
                    ThemedIcon(Theme.Icon.place).font(Theme.Font.secondaryStrong).foregroundStyle(Theme.accent)
                case .handoff:
                    BrandMark(Theme.Brand.iosMaps, side: Theme.Size.iconS)
                }
            }
            .scaledFrame(width: Theme.Size.iconS)
            .accessibilityHidden(true)
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                Text(title).font(Theme.Font.secondaryStrong).lineLimit(2)
                if let detail {
                    Text(detail).font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(2)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .accessibilityElement(children: .combine)
            switch result.kind {
            case .here, .route:
                EmptyView()
            case .places:
                directions(String(localized: "Directions [Place Map]"),
                           spoken: String(localized: "Directions to \(title) [Place Map]"), mode: nil)
            case .handoff:
                directions(String(localized: "Open in Maps [Place Map]"),
                           spoken: String(localized: "Open directions to \(title) in Maps [Place Map]"), mode: result.mode)
            }
        }
    }

    private var title: String {
        switch result.kind {
        case .here: String(localized: "Your Location [Place Map]")
        case .places: PlaceMapText.title(place, number: number)
        case .route, .handoff: PlaceMapText.destination(place)
        }
    }

    private var detail: String? {
        switch result.kind {
        case .here: place.address
        case .places: PlaceMapText.detail(place)
        case .route: PlaceMapText.route(result)
        case .handoff: PlaceMapText.handoff(result.mode)
        }
    }

    /// The icon-only button that hands the place to Maps; a route's and a hand-off's go the way the route went.
    private func directions(_ label: String, spoken: String, mode: PlaceMapResult.Mode?) -> some View {
        Button { PlaceMapText.directions(to: place, named: title, mode: mode) } label: {
            Label(label, systemImage: Theme.Icon.directions).labelStyle(.iconOnly)
        }
        .font(Theme.Font.captionStrong)
        .themedButton()
        .tapTargetFrame()
        .accessibilityLabel(spoken)
    }

    /// A route's main action, labeled across the card: it is what the card is for (#6177). One line: the icon goes
    /// first when the words don't fit (the largest text sizes), then the words scale down, never truncated.
    private var start: some View {
        Button { PlaceMapText.directions(to: place, named: title, mode: result.mode) } label: {
            let words = String(localized: "Start in Maps [Place Map]")
            ViewThatFits(in: .horizontal) {
                // titleAndIcon: inside the sheet's List a Label otherwise drops its icon.
                Label(words, systemImage: Theme.Icon.directions).labelStyle(.titleAndIcon)
                Text(words)
            }
            .lineLimit(1).minimumScaleFactor(0.5)
            .frame(maxWidth: .infinity)
        }
        .font(Theme.Font.labelStrong)
        .themedButton(prominent: true)
        .accessibilityLabel(String(localized: "Start directions to \(title) in Maps [Place Map]"))
        .accessibilityIdentifier("place-map.start")
    }
}

/// A place's mark on the map: its number on the accent, the phone's dot, or a route's or hand-off's destination.
private struct PlaceMapPin: View {
    var kind: PlaceMapResult.Kind
    var number: Int

    var body: some View {
        switch kind {
        case .here:
            Circle().fill(Theme.accent)
                .overlay(Circle().stroke(.white, lineWidth: Theme.Size.lineMedium))
                .scaledFrame(width: Theme.Size.markS, height: Theme.Size.markS)
        case .places:
            CountBadge(count: number, look: .accent)
                .overlay(Capsule().stroke(.white, lineWidth: Theme.Size.lineMedium))
        case .route, .handoff:
            ThemedIcon(Theme.Icon.place).font(Theme.Font.labelStrong).foregroundStyle(.white)
                .scaledFrame(width: Theme.Size.iconS, height: Theme.Size.iconS)
                .background(Theme.accent, in: .circle)
                .overlay(Circle().stroke(.white, lineWidth: Theme.Size.lineMedium))
        }
    }
}

/// The places on a live map that pans and zooms, the same numbers as in the chat, and Directions for each; a route's
/// line drawn on it.
struct PlaceMapSheet: View {
    var result: PlaceMapResult
    @Environment(\.dismiss) private var dismiss
    @State private var camera: MapCameraPosition

    init(result: PlaceMapResult) {
        self.result = result
        _camera = State(initialValue: .region(result.region()))
    }

    var body: some View {
        NavigationStack {
            VStack(spacing: 0) {
                Map(position: $camera) {
                    UserAnnotation()
                    if result.line.count > 1, let start = result.line.first {
                        MapPolyline(coordinates: result.line.map(\.coordinate))
                            .stroke(Theme.accent, style: StrokeStyle(lineWidth: Theme.Size.barWide, lineCap: .round, lineJoin: .round))
                        // Where the line starts, the dot the chat's card draws: the phone may have moved since (#6177).
                        Annotation(String(localized: "Start [Place Map]"), coordinate: start.coordinate) {
                            PlaceMapPin(kind: .here, number: 0)
                        }
                    }
                    ForEach(result.places.indices, id: \.self) { i in
                        let place = result.places[i]
                        switch result.kind {
                        case .here:
                            Marker(String(localized: "Your Location [Place Map]"), systemImage: Theme.Icon.currentLocation, coordinate: place.coordinate)
                                .tint(Theme.accent)
                        case .places:
                            Marker(PlaceMapText.title(place, number: i + 1), monogram: Text(verbatim: "\(i + 1)"), coordinate: place.coordinate)
                                .tint(Theme.accent)
                        case .route, .handoff:
                            Marker(PlaceMapText.destination(place), systemImage: Theme.Icon.place, coordinate: place.coordinate)
                                .tint(Theme.accent)
                        }
                    }
                }
                .accessibilityIdentifier("place-map-sheet.map")
                List {
                    ForEach(result.places.indices, id: \.self) { i in
                        PlaceMapRow(result: result, place: result.places[i], number: i + 1)
                    }
                }
                .listStyle(.plain)
                .scaledFrame(maxHeight: Theme.Size.box240)
            }
            .navigationTitle(title)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                SheetClose { dismiss() }
            }
        }
    }

    private var title: String {
        switch result.kind {
        case .here: String(localized: "Your Location [Place Map]")
        case .places: String(localized: "Places [Place Map]")
        case .route, .handoff: result.places.first.map(PlaceMapText.destination) ?? String(localized: "Destination [Place Map]")
        }
    }
}
