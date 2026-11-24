#if os(iOS)
import SwiftUI

struct HealthActivitySection: View {
    @State private var model: HealthActivityModel

    init(model: HealthActivityModel = HealthActivityModel()) { _model = State(initialValue: model) }

    var body: some View {
        if model.state != .unavailable {
            Section {
                content
            } header: {
                Label { Text("Activity [Health Home]") } icon: { ThemedIcon(Theme.Icon.fitness) }
            }
            .tabActiveTask { await model.load() }
        }
    }

    @ViewBuilder private var content: some View {
        switch model.state {
        case .loaded(let reading): ActivityRings(reading: reading)
        case .loading: ProgressView().frame(maxWidth: .infinity)
        case .needsAccess:
            VStack(alignment: .leading, spacing: Theme.Space.s) {
                Text("Allow reading your daily Activity summary to show your rings here.")
                    .font(Theme.Font.secondary).foregroundStyle(.secondary)
                Button { Task { await model.requestAccess() } } label: {
                    Text("Allow Reading").lineLimit(1).scaledFrame(minHeight: Theme.Size.tapTarget)
                }
                    .themedButton()
                    .disabled(model.requesting)
                    .accessibilityIdentifier("health-activity-allow")
            }
        case .noData, .failed:
            VStack(alignment: .leading, spacing: Theme.Space.s) {
                if model.state == .failed {
                    Text("Couldn't read today's Activity summary.")
                } else {
                    Text("No Activity summary could be read today. There may be no data, or reading may not be allowed.")
                }
                Text("To allow reading, open iOS Settings › Health › Data Access & Devices › Persona.")
            }
            .font(Theme.Font.secondary).foregroundStyle(.secondary)
            .accessibilityIdentifier("health-activity-unreadable")
        case .unavailable: EmptyView()
        }
    }
}
#endif
