import SwiftUI

/// Health Trends (#3272): the phone's steps, sleep and weight from Apple Health as the cards a health answer draws
/// (```persona-card through `AssistantViewCard`), for the health space's home (#2901) to put in as it is.
/// Asks for Health when it was never asked; says so when nothing could be read (the system never tells an app whether
/// reading was allowed, so no data and no permission look the same).
struct HealthTrendsSection: View {
    @State var model: HealthTrendsModel
    var automaticallyLoads = true

    init(model: HealthTrendsModel = HealthTrendsModel(), automaticallyLoads: Bool = true) {
        _model = State(initialValue: model)
        self.automaticallyLoads = automaticallyLoads
    }

    var body: some View {
        section.tabActiveTask(id: model.range) { if automaticallyLoads { await model.load() } }
    }

    private var section: some View {
        VStack(alignment: .leading, spacing: Theme.Space.titleToContent) {
            HStack {
                Text("Health Trends").font(Theme.Font.heading)
                Spacer(minLength: Theme.Space.s)
                Picker("Time Range", selection: $model.range) {
                    ForEach(HealthTrendRange.allCases) { Text($0.title).lineLimit(1).tag($0) }
                }
                .pickerStyle(.segmented)
                .fixedSize()
                .accessibilityIdentifier("health-trends-range")
            }
            content
            Text("Data is read and charted only on this device and never sent to the hub.")
                .font(Theme.Font.caption)
                .foregroundStyle(.secondary)
        }
        // A container: without it the id would land on every element inside and hide the cards' own ids.
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("health-trends")
    }

    @ViewBuilder private var content: some View {
        switch model.state {
        case .loading:
            ProgressView().frame(maxWidth: .infinity).padding(Theme.Space.xl)
        case .needsAccess:
            EmptyStateView(symbol: Theme.Icon.healthTrends, title: String(localized: "Read Health Data"),
                           message: String(localized: "Allow Persona to read steps, sleep and weight, and trends will be charted here."),
                           action: (title: String(localized: "Allow Access"), run: { Task { await model.requestAccess() } }))
        case .unavailable:
            EmptyStateView(symbol: Theme.Icon.healthTrends, title: String(localized: "No Health Data on This Device"),
                           message: String(localized: "Trends for steps, sleep and weight appear on an iPhone with the Health app."), action: nil)
        case .loaded(let cards) where cards.isEmpty:
            EmptyStateView(symbol: Theme.Icon.healthTrends, title: String(localized: "No Data Yet"),
                           message: String(localized: "No steps, sleep or weight data in this period. If you haven't allowed access, turn it on in iOS Settings › Health › Data Access & Devices › Persona."),
                           action: nil)
        case .loaded(let cards):
            ForEach(HealthTrendMetric.allCases) { metric in
                if let view = cards[metric]?.view() {
                    AssistantViewCard(view: view).accessibilityIdentifier("health-trend-\(metric.rawValue)")
                }
            }
        }
    }
}

#if DEBUG
extension View {
    /// `-showHealthTrends YES`: Health Trends in a sheet reading this device's Health; `-showHealthTrends sample`: with made-up
    /// days (a simulator has no Health data), `-showHealthTrends empty` / `ask`: the empty and never-asked states.
    func healthTrendsSample() -> some View { modifier(HealthTrendsSample()) }
}

private struct HealthTrendsSample: ViewModifier {
    @State private var shown = UserDefaults.standard.string(forKey: "showHealthTrends") != nil

    private static var deps: HealthTrendsModel.Deps {
        let mode = UserDefaults.standard.string(forKey: "showHealthTrends") ?? ""
        switch mode {
        case "sample", "empty", "ask":
            return HealthTrendsModel.Deps(access: { mode == "ask" ? .notDetermined : .granted }, requestAccess: { true },
                                          daily: { metric, days in mode == "empty" ? [:] : Self.sampleValues(metric, days) },
                                          now: { Date() })
        default:
            return .live
        }
    }

    /// Steady made-up days: steps every day, sleep with one night missing, weight a few times a week.
    static func sampleValues(_ metric: HealthTrendMetric, _ days: Int) -> [String: Double] {
        var out: [String: Double] = [:]
        for (i, key) in HealthTrends.dayKeys(days, now: Date()).enumerated() {
            let wave = Double((i * 37) % 11) / 10
            switch metric {
            case .steps: out[key] = (5_200 + 4_800 * wave).rounded()
            case .sleep: if i % 9 != 4 { out[key] = ((6.1 + 1.8 * wave) * 10).rounded() / 10 }
            case .weight: if i % 3 == 0 { out[key] = ((68.4 - 0.04 * Double(i) + 0.3 * wave) * 10).rounded() / 10 }
            }
        }
        return out
    }

    func body(content: Content) -> some View {
        content.sheet(isPresented: $shown) {
            NavigationStack {
                ScrollView {
                    HealthTrendsSection(model: HealthTrendsModel(deps: Self.deps)).padding(Theme.Space.page)
                }
                .themedGroupedPage()
                .navigationTitle("健康")
            }
        }
    }
}
#endif
