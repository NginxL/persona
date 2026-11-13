import SwiftUI

/// One todo's stage as a bar: Plan → Develop → Review → Merge → Deploy → Verified, with the step it is on marked (#2776). The
/// hub sends the stage on the todo (`TodoItem.stage`); a todo without one, or with a name this build does not know,
/// draws nothing, so the page only shows this when `TodoItem.todoStage` is set. The steps before the current one are
/// done, the ones after it are still ahead — the same reading as the stage bar in the Feishu report (#2768).
///
/// The interface stops at xxxLarge (`docs/design/DESIGN_SYSTEM.md`, "Dynamic Type"), so the six names always fit their
/// sixth of the width; the AX-size layout (dots without names, a "Step 2 of 6" line) is gone.
struct TodoStageBar: View {
    var stage: TodoStage

    /// The current step's place among the six, from zero.
    private var current: Int { stage.stepNumber - 1 }

    var body: some View {
        let steps = TodoStage.allCases
        HStack(spacing: .zero) {
            ForEach(steps.indices, id: \.self) { index in
                VStack(spacing: Theme.Space.xs) {
                    HStack(spacing: .zero) {
                        connector(reached: index <= current, shown: index > 0)
                        dot(at: index)
                        connector(reached: index < current, shown: index < steps.count - 1)
                    }
                    Text(steps[index].label)
                        .font(index == current ? Theme.Font.captionStrong : Theme.Font.caption)
                        .foregroundStyle(index == current ? Theme.accent : (index < current ? Color.primary : Color.secondary))
                        .lineLimit(1)
                        .minimumScaleFactor(0.7)
                        .multilineTextAlignment(.center)
                }
                .frame(maxWidth: .infinity)
            }
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("Stage: \(stage.label) (step \(stage.stepNumber) of \(TodoStage.stepCount))")
        .accessibilityIdentifier("todo.stage")
    }

    /// The line under the dots at the accessibility sizes: the current step and where it is among the six. Each
    /// language's value has a no-break space before the "·", so a wrapped line keeps it at the end of the first line.
    nonisolated static func summary(_ stage: TodoStage) -> String {
        String(localized: "Step \(stage.stepNumber) of \(TodoStage.stepCount) · \(stage.label) [Todo Stage Bar]")
    }

    /// What VoiceOver says for one dot at the accessibility sizes: its name, its place, and whether it is done, the
    /// current step or still ahead of `current`.
    nonisolated static func dotLabel(_ step: TodoStage, current: TodoStage) -> String {
        let n = step.stepNumber, of = TodoStage.stepCount
        if n < current.stepNumber { return String(localized: "\(step.label), step \(n) of \(of), done [Todo Stage Bar]") }
        if n == current.stepNumber { return String(localized: "\(step.label), step \(n) of \(of), current step [Todo Stage Bar]") }
        return String(localized: "\(step.label), step \(n) of \(of), still ahead [Todo Stage Bar]")
    }

    /// The line from a step to its neighbour: colored once the neighbour is reached. The first step's line to its left
    /// and the last step's to its right stay invisible (they keep the dots evenly spaced in their columns).
    private func connector(reached: Bool, shown: Bool) -> some View {
        Rectangle()
            .fill(reached ? Theme.positive.opacity(0.5) : Theme.Fill.pressed)
            .frame(height: Theme.Size.hairline)
            .frame(maxWidth: .infinity)
            .opacity(shown ? 1 : 0)
    }

    /// A passed step is a green tick, the current one the accent color with a ring around it, the ones ahead hollow.
    @ViewBuilder private func dot(at index: Int) -> some View {
        if index < current {
            ZStack {
                Circle().fill(Theme.positive)
                ThemedIcon(Theme.Icon.done)
                    .font(Theme.Font.labelStrong)
                    .dynamicTypeSize(...DynamicTypeSize.xLarge)
                    .foregroundStyle(Theme.onSolid)
            }
            .scaledFrame(width: Theme.Size.markM, height: Theme.Size.markM)
        } else if index == current {
            Circle()
                .fill(Theme.accent)
                .scaledFrame(width: Theme.Size.markM, height: Theme.Size.markM)
                .overlay {
                    Circle()
                        .strokeBorder(Theme.accent.opacity(0.35), lineWidth: Theme.Size.rule)
                        .scaledFrame(width: Theme.Size.badge, height: Theme.Size.badge)
                }
        } else {
            Circle()
                .strokeBorder(Theme.Fill.pressed, lineWidth: Theme.Size.line)
                .scaledFrame(width: Theme.Size.markM, height: Theme.Size.markM)
        }
    }
}
