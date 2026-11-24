package persona.core.domain.tasks

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.Session
import persona.core.contracts.Todo
import persona.core.contracts.TodoAgent
import java.time.DayOfWeek
import java.time.LocalDateTime
import java.time.ZoneId

/**
 * The To-dos list's filter by due date and its sort inside each section (#3740, docs/assistant/todos.md "List filter
 * and sort"): the cases of iOS TodoListFilterTests. Wednesday 2026-11-11, noon in Shanghai; weeks run Monday to Sunday,
 * as the calendar's do in Chinese.
 */
class TodoListFilterTest {
    private val zone = ZoneId.of("Asia/Shanghai")
    private val monday = DayOfWeek.MONDAY
    private val now = LocalDateTime.of(2026, 11, 11, 12, 0).atZone(zone).toInstant()
    private fun at(day: Int, hour: Int = 9, month: Int = 11) = LocalDateTime.of(2026, month, day, hour, 0).atZone(zone).toInstant().toString()
    private val doneAt = now.minusSeconds(3600).toString()

    private fun todo(id: String, day: String? = null, at: String? = null, done: Boolean = false, priority: String? = null) =
        Todo(id, title = id, dueDate = day, dueAt = at, doneAt = if (done) doneAt else null, priority = priority)

    private fun shown(todos: List<Todo>, filter: TodoListFilter, first: DayOfWeek = monday) =
        todos.filter { it.matches(filter, now, zone, first) }.map { it.id }

    private val list = listOf(
        todo("none"),
        todo("today-day", day = "2026-11-11"),
        todo("today-evening", at = at(11, 18)),
        todo("passed-today", at = at(11, 9)), // earlier today, open: overdue
        todo("sunday", day = "2026-11-15"), // the week's last day
        todo("next-monday", day = "2026-11-16"), // next week, this month
        todo("last-of-month", day = "2026-11-30"),
        todo("december", day = "2026-12-01"),
        todo("overdue-open", day = "2026-11-07"),
        todo("overdue-done", day = "2026-11-07", done = true),
        todo("monday-done", day = "2026-11-09", done = true), // this week, already done
        todo("bad-day", day = "soon"), // unreadable: no due date
    )

    @Test fun allShowsEverything() = assertEquals(list.size, shown(list, TodoListFilter.All).size)

    @Test fun noDueDateIsTheOnesWithoutOne() = assertEquals(listOf("none", "bad-day"), shown(list, TodoListFilter.NoDue))

    // Today, This week and This month also take open to-dos already overdue, never done ones that were.
    @Test fun timeFiltersTakeOpenOverdueToDos() {
        assertEquals(listOf("today-day", "today-evening", "passed-today", "overdue-open"), shown(list, TodoListFilter.Today))
        assertEquals(listOf("today-day", "today-evening", "passed-today", "sunday", "overdue-open", "monday-done"), shown(list, TodoListFilter.Week))
        assertEquals(
            listOf("today-day", "today-evening", "passed-today", "sunday", "next-monday", "last-of-month", "overdue-open", "overdue-done", "monday-done"),
            shown(list, TodoListFilter.Month),
        )
    }

    // The week is the calendar's: with weeks starting on Sunday, Sunday the 15th is next week's.
    @Test fun theWeekFollowsTheCalendarsFirstDay() {
        val sunday = todo("sunday", day = "2026-11-15")
        val monday = todo("monday", day = "2026-11-09")
        assertFalse(sunday.matches(TodoListFilter.Week, now, zone, DayOfWeek.SUNDAY))
        assertTrue(monday.matches(TodoListFilter.Week, now, zone, DayOfWeek.SUNDAY))
        assertTrue(sunday.matches(TodoListFilter.Week, now, zone, DayOfWeek.MONDAY))
    }

    @Test fun countsPerFilter() {
        val c = filterCounts(list, now, zone, monday)
        assertEquals(mapOf(TodoListFilter.All to 12, TodoListFilter.NoDue to 2, TodoListFilter.Today to 4, TodoListFilter.Week to 6, TodoListFilter.Month to 9), c)
    }

    private fun group(vararg members: Todo) =
        TriageGroup(members[0].id, TriageSection.Other, members[0], members.toList(), emptyMap(), 0)

    // Recent keeps the list's order; Due puts the earliest first and those without one last; Priority goes high,
    // normal, low, then by due date; ties keep the list's order.
    @Test fun sortsInsideASection() {
        val rows = listOf(
            group(todo("a", priority = "low")),
            group(todo("b", day = "2026-11-20")),
            group(todo("c", day = "2026-11-12", priority = "high")),
            group(todo("d")),
            group(todo("e", day = "2026-11-12")),
        )
        fun ids(s: TodoListSort) = sortedRows(rows, s, zone).map { it.key }
        assertEquals(listOf("a", "b", "c", "d", "e"), ids(TodoListSort.Recent))
        assertEquals(listOf("c", "e", "b", "a", "d"), ids(TodoListSort.Due))
        assertEquals(listOf("c", "e", "b", "d", "a"), ids(TodoListSort.Priority))
    }

    // A merged row sorts by its earliest due and its highest priority member.
    @Test fun aMergedRowSortsByItsBestMember() {
        val rows = listOf(
            group(todo("lead"), todo("member", day = "2026-11-12", priority = "high")),
            group(todo("other", day = "2026-11-13")),
        )
        assertEquals(listOf("lead", "other"), sortedRows(rows, TodoListSort.Due, zone).map { it.key })
        assertEquals(listOf("lead", "other"), sortedRows(rows, TodoListSort.Priority, zone).map { it.key })
    }

    // What the device keeps reads back; anything else is the default.
    @Test fun keptChoicesReadBack() {
        for (f in TodoListFilter.entries) assertEquals(f, TodoListFilter.of(f.key))
        for (s in TodoListSort.entries) assertEquals(s, TodoListSort.of(s.key))
        assertEquals(TodoListFilter.All, TodoListFilter.of(null))
        assertEquals(TodoListSort.Recent, TodoListSort.of("later"))
    }

    // With a filter, Needs you is filtered too; the line over the list counts the waiting ones it leaves out.
    @Test fun theWaitingOnesAFilterLeavesOut() {
        val asks = Session(machineId = "m", sessionId = "ask", state = "needs_you", updatedAt = now.minusSeconds(600).toString())
        val lookup = { key: persona.core.contracts.SessionKey -> asks.takeIf { it.key == key } }
        fun waiting(id: String, day: String?) = Todo(id, title = id, dueDate = day, updatedAt = now.minusSeconds(600).toString(),
            agents = listOf(TodoAgent(machineId = "m", sessionId = "ask")))
        val todos = list + waiting("asks-today", "2026-11-11") + waiting("asks-later", "2026-12-20") + waiting("asks-undated", null)
        val shown = todos.filter { it.matches(TodoListFilter.Today, now, zone, monday) }
        assertEquals(2, hiddenWaiting(todos, shown, lookup, now.toEpochMilli(), zone = zone))
        assertEquals(0, hiddenWaiting(todos, todos, lookup, now.toEpochMilli(), zone = zone))
        // and a section counts the to-dos listed under it, related ones merged into a row included
        val result = triageTodos(shown, lookup, now.toEpochMilli(), zone = zone)
        assertEquals(1, result.listed(TriageSection.Waiting))
    }

    /** A failed start (#3713) whose finished work a filter or a search leaves out is still superseded (decided on every
     *  to-do): not back in Needs you, not in the line about those the filter leaves out. The case of iOS and the web. */
    @Test fun aFilterNeverPutsASupersededFailedStartBackInNeedsYou() {
        val failed = todo("failed", day = "2026-11-11", done = true).copy(planId = "p", startError = "no launcher") // due today
        val finished = todo("finished", done = true).copy(planId = "p") // its plan's work, no due date: left out
        val hiddenFailed = todo("hidden-failed", done = true).copy(planId = "q", startError = "no launcher")
        val hiddenFinished = todo("hidden-finished", done = true).copy(planId = "q")
        val waiting = todo("waiting", done = true).copy(startError = "no launcher") // nothing finished beside it: waits
        val all = listOf(failed, finished, hiddenFailed, hiddenFinished, waiting)
        val superseded = supersededFailedStarts(all)
        val nowMs = now.toEpochMilli()
        val lookup = { _: persona.core.contracts.SessionKey -> null as Session? }

        val today = all.filter { it.matches(TodoListFilter.Today, now, zone, monday) }
        val r = triageTodos(today, lookup, nowMs, zone = zone, known = superseded)
        assertEquals(0, r.listed(TriageSection.Waiting))
        assertEquals(listOf("failed"), r.rows(TriageSection.Done).flatMap { g -> g.members.map { it.id } })
        assertEquals(1, hiddenWaiting(all, today, lookup, nowMs, zone = zone, superseded = superseded))

        val searched = triageTodos(listOf(failed, waiting), lookup, nowMs, zone = zone, known = superseded)
        assertEquals(listOf("waiting"), searched.rows(TriageSection.Waiting).flatMap { g -> g.members.map { it.id } })
    }
}
