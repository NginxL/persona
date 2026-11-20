#if canImport(AlarmKit)
import ActivityKit
import AlarmKit
import AppIntents
import SwiftUI
import WidgetKit

/// Draws the alarms and timers Persona sets through AlarmKit (#2278, docs/apple/live-activity.md "Alarms and timers"):
/// AlarmKit starts and ends the activity itself; this only draws it. A running timer counts down on the Lock Screen and in
/// the Dynamic Island with Pause and Stop; a paused one shows the time left with Resume and Stop; a ringing one its label
/// with Stop. The countdown is the system's (`Text(timerInterval:)`), so it needs no updates. The tint is the one the app
/// gave the alarm (`AlarmAttributes.tintColor`): the extension builds neither the app's Theme nor its style.
@available(iOS 26.0, *)
struct AlarmLiveActivity: Widget {
    var body: some WidgetConfiguration {
        ActivityConfiguration(for: AlarmAttributes<PersonaAlarmData>.self) { context in
            AlarmLockScreen(state: context.state, attributes: context.attributes)
                .dynamicTypeSize(...DynamicTypeSize.large)
                .activitySystemActionForegroundColor(context.attributes.tintColor)
                .environment(\.locale, LanguageStore.drawingLocale)
        } dynamicIsland: { context in
            let state = context.state
            let attributes = context.attributes
            return DynamicIsland {
                DynamicIslandExpandedRegion(.leading) {
                    AlarmMark(mode: state.mode, tint: attributes.tintColor)
                        .font(.title2)
                        .padding(.leading, IslandMetrics.edge)
                }
                DynamicIslandExpandedRegion(.trailing) {
                    AlarmTime(mode: state.mode)
                        .font(.title3.weight(.semibold))
                        .foregroundStyle(attributes.tintColor)
                        .frame(minWidth: IslandMetrics.alarmExpandedTimerWidth, alignment: .trailing)
                        .padding(.trailing, IslandMetrics.edge)
                }
                DynamicIslandExpandedRegion(.bottom) {
                    VStack(alignment: .leading, spacing: IslandMetrics.rowGap) {
                        AlarmTitle(attributes: attributes).font(.headline).lineLimit(1)
                            .padding(.horizontal, IslandMetrics.gap)
                        AlarmButtons(state: state, attributes: attributes, onDark: true)
                            .padding(.horizontal, IslandMetrics.alarmIslandButtonsInset)
                    }
                    .environment(\.locale, LanguageStore.drawingLocale)
                }
            } compactLeading: {
                IslandWidth { _ in
                    AlarmMark(mode: state.mode, tint: attributes.tintColor)
                        .font(.system(size: IslandMetrics.timerNumber, weight: .semibold))
                        .padding(.leading, IslandMetrics.edge)
                }
            } compactTrailing: {
                IslandWidth { limited in
                    if !limited {
                        AlarmTime(mode: state.mode)
                            .font(.system(size: IslandMetrics.timerNumber, weight: .semibold))
                            .foregroundStyle(attributes.tintColor)
                            // `Text(timerInterval:)` takes all the width it is offered.
                            .frame(width: IslandMetrics.alarmTimerWidth, alignment: .trailing)
                            .padding(.trailing, IslandMetrics.edge)
                    }
                }
            } minimal: {
                AlarmMark(mode: state.mode, tint: attributes.tintColor)
                    .font(.system(size: IslandMetrics.timerNumber, weight: .semibold))
            }
            .keylineTint(attributes.tintColor)
        }
        .supplementalActivityFamilies([.small])
    }
}

/// The alarm's name: the label it was set with, else the alert's title (Alarm / Timer).
@available(iOS 26.0, *)
struct AlarmTitle: View {
    var attributes: AlarmAttributes<PersonaAlarmData>

    var body: some View {
        if let label = attributes.metadata?.label, !label.isEmpty {
            Text(label)
        } else {
            Text(attributes.presentation.alert.title)
        }
    }
}

/// What the alarm is doing: a timer's countdown, paused, or ringing.
@available(iOS 26.0, *)
struct AlarmMark: View {
    var mode: AlarmPresentationState.Mode
    var tint: Color

    var body: some View {
        Image(systemName: symbol)
            .foregroundStyle(tint)
            .accessibilityHidden(true)
    }

    private var symbol: String {
        switch mode {
        case .countdown: AlarmSymbol.timer
        case .paused: AlarmSymbol.pause
        case .alert: AlarmSymbol.alarm
        @unknown default: AlarmSymbol.alarm
        }
    }
}

/// The time: left on a running timer (counting down by itself), left on a paused one, or the time an alarm rings at.
@available(iOS 26.0, *)
struct AlarmTime: View {
    var mode: AlarmPresentationState.Mode

    var body: some View {
        switch mode {
        case .countdown(let countdown):
            Text(timerInterval: AlarmActivityTime.countdown(start: countdown.startDate, fire: countdown.fireDate), countsDown: true)
                .monospacedDigit()
                .multilineTextAlignment(.trailing)
                .lineLimit(1)
        case .paused(let paused):
            Text(verbatim: AlarmActivityTime.paused(total: paused.totalCountdownDuration, elapsed: paused.previouslyElapsedDuration))
                .monospacedDigit()
                .lineLimit(1)
        case .alert(let alert):
            Text(verbatim: String(format: "%02d:%02d", alert.time.hour, alert.time.minute))
                .monospacedDigit()
                .lineLimit(1)
        @unknown default:
            EmptyView()
        }
    }
}

/// Pause or Resume (a timer that has them), and Stop.
@available(iOS 26.0, *)
struct AlarmButtons: View {
    var state: AlarmPresentationState
    var attributes: AlarmAttributes<PersonaAlarmData>
    var onDark = false

    var body: some View {
        HStack(spacing: IslandMetrics.buttonGap) {
            switch state.mode {
            case .countdown:
                if attributes.presentation.countdown?.pauseButton != nil {
                    Button(intent: PauseAlarmIntent(alarmID: state.alarmID)) { label("Pause [Persona Alarm]", AlarmSymbol.pause) }
                        .buttonStyle(.bordered)
                        .tint(onDark ? .white : .primary)
                }
            case .paused:
                Button(intent: ResumeAlarmIntent(alarmID: state.alarmID)) { label("Resume [Persona Alarm]", AlarmSymbol.resume) }
                    .buttonStyle(.bordered)
                    .tint(onDark ? .white : .primary)
            default:
                EmptyView()
            }
            Button(intent: StopAlarmIntent(alarmID: state.alarmID)) { label("Stop [Persona Alarm]", AlarmSymbol.stop) }
                .buttonStyle(.borderedProminent)
                .tint(attributes.tintColor)
                .foregroundStyle(.white)
        }
        .buttonBorderShape(.capsule)
    }

    private func label(_ text: LocalizedStringKey, _ symbol: String) -> some View {
        Label(text, systemImage: symbol)
            .font(.subheadline.weight(.semibold))
            .lineLimit(1)
            .minimumScaleFactor(0.8)
            .frame(maxWidth: .infinity)
    }
}

/// The Lock Screen: mark, name, what it is doing, the time; under them the buttons. The watch's Smart Stack (`.small`)
/// gets a glance: mark, name, time, no buttons.
@available(iOS 26.0, *)
struct AlarmLockScreen: View {
    var state: AlarmPresentationState
    var attributes: AlarmAttributes<PersonaAlarmData>
    @Environment(\.activityFamily) private var family

    var body: some View {
        if family == .small { glance } else { full }
    }

    private var glance: some View {
        HStack(spacing: IslandMetrics.rowGap) {
            AlarmMark(mode: state.mode, tint: attributes.tintColor).font(.title3)
            VStack(alignment: .leading, spacing: IslandMetrics.smallGap) {
                AlarmTitle(attributes: attributes).font(.footnote.weight(.semibold)).lineLimit(1)
                AlarmTime(mode: state.mode).font(.footnote).foregroundStyle(attributes.tintColor)
            }
            Spacer(minLength: 0)
        }
        .padding(IslandMetrics.rowGap)
    }

    private var full: some View {
        VStack(alignment: .leading, spacing: IslandMetrics.rowGap + IslandMetrics.gap) {
            HStack(spacing: IslandMetrics.rowGap + IslandMetrics.gap) {
                AlarmMark(mode: state.mode, tint: attributes.tintColor).font(.title)
                VStack(alignment: .leading, spacing: IslandMetrics.smallGap) {
                    AlarmTitle(attributes: attributes).font(.headline).lineLimit(1)
                    subtitle.font(.caption.weight(.medium)).foregroundStyle(.secondary).lineLimit(1)
                }
                Spacer(minLength: IslandMetrics.gap)
                AlarmTime(mode: state.mode)
                    .font(.title2.weight(.semibold))
                    .foregroundStyle(attributes.tintColor)
                    // It takes all the width it is offered: fixed, the name keeps the rest of the row.
                    .frame(width: IslandMetrics.alarmLockTimerWidth, alignment: .trailing)
            }
            AlarmButtons(state: state, attributes: attributes)
        }
        .padding(IslandMetrics.lockPadding)
    }

    private var subtitle: Text {
        switch state.mode {
        case .countdown: Text("Timer [Persona Alarm]")
        case .paused: Text("Paused [Persona Alarm]")
        default: Text("Alarm [Persona Alarm]")
        }
    }
}

/// The activity's symbols (the extension doesn't build `Theme.Icon`): the app's own for the same meanings.
enum AlarmSymbol {
    /// `Theme.Icon.alarmClock`.
    static let alarm = "alarm"
    /// A timer counting down, as the Clock app draws one (the app has no timer of its own to show).
    static let timer = "timer"
    /// `Theme.Icon.pause`.
    static let pause = "pause.fill"
    /// `Theme.Icon.run`.
    static let resume = "play.fill"
    /// `Theme.Icon.interrupt`.
    static let stop = "stop.fill"
}
#endif
