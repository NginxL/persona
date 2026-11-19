package persona.feature.agents

import persona.core.contracts.*
import persona.core.domain.sort.*
import org.junit.Assert.*
import org.junit.Test

/** The "By project" tree (stored as MachineSpace) these tests were written for (the default grouping is now "By repository"). */
private val MachineSpaceTree = AgentListSettings(grouping = AgentGrouping.MachineSpace)

class AgentsTreeTest {
    private fun session(machine: String, id: String, state: String, space: String) =
        AgentSession(Session(machineId = machine, sessionId = id, state = state, spaceId = space, spaceName = space,
            updatedAt = "2026-01-01T00:00:00Z"))

    private val machines = listOf(Machine("mac", "Mac", online = true), Machine("box", "Box", online = true), Machine("nas", "NAS"))
    private val rows = listOf(
        session("mac", "a", "working", "app"), session("mac", "b", "idle", "app"),
        session("mac", "c", "idle", "notes"), session("mac", "d", "idle", "notes"),
        session("box", "e", "needs_you", "api"),
    )

    @Test fun quietSpacesStayUnderTheirComputerAfterTheActiveOnesWithoutATitleOfTheirOwn() {
        val entries = agentTreeEntries(buildAgentList(rows, machines, settings = MachineSpaceTree), "")
        assertEquals(listOf("machine-header:machine/mac", "group:mac/app", "session:mac/a", "idle:mac/app", "group:mac/notes",
            "machine-header:machine/box", "group:box/api", "session:box/e"), entries.map { it.key })
        val notes = entries.filterIsInstance<TreeGroupEntry>().single { it.group.title.shown == "notes" }
        assertTrue(notes.quiet)
        assertTrue("quiet spaces start folded", notes.group.folded)
        assertEquals(2, notes.group.rows.size)
        assertEquals(1, notes.depth)
        assertTrue(entries.filterIsInstance<TreeGroupEntry>().all { it.space })
        assertTrue(entries.filterIsInstance<TreeRowEntry>().none { it.located })
        assertTrue((entries.first() as TreeMachineEntry).first)
        assertFalse(entries.filterIsInstance<TreeMachineEntry>().last().first)
    }

    @Test fun searchingOpensQuietSpacesAndFoldingAComputerLeavesOnlyItsHeading() {
        val searched = agentTreeEntries(buildAgentList(rows, machines, settings = AgentListSettings(grouping = AgentGrouping.MachineSpace, query = "notes")), "")
        // One computer left: no heading over its own list, as iOS (#4338 A7).
        assertEquals(listOf("group:mac/notes", "session:mac/c", "session:mac/d"), searched.map { it.key })
        val rowsOfNotes = searched.filterIsInstance<TreeRowEntry>()
        assertEquals(listOf(true, false), rowsOfNotes.map { it.first })
        assertEquals(listOf(false, true), rowsOfNotes.map { it.last })
        val folded = agentTreeEntries(buildAgentList(rows, machines, settings = AgentListSettings(grouping = AgentGrouping.MachineSpace, folded = setOf("machine/mac"))), "")
        assertEquals(listOf("machine-header:machine/mac", "machine-header:machine/box", "group:box/api", "session:box/e"), folded.map { it.key })
    }

    @Test fun oneComputerSelectedDropsItsHeadingAndLiftsItsSpaces() {
        val entries = agentTreeEntries(buildAgentList(rows, machines, settings = AgentListSettings(grouping = AgentGrouping.MachineSpace, machineFilter = "mac")), "mac")
        assertTrue(entries.none { it is TreeMachineEntry })
        assertEquals(setOf(0), entries.filterIsInstance<TreeGroupEntry>().map { it.depth }.toSet())
        assertEquals(setOf(0), entries.filterIsInstance<TreeRowEntry>().map { it.depth }.toSet())
    }

    @Test fun sectionsOutsideTheTreeSayWhereTheirSessionsRun() {
        val pinned = agentTreeEntries(buildAgentList(rows, machines, pins = AgentPreferences(sessions = listOf("box/e")), settings = MachineSpaceTree), "")
        val first = pinned.first() as TreeGroupEntry
        assertEquals("pinned", first.group.id)
        assertFalse(first.space)
        assertTrue((pinned[1] as TreeRowEntry).located)
        assertEquals(1, pinned.count { it.key == "session:box/e" })
        assertTrue("a computer with only pinned sessions has no empty heading", pinned.none { it.key == "machine-header:machine/box" })
        val byState = agentTreeEntries(buildAgentList(rows, machines, settings = AgentListSettings(grouping = AgentGrouping.State)), "")
        assertTrue(byState.none { it is TreeMachineEntry })
        assertTrue(byState.filterIsInstance<TreeGroupEntry>().none { it.space })
        assertTrue(byState.filterIsInstance<TreeRowEntry>().all { it.located && it.depth == 0 })
    }

    @Test fun everyComputerGetsAChipOnlineFirstWithWhatTheListWouldShow() {
        val shown = buildAgentList(rows, machines, settings = MachineSpaceTree).groups.flatMap { it.rows }
        val chips = machineCounts(listOf(Machine("nas", "NAS")) + machines.filter { it.online }, shown)
        assertEquals(listOf("mac", "box", "nas"), chips.map { it.machine.id })
        assertEquals(listOf(4, 1, 0), chips.map { it.count })
        val searched = buildAgentList(rows, machines, settings = AgentListSettings(grouping = AgentGrouping.MachineSpace, query = "api")).groups.flatMap { it.rows }
        assertEquals(listOf(0, 1, 0), machineCounts(machines, searched).map { it.count })
    }

    @Test fun byTodoGroupingTreeFlattensIntoSectionsWithDetailAction() {
        val todo = Todo(id = "todo_1", title = "待办一", agents = listOf(TodoAgent("mac", "a")))
        val list = buildAgentList(
            rows, machines,
            settings = AgentListSettings(grouping = AgentGrouping.Todo),
            todos = listOf(todo)
        )
        val entries = agentTreeEntries(list, "")
        assertTrue(entries.none { it is TreeMachineEntry })
        val groups = entries.filterIsInstance<TreeGroupEntry>()
        // As iOS (#4338 A5): a section per to-do, then "No to-do", open here (one there needs you)
        assertEquals(listOf("todo/todo_1", "todo/none"), groups.map { it.group.id })
        assertEquals(listOf(0, 0), groups.map { it.depth })
        assertEquals("待办一", groups[0].group.title.shown)
        assertEquals("没有待办", groups[1].group.title.shown)
        val rowsInTodo1 = entries.filterIsInstance<TreeRowEntry>().filter { it.row.session.sessionId == "a" }
        assertEquals(1, rowsInTodo1.size)
        assertEquals(0, rowsInTodo1.single().depth)
        assertTrue("todo rows still say where they run", rowsInTodo1.single().located)
    }

    @Test fun byTodoIsOneSectionPerTodoAndFoldsThem() {
        fun at(id: String, cwd: String, state: String) = AgentSession(Session(machineId = "mac", sessionId = id, state = state, cwd = cwd,
            updatedAt = "2026-01-01T00:00:00Z"))
        val sessions = listOf(at("a", "/code/app", "working"), at("b", "/code/app/.worktrees/fix", "idle"), at("c", "/code/api", "needs_you"),
            at("d", "/code/api", "idle"))
        val todos = listOf(Todo(id = "t1", title = "修登录", agents = listOf(TodoAgent("mac", "a"), TodoAgent("mac", "b"))),
            Todo(id = "t2", title = "改接口", agents = listOf(TodoAgent("mac", "c"))))
        fun keys(folded: Set<String> = emptySet()) = agentTreeEntries(buildAgentList(sessions, machines,
            settings = AgentListSettings(grouping = AgentGrouping.Todo, folded = folded), todos = todos), "").map { it.key }
        // As iOS (#4338 A5): the to-do with an agent waiting for you first, each a flat list whatever repository its agents
        // work in; "No to-do" folded under the to-dos while nothing in it waits.
        assertEquals(listOf("group:todo/t2", "session:mac/c", "group:todo/t1", "session:mac/a", "session:mac/b", "group:todo/none"), keys())
        assertEquals(listOf("group:todo/t2", "group:todo/t1", "session:mac/a", "session:mac/b", "group:todo/none"), keys(setOf("todo/t2")))
    }

    @Test fun byRepositoryOfflineHistoryStaysUnderItsComputerAndFoldsWithIt() {
        fun at(machine: String, id: String, state: String, cwd: String, updatedAt: String) = AgentSession(Session(
            machineId = machine, sessionId = id, state = state, status = state, cwd = cwd, updatedAt = updatedAt))
        val rows = listOf(
            at("mac", "running", "working", "/tmp/example-project", "2026-01-04T00:00:00Z"),
            at("box", "other", "working", "/srv/api", "2026-01-03T00:00:00Z"),
            at("mac", "old", "offline", "/tmp/example-project", "2026-01-02T00:00:00Z"),
            at("box", "old", "offline", "/tmp/example-project", "2026-01-01T00:00:00Z"),
        )
        val computers = listOf(Machine("mac", "Mac", online = true), Machine("box", "Box", online = true))
        val prefs = AgentsPreferencesSnapshot(range = AgentRange.All)
        val settings = AgentListSettings(grouping = AgentGrouping.Repo)
        val projected = scopedAgentList(rows, computers, prefs, settings)
        val treeIds = projected.machines.flatMap { it.groups }.toSet()
        assertTrue(projected.groups.filter { it.id.startsWith("history/") }.all { it.id in treeIds })
        assertEquals(listOf(1, 1), projected.machines.map { it.repoCount })
        val runningTitle = projected.groups.single { it.id == "tree/mac//tmp/example-project" }.title
        val historyTitle = projected.groups.single { it.id == "history/mac//tmp/example-project" }.title
        assertNotEquals(runningTitle.shown, historyTitle.shown)
        assertTrue(historyTitle.shown.contains(CoreText.of(CoreText.Key.StateNotRunning).shown))
        fun keys(range: AgentRange = AgentRange.All, folded: Set<String> = emptySet()) = agentTreeEntries(
            scopedAgentList(rows, computers, prefs.copy(range = range), settings.copy(folded = folded)), "").map { it.key }
        assertEquals(listOf(
            "machine-header:machine/mac", "group:tree/mac//tmp/example-project", "session:mac/running",
            "group:history/mac//tmp/example-project", "session:mac/old",
            "machine-header:machine/box", "group:tree/box//srv/api", "session:box/other",
            "group:history/box//tmp/example-project", "session:box/old"), keys())
        assertEquals(listOf("machine-header:machine/mac", "machine-header:machine/box"),
            keys(folded = setOf("machine/mac", "machine/box")))
        assertEquals(listOf(
            "machine-header:machine/mac", "group:history/mac//tmp/example-project", "session:mac/old",
            "machine-header:machine/box", "group:history/box//tmp/example-project", "session:box/old"), keys(AgentRange.History))
        assertEquals(listOf("machine-header:machine/mac", "machine-header:machine/box"),
            keys(AgentRange.History, setOf("machine/mac", "machine/box")))
        val running = scopedAgentList(rows, computers, prefs.copy(range = AgentRange.Running), settings)
        assertTrue(running.groups.none { it.id.startsWith("history/") })
        assertTrue(running.groups.flatMap { it.rows }.none { it.session.sessionId == "old" })
    }

    @Test fun historyTitlesOutsideRepositoryGroupingStayUnchanged() {
        val rows = listOf(AgentSession(Session(machineId = "mac", sessionId = "old", cwd = "/tmp/example-project",
            state = "offline", status = "offline", updatedAt = "2026-01-01T00:00:00Z")))
        val projected = scopedAgentList(rows, listOf(Machine("mac", "Mac")), AgentsPreferencesSnapshot(range = AgentRange.History),
            AgentListSettings(grouping = AgentGrouping.MachineSpace))
        assertEquals("/tmp/example-project", projected.groups.single().title.shown)
    }

    @Test fun byRepositoryTreesSitAStepInsideTheirRepositoryAndFoldWithIt() {
        fun at(id: String, cwd: String, state: String) = AgentSession(Session(machineId = "mac", sessionId = id, state = state, cwd = cwd,
            updatedAt = "2026-01-01T00:00:00Z"))
        val repoRows = listOf(at("main", "/code/app", "working"), at("fix", "/code/app/.worktrees/fix", "needs_you"),
            at("notes", "/code/notes", "idle"), session("box", "e", "needs_you", "api"))
        val byRepo = AgentListSettings(grouping = AgentGrouping.Repo)
        val entries = agentTreeEntries(buildAgentList(repoRows, machines, settings = byRepo), "")
        assertEquals(listOf("machine-header:machine/mac", "group:repo/mac//code/app", "group:tree/mac//code/app", "session:mac/main",
            "group:tree/mac//code/app/.worktrees/fix", "session:mac/fix", "group:tree/mac//code/notes",
            "machine-header:machine/box", "group:tree/box/", "session:box/e"), entries.map { it.key })
        val depths = entries.filterIsInstance<TreeGroupEntry>().associate { it.group.id to it.depth }
        assertEquals(1, depths["repo/mac//code/app"])
        assertEquals(2, depths["tree/mac//code/app/.worktrees/fix"])
        assertEquals(2, entries.filterIsInstance<TreeRowEntry>().single { it.row.session.sessionId == "fix" }.depth)
        assertTrue(entries.filterIsInstance<TreeGroupEntry>().single { it.group.id == "tree/mac//code/notes" }.quiet)
        assertEquals(1, entries.filterIsInstance<TreeGroupEntry>().single { it.group.id == "repo/mac//code/app" }.group.headingCounts
            .single { it.first == persona.core.designsystem.Status.NeedsYou }.second)
        val folded = agentTreeEntries(buildAgentList(repoRows, machines, settings = byRepo.copy(machineFilter = "mac", folded = setOf("repo/mac//code/app"))), "mac")
        assertEquals(listOf("group:repo/mac//code/app", "group:tree/mac//code/notes"), folded.take(2).map { it.key })
    }

    @Test fun byRepositoryOnGlassPutsEachRepositoryInACardWithItsLinesAndRowsAtTheirLevel() {
        // iOS `repoCard` (#4338 A7): a card per repository, the header level 0, a working tree's line 1 with its agents at
        // its level; a repository with its main checkout alone has its agents a level in under its header.
        fun at(id: String, cwd: String, state: String, machine: String = "mac") = AgentSession(Session(machineId = machine, sessionId = id,
            state = state, cwd = cwd, updatedAt = "2026-01-01T00:00:00Z"))
        val rows = listOf(at("main", "/code/app", "working"), at("fix", "/code/app/.worktrees/fix", "needs_you"),
            at("notes", "/code/notes", "unread"), at("old", "/code/old", "idle"), at("api", "/srv/api", "working", machine = "box"))
        val entries = agentTreeEntries(buildAgentList(rows, machines, settings = AgentListSettings(grouping = AgentGrouping.Repo)), "")
        val slots = repoCardSlots(entries)
        assertEquals(listOf("machine-header:machine/mac", "group:repo/mac//code/app", "group:tree/mac//code/app", "session:mac/main",
            "group:tree/mac//code/app/.worktrees/fix", "session:mac/fix", "group:tree/mac//code/notes", "session:mac/notes",
            "group:tree/mac//code/old", "machine-header:machine/box", "group:tree/box//srv/api", "session:box/api"), entries.map { it.key })
        assertNull("a computer's heading is outside the cards", slots["machine-header:machine/mac"])
        assertEquals(RepoCardSlot(first = true, last = false, level = 0), slots["group:repo/mac//code/app"])
        assertEquals(RepoCardSlot(first = false, last = false, level = 1), slots["group:tree/mac//code/app"])
        assertEquals(RepoCardSlot(first = false, last = false, level = 1), slots["session:mac/main"])
        assertEquals(RepoCardSlot(first = false, last = true, level = 1), slots["session:mac/fix"])
        // The main checkout alone: its agent a level in under the repository's header.
        assertEquals(RepoCardSlot(first = true, last = false, level = 0), slots["group:tree/mac//code/notes"])
        assertEquals(RepoCardSlot(first = false, last = true, level = 1), slots["session:mac/notes"])
        // A quiet repository starts folded: its header is the whole card.
        assertEquals(RepoCardSlot(first = true, last = true, level = 0), slots["group:tree/mac//code/old"])
        assertEquals(RepoCardSlot(first = false, last = true, level = 1), slots["session:box/api"])
        // Folded, a repository's card is its header alone.
        val folded = repoCardSlots(agentTreeEntries(buildAgentList(rows, machines,
            settings = AgentListSettings(grouping = AgentGrouping.Repo, folded = setOf("repo/mac//code/app"))), ""))
        assertEquals(RepoCardSlot(first = true, last = true, level = 0), folded["group:repo/mac//code/app"])
        assertNull(folded["session:mac/main"])
        // Other groupings have no cards.
        assertTrue(repoCardSlots(agentTreeEntries(buildAgentList(rows, machines, settings = MachineSpaceTree), "")).isEmpty())
    }

    @Test fun byRepositoryOnGlassListsEachComputersIdleRepositoriesInOneCardAndFoldsIdleAgentsBehindOneLine() {
        // iOS `hostRepos` and `agentRows` (#4338 A8): a computer's busy repositories, then its Idle Repositories heading with
        // how many, each idle repository a row of one card, its agents a level in once opened; a busy tree's idle agents
        // behind one line, which folds them again once open.
        fun at(id: String, cwd: String, state: String, machine: String = "mac", tab: String? = null) = AgentSession(Session(machineId = machine,
            sessionId = id, state = state, cwd = cwd, slotId = tab, slotName = tab, updatedAt = "2026-01-01T00:00:00Z"))
        val rows = listOf(at("build", "/code/app", "working", tab = "build"), at("review", "/code/app", "idle", tab = "review"),
            at("research", "/code/app", "idle", tab = "research"), at("minutes", "/code/notes", "idle", tab = "minutes"),
            at("cleanup", "/code/scripts", "idle"), at("api", "/srv/api", "working", machine = "box"),
            at("certs", "/srv/infra", "idle", machine = "box"))
        fun entries(settings: AgentListSettings = AgentListSettings(grouping = AgentGrouping.Repo), idleRepos: Boolean = true) =
            agentTreeEntries(buildAgentList(rows, machines, settings = settings), "", idleRepos = idleRepos)
        val glass = entries()
        assertEquals(listOf("machine-header:machine/mac", "group:tree/mac//code/app", "session:mac/build", "idle:tree/mac//code/app",
            "idle-repos:machine/mac", "group:tree/mac//code/notes", "group:tree/mac//code/scripts",
            "machine-header:machine/box", "group:tree/box//srv/api", "session:box/api", "idle-repos:machine/box", "group:tree/box//srv/infra"),
            glass.map { it.key })
        assertEquals(listOf(2, 1), glass.filterIsInstance<TreeIdleReposEntry>().map { it.count })
        val line = glass.filterIsInstance<TreeIdleEntry>().single()
        // Equal times: by id, as the list orders them.
        assertEquals(listOf("research", "review"), line.fold!!.names)
        assertFalse(line.fold!!.open)
        val slots = repoCardSlots(glass)
        assertEquals(RepoCardSlot(first = false, last = true, level = 1), slots["idle:tree/mac//code/app"])
        assertNull("the heading is outside the cards", slots["idle-repos:machine/mac"])
        assertEquals(RepoCardSlot(first = true, last = false, level = 0), slots["group:tree/mac//code/notes"])
        assertEquals(RepoCardSlot(first = false, last = true, level = 0), slots["group:tree/mac//code/scripts"])
        assertEquals(RepoCardSlot(first = true, last = true, level = 0), slots["group:tree/box//srv/infra"])
        // Opened: an idle repository's agents a level in, in the same card; the idle agents after the busy one, then the
        // line that folds them again.
        val opened = entries(AgentListSettings(grouping = AgentGrouping.Repo, expandedQuiet = setOf("tree/mac//code/notes"),
            expandedIdle = setOf("tree/mac//code/app")))
        assertEquals(listOf("session:mac/build", "session:mac/research", "session:mac/review", "idle:tree/mac//code/app"),
            opened.map { it.key }.drop(2).take(4))
        assertTrue(opened.filterIsInstance<TreeIdleEntry>().single().fold!!.open)
        val openSlots = repoCardSlots(opened)
        assertEquals(RepoCardSlot(first = true, last = false, level = 0), openSlots["group:tree/mac//code/notes"])
        assertEquals(RepoCardSlot(first = false, last = false, level = 1), openSlots["session:mac/minutes"])
        assertEquals(RepoCardSlot(first = false, last = true, level = 0), openSlots["group:tree/mac//code/scripts"])
        // Brief and Board (no idleRepos): as before, the idle repositories inline and "N more idle" with no line of their own.
        val plain = entries(idleRepos = false)
        assertTrue(plain.none { it is TreeIdleReposEntry })
        assertNull(plain.filterIsInstance<TreeIdleEntry>().single().fold)
        assertEquals(2, plain.filterIsInstance<TreeIdleEntry>().single().hidden)
        assertEquals(glass.filterNot { it is TreeIdleReposEntry }.map { it.key }, plain.map { it.key })
    }

    @Test fun byProjectHeadingsCountTheAgentsThatOnlyWaitInTheBackgroundApartFromWorking() {
        // iOS `StateCounts` (#4338 A7): Working leaves them out and the heading draws them with a clock, as the computer's does.
        fun at(id: String, state: String, waiting: Boolean = false) = AgentSession(Session(machineId = "mac", sessionId = id, state = state,
            spaceId = "app", spaceName = "app", backgroundWaiting = waiting, updatedAt = "2026-01-01T00:00:00Z"))
        val list = buildAgentList(listOf(at("a", "working"), at("b", "working", waiting = true), at("c", "working", waiting = true),
            at("d", "needs_you"), at("e", "unread")), machines, settings = MachineSpaceTree)
        val space = list.groups.single()
        assertEquals(2, space.waitingInBackground)
        assertEquals(listOf(1, 1, 1, 0), space.headingCounts.map { it.second })
        assertEquals(2, list.machines.single().waitingInBackground)
        assertEquals(listOf(1, 1, 1, 0), list.machines.single().headingCounts.map { it.second })
        // Nothing waits: Working counts them all, as before.
        val plain = buildAgentList(listOf(at("a", "working"), at("b", "working")), machines, settings = MachineSpaceTree).groups.single()
        assertEquals(0, plain.waitingInBackground)
        assertEquals(listOf(0, 2, 0, 0), plain.headingCounts.map { it.second })
        // A paged list takes the hub's count for the space (`SessionListCounts.backgroundWaiting`): Working there includes them.
        val counted = space.copy(totalCount = 4, stateCounts = AgentState.entries.associateWith { if (it == AgentState.Working) 3 else 0 }, waitingInBackground = 2)
        assertEquals(listOf(0, 1, 0, 0), counted.headingCounts.map { it.second })
    }

    @Test fun byMachineHeadingCountsFromRowsWhenStateCountsAreNull() {
        // #9319: shared fixtures leave state_counts null for Machine groups; the feature helper counts from rows.
        fun at(id: String, machine: String, state: String, waiting: Boolean = false) = AgentSession(Session(
            machineId = machine, sessionId = id, state = state, backgroundWaiting = waiting,
            updatedAt = "2026-01-01T00:00:00Z"))
        val list = buildAgentList(
            listOf(
                at("a", "mac", "needs_you"), at("b", "mac", "working"), at("c", "mac", "working", waiting = true),
                at("d", "mac", "unread"), at("e", "mac", "idle"), at("f", "mac", "unknown"),
                at("g", "box", "idle"),
            ),
            machines,
            settings = AgentListSettings(grouping = AgentGrouping.Machine),
        )
        val mac = list.groups.single { it.id == "machine/mac" }
        assertNull(mac.stateCounts)
        assertEquals(1, mac.waitingInBackground)
        assertEquals(
            listOf(persona.core.designsystem.Status.NeedsYou, persona.core.designsystem.Status.Working,
                persona.core.designsystem.Status.Unread, persona.core.designsystem.Status.Idle),
            machineHeadingCounts(mac).map { it.first },
        )
        assertEquals(listOf(1, 1, 1, 2), machineHeadingCounts(mac).map { it.second })
        val box = list.groups.single { it.id == "machine/box" }
        assertNull(box.stateCounts)
        assertEquals(listOf(0, 0, 0, 1), machineHeadingCounts(box).map { it.second })
    }

    @Test fun aSpaceHeadingCountsItsIdleAgentsAfterTheClockAsTheComputerDoes() {
        // iOS `SpaceHeader` draws `StateCounts`, whose last count is the idle agents (Idle and Unknown; the ones that only wait
        // in the background are counted apart): a gray dot and a number (#4338 A5). The computer heading counts the same.
        fun at(id: String, state: String, waiting: Boolean = false) = AgentSession(Session(machineId = "mac", sessionId = id, state = state,
            spaceId = "app", spaceName = "app", backgroundWaiting = waiting, updatedAt = "2026-01-01T00:00:00Z"))
        val list = buildAgentList(listOf(at("a", "needs_you"), at("b", "working"), at("c", "working", waiting = true), at("d", "unread"),
            at("e", "idle"), at("f", "idle"), at("g", "unknown")), machines, settings = MachineSpaceTree)
        val space = list.groups.single()
        assertEquals(listOf(persona.core.designsystem.Status.NeedsYou, persona.core.designsystem.Status.Working, persona.core.designsystem.Status.Unread,
            persona.core.designsystem.Status.Idle), space.headingCounts.map { it.first })
        assertEquals(listOf(1, 1, 1, 3), space.headingCounts.map { it.second })
        assertEquals(1, space.waitingInBackground)
        assertEquals("the computer's heading counts the same", list.machines.single().headingCounts.map { it.second }, space.headingCounts.map { it.second })
        // A paged list takes the hub's counts for the space: Idle and Unknown both.
        val counted = space.copy(totalCount = 9, stateCounts = mapOf(AgentState.Working to 2, AgentState.Idle to 4, AgentState.Unknown to 1), waitingInBackground = 1)
        assertEquals(listOf(0, 1, 0, 5), counted.headingCounts.map { it.second })
        // A section (Pinned, a history project) is no iOS `SpaceHeader`: it keeps its three counts.
        assertEquals(listOf(1, 1, 1), space.sectionCounts.map { it.second })
        // A quiet space says how many sessions it has instead of counts (nothing changes there); a subtask under its parent is not counted.
        val child = AgentSession(Session(machineId = "mac", sessionId = "h", state = "idle", spaceId = "app", spaceName = "app", sourceKind = "runtime",
            isSubagent = true, parentSessionId = "e", updatedAt = "2026-01-01T00:00:00Z"))
        assertEquals(listOf(1, 1, 1, 3), buildAgentList(listOf(at("a", "needs_you"), at("b", "working"), at("c", "working", waiting = true), at("d", "unread"),
            at("e", "idle"), at("f", "idle"), at("g", "unknown"), child), machines, settings = MachineSpaceTree).groups.single().headingCounts.map { it.second })
    }

    @Test fun byProjectNamesTheFirstSixAgentsThatNMoreIdleKeepsOutOfSight() {
        // iOS `IdleFoldRow` (#4338 A8): beside the count, the first six hidden agents' tab names, else their titles.
        fun at(id: String, state: String, tab: String? = null, title: String = "", index: Int? = null) = AgentSession(Session(machineId = "mac", sessionId = id,
            state = state, title = title, spaceId = "w1", spaceName = "app", slotId = tab, slotName = tab, slotIndex = index, updatedAt = "2026-01-01T00:00:0${id.length}Z"))
        val list = listOf(at("build", "working", tab = "build"), at("a", "idle", tab = "review"), at("b", "idle", tab = "docs"), at("c", "idle", title = "Sort receipts"),
            at("d", "idle", tab = "deps"), at("e", "idle", tab = "lint"), at("f", "idle", tab = "types"), at("g", "idle", tab = "perf"), at("h", "idle", tab = "ci"))
        val closed = agentTreeEntries(buildAgentList(list, machines, settings = MachineSpaceTree), "mac", tabs = true, hideIdle = true)
            .filterIsInstance<TreeIdleEntry>().single()
        assertEquals(8, closed.hidden)
        assertEquals("six names at most", 6, closed.tabs.size)
        assertTrue("a tab name, else the title", "Sort receipts" in closed.tabs)
        assertEquals(setOf("review", "docs", "Sort receipts", "deps", "lint", "types", "perf", "ci").containsAll(closed.tabs), true)
        // Once opened, the row that folds them has no names to give; a space with nothing hidden has no row.
        assertTrue(agentTreeEntries(buildAgentList(list, machines, settings = MachineSpaceTree.copy(expandedIdle = setOf("mac/w1"))), "mac", tabs = true, hideIdle = true)
            .filterIsInstance<TreeIdleEntry>().single().tabs.isEmpty())
        // An idle agent that a busy tab brought along is on screen already: it is not named beside the count.
        val withTab = listOf(at("x", "working", tab = "shared"), at("y", "idle", tab = "shared"), at("z", "idle", tab = "alone"), at("w", "working", tab = "main"))
        val one = agentTreeEntries(buildAgentList(withTab, machines, settings = MachineSpaceTree), "mac", tabs = true, hideIdle = true).filterIsInstance<TreeIdleEntry>().single()
        assertEquals(1, one.hidden)
        assertEquals(listOf("alone"), one.tabs)
    }

    @Test fun byProjectOnGlassEndsItsOpenedIdleAgentsWithARowThatFoldsThemAgain() {
        // iOS `agentRows` and `collapseRow` (#4338 A8): "N more idle" opens the idle agents; once open, "Hide N Idle" follows them.
        fun at(id: String, state: String, tab: String? = null) = AgentSession(Session(machineId = "mac", sessionId = id, state = state,
            spaceId = "w1", spaceName = "app", slotId = tab, updatedAt = "2026-01-01T00:00:00Z"))
        // Two idle agents of their own, and a tab of two peers of which only one is idle: it shows with its tab, and is
        // neither "more idle" nor hidden again.
        val list = listOf(at("build", "working"), at("review", "idle"), at("research", "idle"), at("x", "working", tab = "tab"), at("y", "idle", tab = "tab"))
        fun entries(expanded: Set<String> = emptySet(), hideIdle: Boolean = true, query: String = "") =
            agentTreeEntries(buildAgentList(list, machines, settings = MachineSpaceTree.copy(expandedIdle = expanded, query = query)), "mac",
                tabs = true, hideIdle = hideIdle).filterIsInstance<TreeIdleEntry>()
        // Closed: "N more idle", as before, with no fold.
        val closed = entries().single()
        assertEquals(2, closed.hidden)
        assertNull(closed.fold)
        // Open: the row that folds them again, counting the same two.
        val open = entries(setOf("mac/w1")).single()
        assertEquals(2, open.hidden)
        assertTrue(open.fold!!.open)
        assertEquals("idle:mac/w1", open.key)
        // Brief and Board (no hideIdle) have no such row; a search shows every agent and has none either.
        assertTrue(entries(setOf("mac/w1"), hideIdle = false).isEmpty())
        assertTrue(entries(setOf("mac/w1"), query = "re").isEmpty())
        // Nothing hidden by "N more idle" when open means nothing to fold: a space whose idle agents are all in a busy tab.
        val inTab = listOf(at("x", "working", tab = "tab"), at("y", "idle", tab = "tab"), at("z", "working"))
        val allShown = agentTreeEntries(buildAgentList(inTab, machines, settings = MachineSpaceTree.copy(expandedIdle = setOf("mac/w1"))), "mac",
            tabs = true, hideIdle = true)
        assertTrue(allShown.none { it is TreeIdleEntry })
    }

    @Test fun aSharedCheckoutPathKeepsEachRepositoryAndItsSessionsInTheTree() {
        val path = "/home/user"
        fun at(id: String, repo: String) = AgentSession(Session(machineId = "mac", sessionId = id,
            state = "working", cwd = "$path/project", repoRoot = repo, worktree = path))
        val sessions = listOf(at("main", path), at("first", "/code/first"), at("second", "/code/second"))
        val settings = AgentListSettings(grouping = AgentGrouping.Repo)
        fun entries(rows: List<AgentSession>) = agentTreeEntries(buildAgentList(rows, machines, settings = settings), "")
        val all = entries(sessions)
        assertEquals(all.size, all.map { it.key }.toSet().size)
        assertEquals(setOf("main", "first", "second"), all.filterIsInstance<TreeRowEntry>().map { it.row.session.sessionId }.toSet())
        val groups = all.filterIsInstance<TreeGroupEntry>().filter { it.group.rows.isNotEmpty() }.map { it.group }
        val main = groups.single { it.rows.single().session.sessionId == "main" }
        assertEquals("tree/mac/$path", main.id)
        for (session in sessions.drop(1)) {
            val group = groups.single { it.rows.single().id == session.id }
            assertEquals("repo/mac/${session.session.repoRoot}", group.under)
            val filtered = entries(listOf(session)).filterIsInstance<TreeGroupEntry>().single { it.group.rows.isNotEmpty() }
            assertEquals(group.id, filtered.group.id)
        }
    }

    // #2320: a herdr tab holding the launcher and two panes it split off, beside a tab of one agent and a tab of two peers
    private fun pane(id: String, state: String, slot: String, place: Int?, index: Int, at: String = "2026-01-01T00:00:00Z") =
        AgentSession(Session(machineId = "mac", sessionId = id, state = state, spaceId = "w1", spaceName = "app", slotId = slot,
            slotIndex = index, paneId = "w1:$id", paneIndex = place, updatedAt = at))
    private val panes = listOf(
        pane("review", "needs_you", "t1", 1, 1), pane("lead", "working", "t1", 0, 1), pane("tests", "unread", "t1", 2, 1),
        pane("deps", "working", "t2", 0, 2), pane("peerA", "unread", "t3", 1, 3), pane("peerB", "idle", "t3", 2, 3),
    )

    @Test fun aTabOfSeveralAgentsIsOneGroupTheLauncherOnTopItsBranchesUnderIt() {
        val entries = agentTreeEntries(buildAgentList(panes, machines, settings = MachineSpaceTree), "mac", tabs = true)
        assertEquals(listOf("group:mac/w1", "tab:mac/:w1/t1", "session:mac/lead", "session:mac/review", "session:mac/tests",
            "tab:mac/:w1/t3", "session:mac/peerA", "session:mac/peerB", "session:mac/deps"), entries.map { it.key })
        val tab = entries.filterIsInstance<TreeTabEntry>().first()
        assertEquals("review", tab.tab.lead.session.sessionId) // the row speaks for the one that needs you
        assertEquals(3, tab.tab.members.size)
        assertEquals(0, tab.depth)
        assertTrue(tab.first)
        val rows = entries.filterIsInstance<TreeRowEntry>().associateBy { it.row.session.sessionId }
        assertTrue(rows.getValue("lead").launcher)
        assertEquals(1, rows.getValue("lead").depth)
        assertEquals(listOf(true, true), listOf("review", "tests").map { rows.getValue(it).branch })
        assertEquals(listOf(2, 2), listOf("review", "tests").map { rows.getValue(it).depth })
        // peers (the top left pane is a shell): no launcher, side by side under their tab
        assertEquals(listOf(false, false), listOf("peerA", "peerB").map { rows.getValue(it).launcher || rows.getValue(it).branch })
        assertEquals(listOf(1, 1), listOf("peerA", "peerB").map { rows.getValue(it).depth })
        // the idle peer is shown with its tab, so nothing is left for "N more idle"
        assertTrue(entries.none { it is TreeIdleEntry })
        assertTrue(rows.getValue("deps").last)
        assertEquals(0, rows.getValue("deps").depth)
    }

    @Test fun aFoldedTabIsItsRowAndTheOtherGroupingsListOneAgentARow() {
        val list = buildAgentList(panes, machines, settings = MachineSpaceTree)
        val tab = agentTreeEntries(list, "mac", tabs = true).filterIsInstance<TreeTabEntry>().first().tab
        val folded = agentTreeEntries(list, "mac", tabs = true, foldedTabs = setOf(tab.foldKey))
        assertEquals(listOf("group:mac/w1", "tab:mac/:w1/t1", "tab:mac/:w1/t3"), folded.take(3).map { it.key })
        assertTrue(folded.filterIsInstance<TreeTabEntry>().first().folded)
        // without tabs ("By repository", "By status", "By to-do", the way the web lists them too) every agent is a row of its own
        val flat = agentTreeEntries(list, "mac")
        assertTrue(flat.none { it is TreeTabEntry })
        assertEquals(panes.size - 1, flat.count { it is TreeRowEntry }) // the seen peer is tucked under "1 more idle"
        // one agent of the tab left under a quick filter: a row of its own
        val needs = agentTreeEntries(buildAgentList(panes, machines, settings = MachineSpaceTree.copy(quick = AgentQuickFilter.NeedsYou)), "mac", tabs = true)
        assertTrue(needs.none { it is TreeTabEntry })
    }

    // #4338 A11, as iOS's `cappedRows`: a long section shows 20 rows, then "Show All N"; open, "Collapse" at its end.
    @Test fun aLongSectionShowsTwentyRowsThenShowAll() {
        val many = (1..30).map { session("box", "s$it", "working", "api") }
        val byStatus = AgentListSettings(grouping = AgentGrouping.State)
        val list = buildAgentList(many, machines, settings = byStatus)
        fun entries(showAll: Set<String>?, of: AgentListResult = list) = agentTreeEntries(of, "", showAll = showAll)
        val capped = entries(emptySet())
        assertEquals(20, capped.count { it is TreeRowEntry })
        assertEquals(TreeShowAllEntry(list.groups.single(), 30, 0, expanded = false), capped.last())
        val open = entries(setOf(AgentRowCap.key("state/working")))
        assertEquals(30, open.count { it is TreeRowEntry })
        assertTrue((open.last() as TreeShowAllEntry).expanded)
        // A search, a quick filter or a paged list shows every row (null); a run only a few over the cap shows them all.
        assertEquals(30, entries(null).count { it is TreeRowEntry })
        assertTrue(entries(null).none { it is TreeShowAllEntry })
        assertTrue(entries(emptySet(), buildAgentList(many.take(25), machines, settings = byStatus)).none { it is TreeShowAllEntry })
    }
}
