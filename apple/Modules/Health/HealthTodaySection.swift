import SwiftUI

/// Today's hub totals. Entry uses the shared short-text alert and reversible-action toast.
struct HealthTodaySection: View {
    let page: HealthModel
    var openDetail: (HealthIntake.Kind) -> Void
    @State private var asking = false
    @State private var kind = HealthIntake.Kind.water
    @State private var amount = ""

    private var enteredAmount: Double? {
        guard let value = Double(amount.replacingOccurrences(of: ",", with: ".")), value.isFinite,
              value >= (kind == .water ? 1 : 0.1), value <= (kind == .water ? 3000 : 200),
              abs(value * 10 - (value * 10).rounded()) < 0.000_001 else { return nil }
        return value
    }

    var body: some View {
        Group {
            if !page.supportsIntake {
                Text("Update your hub first")
                    .font(Theme.Font.secondary).foregroundStyle(.secondary)
                    .accessibilityIdentifier("health-update-hub")
            } else if let intake = page.intake {
                HStack(alignment: .top, spacing: Theme.Space.s) {
                    VStack(spacing: Theme.Space.s) {
                        ring(.water, portion: intake.water)
                        waterActions
                    }
                    .frame(maxWidth: .infinity)
                    VStack(spacing: Theme.Space.s) {
                        ring(.protein, portion: intake.protein)
                        action("Log Grams", id: "health-log-protein") { ask(.protein) }
                    }
                    .frame(maxWidth: .infinity)
                }
                .font(Theme.Font.label)
                .disabled(page.intakeBusy)
                .accessibilityElement(children: .contain)
                .accessibilityIdentifier("health-today")
            } else if let error = page.intakeError {
                Text("Couldn't load intake: \(error)")
                    .font(Theme.Font.secondary).foregroundStyle(.secondary)
            } else {
                ProgressView().frame(maxWidth: .infinity)
            }
        }
        .alert(kind == .water ? String(localized: "Water amount (ml)") : String(localized: "Protein amount (g)"), isPresented: $asking) {
            TextField(kind == .water ? String(localized: "ml") : String(localized: "g"), text: $amount)
                #if os(iOS) || os(visionOS)
                .keyboardType(.decimalPad)
                #endif
                .accessibilityIdentifier("health-intake-amount")
            Button("Log") {
                guard let value = enteredAmount else { return }
                Task { await page.record(kind, amount: value) }
            }
            .disabled(enteredAmount == nil)
            Button("Cancel", role: .cancel) {}
        } message: {
            Text(kind == .water ? String(localized: "Enter 1–3,000 ml, with at most one decimal place.") :
                    String(localized: "Enter 0.1–200 g, with at most one decimal place."))
        }
    }

    /// The interface stops growing at xxxLarge (DESIGN_SYSTEM.md, "Dynamic Type"), so the two buttons keep one row
    /// at every size and shrink their one-line titles instead of stacking.
    private var waterActions: some View {
        HStack(spacing: Theme.Space.xs) {
            action("Drank a Glass", id: "health-log-water") { Task { await page.record(.water) } }
            action("Other Amount", id: "health-log-water-amount") { ask(.water) }
        }
    }

    private func action(_ title: LocalizedStringKey, id: String, run: @escaping () -> Void) -> some View {
        Button(action: run) {
            Text(title).lineLimit(1).minimumScaleFactor(0.6)
                .frame(maxWidth: .infinity)
                .scaledFrame(minHeight: Theme.Size.tapTarget)
        }
        .themedButton()
        .accessibilityIdentifier(id)
    }

    private func ask(_ value: HealthIntake.Kind) {
        kind = value
        amount = ""
        asking = true
    }

    private func ring(_ kind: HealthIntake.Kind, portion: HealthIntake.Portion) -> some View {
        let value = portion.today.formatted(.number.precision(.fractionLength(0...1)))
        let goal = portion.goal.formatted()
        let unit = kind == .water ? String(localized: "ml") : String(localized: "g")
        let title = kind == .water ? String(localized: "Water") : String(localized: "Protein")
        return Button { openDetail(kind) } label: {
            GaugeRing(title: title, value: value, unit: unit,
                         subtitle: String(localized: "Goal: \(goal) \(unit)"),
                         ratio: portion.today / Double(max(1, portion.goal)),
                         color: kind == .water ? Theme.Health.water : Theme.Health.protein,
                         subtitleLineLimit: nil)
                .accessibilityElement(children: .ignore)
        }
            .buttonStyle(.plain)
            .accessibilityLabel(title)
            .accessibilityValue(String(localized: "\(value) of \(goal) \(unit)"))
            .accessibilityIdentifier(kind == .water ? "health-water-ring" : "health-protein-ring")
    }
}

/// Hosted by the Health page rather than a reusable List row, as the subscriptions sheet is.
struct HealthIntakeSheet: View {
    struct Route: Identifiable {
        var kind: HealthIntake.Kind
        var id: String { kind.rawValue }
    }
    var kind: HealthIntake.Kind
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            ExtensionPageView(plugin: HealthIntakeUI.id, ui: HealthIntakeUI.page,
                              title: kind == .water ? String(localized: "Water") : String(localized: "Protein"),
                              state: HealthIntakeUI.state(kind))
                .toolbar { SheetClose { dismiss() } }
        }
    }
}
