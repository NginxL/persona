import SwiftUI
import WidgetKit

struct WatchWaterComplication: Widget {
    var body: some WidgetConfiguration { WatchIntakeComplication(kind: .water).body }
}

struct WatchProteinComplication: Widget {
    var body: some WidgetConfiguration { WatchIntakeComplication(kind: .protein).body }
}

private struct WatchIntakeComplication {
    var kind: WatchIntakeKind
    var body: some WidgetConfiguration {
        AppIntentConfiguration(kind: kind.widgetKind, intent: WatchIntakeConfiguration.self, provider: WatchIntakeProvider()) { entry in
            WatchIntakeFamilyView(entry: entry, kind: kind)
                .containerBackground(for: .widget) { ExtensionSurfaceTokens.surface }
        }
        .configurationDisplayName(kind.title)
        .description("Today's intake recorded in Persona [Watch Widgets]")
        .supportedFamilies(WatchIntakeKind.families)
    }
}

struct WatchIntakeProvider: AppIntentTimelineProvider {
    typealias Intent = WatchIntakeConfiguration
    typealias Entry = WatchIntakeEntry

    nonisolated func placeholder(in context: Context) -> Entry { .init(date: .now) }
    nonisolated func snapshot(for configuration: Intent, in context: Context) async -> Entry {
        await WatchIntakeTimeline.cached(group: configuration.hub?.group)
    }
    nonisolated func timeline(for configuration: Intent, in context: Context) async -> Timeline<Entry> {
        WatchIntakeTimeline.timeline(await WatchIntakeTimeline.load(group: configuration.hub?.group))
    }
    nonisolated func recommendations() -> [AppIntentRecommendation<Intent>] {
        ExtensionSurfaceStore.shared.read().groups.sorted { $0.key < $1.key }.map {
            let hub = WatchIntakeHub(group: $0.key, name: $0.value.credential.name)
            return AppIntentRecommendation(intent: Intent(hub: hub), description: Text(verbatim: hub.name))
        }
    }
}

private struct WatchIntakeFamilyView: View {
    @Environment(\.widgetFamily) private var family
    var entry: WatchIntakeEntry
    var kind: WatchIntakeKind
    var body: some View { WatchIntakeComplicationView(entry: entry, kind: kind, family: family) }
}
