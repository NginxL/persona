package persona.feature.chat

import persona.core.contracts.BackgroundTask
import persona.core.contracts.Session
import persona.core.contracts.Workflow
import persona.core.contracts.parseTime
import java.time.Duration
import java.time.Instant

/** The panel uses distinct identities for workflows and the commands beside them. */
internal sealed interface BackgroundDestination {
    data object List : BackgroundDestination
    data object LatestWorkflow : BackgroundDestination
    data class Run(val id: String) : BackgroundDestination
    data class Command(val id: String, val initial: BackgroundTask) : BackgroundDestination
}

internal enum class BackgroundOutcome { Running, Completed, Failed, Stopped, Unknown }

internal data class BackgroundCommand(val task: BackgroundTask, val outcome: BackgroundOutcome) {
    val running: Boolean get() = outcome == BackgroundOutcome.Running
    /** Shell commands and Monitors are useful to inspect before the first line or output path arrives. */
    val opens: Boolean get() = task.kind in setOf("shell", "monitor") || !task.outputFile.isNullOrBlank()

    fun elapsed(now: Instant): Long? {
        val start = parseTime(task.startedAt) ?: return null
        val end = if (running) now else parseTime(task.endedAt) ?: return null
        return Duration.between(start, end).seconds.coerceAtLeast(0)
    }

    companion object {
        fun retained(task: BackgroundTask) = BackgroundCommand(task, when (task.status) {
            "completed" -> BackgroundOutcome.Completed
            "failed" -> BackgroundOutcome.Failed
            "killed", "stopped" -> BackgroundOutcome.Stopped
            else -> BackgroundOutcome.Unknown
        })
    }
}

internal data class BackgroundSections(
    val runningRuns: List<Workflow>,
    val runningCommands: List<BackgroundCommand>,
    val completedRuns: List<Workflow>,
    val completedCommands: List<BackgroundCommand>,
) {
    val runningCount get() = runningRuns.size + runningCommands.size
    val latestWorkflow get() = (runningRuns + completedRuns)
        .maxByOrNull { parseTime(it.startedAt) ?: Instant.EPOCH }

    fun workflow(id: String): Workflow? = (runningRuns + completedRuns).firstOrNull { it.backgroundId() == id }

    /** A compact list removing a command is not an outcome. Freeze its duration and keep what was already read. */
    fun command(id: String, previous: BackgroundTask): BackgroundCommand =
        completedCommands.firstOrNull { it.task.id == id } ?: runningCommands.firstOrNull { it.task.id == id }
        ?: BackgroundCommand(previous, BackgroundOutcome.Unknown)
}

internal fun Workflow.backgroundId(): String = runId?.takeIf(String::isNotBlank) ?: taskId

internal fun backgroundSections(session: Session, now: Instant): BackgroundSections {
    val cut = now.minus(Duration.ofDays(1))
    fun ended(run: Workflow): Instant? = parseTime(run.endedAt) ?: parseTime(run.updatedAt) ?: parseTime(run.startedAt)
    val runs = session.workflows.orEmpty().map { run ->
        if (run.status == "unknown" && run.running) run.copy(status = "running") else run
    }.filter {
        it.backgroundId().isNotBlank() && (it.isRunning || ended(it)?.isBefore(cut) != true)
    }.distinctBy(Workflow::backgroundId)
    val completed = session.backgroundEnded.orEmpty().filter { task ->
        task.id.isNotBlank() && parseTime(task.endedAt)?.isBefore(cut) != true
    }.sortedByDescending { parseTime(it.endedAt) ?: Instant.EPOCH }.distinctBy(BackgroundTask::id)
    val finishedIds = completed.mapTo(hashSetOf(), BackgroundTask::id)
    return BackgroundSections(
        runningRuns = runs.filter(Workflow::isRunning).sortedBy { parseTime(it.startedAt) ?: Instant.EPOCH },
        runningCommands = session.backgroundTasks
            .filter { it.id.isNotBlank() && it.id !in finishedIds }.distinctBy(BackgroundTask::id)
            .map { BackgroundCommand(it, BackgroundOutcome.Running) },
        completedRuns = runs.filterNot(Workflow::isRunning).sortedByDescending { ended(it) ?: Instant.EPOCH },
        completedCommands = completed.map(BackgroundCommand::retained),
    )
}

/** Keep the Activity entry useful before the first whole row, without double-counting workflow links. */
internal fun backgroundRunningCount(session: Session, now: Instant): Int {
    if (session.waiting == null) return backgroundSections(session, now).runningCount
    return maxOf(session.backgroundCountHint ?: 0,
        (session.backgroundTasksCount ?: 0) + (session.runningWorkflowsCount ?: 0))
}
