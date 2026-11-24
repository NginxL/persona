package persona.core.domain.tasks

import persona.core.contracts.Todo

// Where a task's finished work is held short of live (#3306, docs/assistant/todos.md"Delivery"): stopped at a PR, merged but not
// live, or live but off by default; with what holds it and who can unhold it. iOS `TodoHold`.

enum class HoldStep { Pr, Merged, Off }

/** Who can unhold it. [Supervisor] (#13191): an owner hold an Agent reported on a supervised task that its supervisor has not
 *  read yet; it is not the owner's until the supervisor opens a decision. Only [User] is the owner's. */
enum class HoldUnblock { User, Agent, Other, Supervisor }

data class TodoHold(val step: HoldStep, val gate: String?, val unblock: HoldUnblock?)

/** The hold while the to-do is open; null once done, live, with nothing to ship, or from a hub that does not judge it. */
val Todo.hold: TodoHold?
    get() {
        if (isDone) return null
        val d = delivery ?: return null
        val step = when (d.state) {
            "pr" -> HoldStep.Pr
            "merged" -> HoldStep.Merged
            "off" -> HoldStep.Off
            else -> return null
        }
        val unblock = when (d.unblock) {
            "user" -> HoldUnblock.User
            "agent" -> HoldUnblock.Agent
            "other" -> HoldUnblock.Other
            "supervisor" -> HoldUnblock.Supervisor
            else -> null
        }
        return TodoHold(step, d.gate?.trim()?.takeIf(String::isNotEmpty), unblock)
    }
