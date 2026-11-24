import SwiftUI

/// A failed start's next steps in web order: another launcher first when offered, ordinary Retry, then Remove.
struct TaskRetryButtons: View {
    @Environment(AppModel.self) private var model
    var todo: TodoItem
    var idPrefix = "task"
    var remove: (() -> Void)? = nil
    private var store: TasksStore { .shared }

    var body: some View {
        let instead = store.retryLauncher(todo, features: model.hubFeatures)
        let busy = store.busyRetries.contains(todo.id)
        VStack(spacing: Theme.Space.s) {
            if let instead {
                // The new alternative gets its own measured row; the ordinary Retry / Remove row keeps its old layout.
                StackingButtonRow(spacing: Theme.Space.s) {
                    Button { Task { await store.retry(todo, launcher: instead, features: model.hubFeatures) } } label: {
                        Text("Use \(instead) instead").buttonLabelLine(minimumScale: 0.65)
                            .opacity(busy ? 0 : 1).overlay { if busy { ProgressView().controlSize(.small) } }
                    }
                    .themedButton(prominent: true)
                    .disabled(busy)
                    .accessibilityIdentifier("\(idPrefix).retry-launcher")
                }
            }
            StackingButtonRow(spacing: Theme.Space.s) {
                Button { Task { await store.retry(todo, features: model.hubFeatures) } } label: {
                    Text("Retry [Components b8784c]").buttonLabelLine()
                        .opacity(busy ? 0 : 1).overlay { if busy { ProgressView().controlSize(.small) } }
                }
                .themedButton(prominent: instead == nil)
                .disabled(busy)
                .accessibilityIdentifier("\(idPrefix).retry")
                if let remove {
                    Button(action: remove) {
                        Text("Remove [Task Sheets 6135d4]").buttonLabelLine()
                    }
                    .themedButton()
                    .accessibilityIdentifier("\(idPrefix).remove")
                }
            }
        }
        .font(Theme.Font.captionStrong)
    }
}
