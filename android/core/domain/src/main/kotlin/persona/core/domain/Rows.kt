package persona.core.domain

import persona.core.contracts.ProtocolCompatibility
import persona.core.contracts.canonicalMcpToolName

import persona.core.contracts.CoreText.Key
import persona.core.contracts.CoreText
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonNull
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.booleanOrNull
import kotlinx.serialization.json.contentOrNull
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import persona.core.contracts.Item

/** What the chat shows: user / assistant text, a tool call folded with its result, or a notice. */
sealed interface Row {
    val id: String
    data class User(override val id: String, val text: String, val queued: Boolean, val from: String?) : Row
    data class Assistant(override val id: String, val text: String) : Row
    data class Thinking(override val id: String, val text: String) : Row
    data class Tool(override val id: String, val use: Item, val result: Item?) : Row
    data class Image(override val id: String, val item: Item) : Row
    data class Notice(override val id: String, val text: String) : Row
}

/** Tool uses and results share tool_use_id: merge them into one row (as the iOS chat does). */
fun buildRows(items: List<Item>, photo: String): List<Row> {
    val results = items.filter { it.kind == "tool_result" && it.toolUseId != null }.associateBy { it.toolUseId!! }
    val rows = mutableListOf<Row>()
    for (it in items) {
        when (it.kind) {
            "text" -> {
                val t = cleanText(it.text.orEmpty(), photo)
                if (t.isBlank()) continue
                rows += if (it.role == "user") Row.User(it.id, t, it.queued == true, it.from) else Row.Assistant(it.id, t)
            }
            "thinking" -> if (!it.text.isNullOrBlank()) rows += Row.Thinking(it.id, it.text.orEmpty())
            "tool_use" -> rows += Row.Tool(it.id, it, it.toolUseId?.let { id -> results[id] })
            "tool_result" -> Unit // shown with its tool_use
            "image" -> if (it.toolUseId == null) rows += Row.Image(it.id, it)
            "notice" -> if (!it.text.isNullOrBlank()) rows += Row.Notice(it.id, it.text.orEmpty())
        }
    }
    return rows
}

/**
 * A message as one plain text for previews (docs/protocol/protocol.md "Tags in messages"): what the hub / Claude Code wrap around
 * typed text unwrapped, photos as "[Image]". `<persona-context>` blocks are not what the user wrote and are left out
 * ([parseContexts]); a message of nothing but blocks previews as empty. A conversation row draws photos as thumbnails
 * instead ([messageText]) and folds the blocks into one row.
 */
fun cleanText(s: String, photo: String): String {
    val shown = messageText(parseContexts(s).second
        .replace(Regex("<cross-session-message[^>]*>([\\s\\S]*?)</cross-session-message>"), "$1"), files = false)
    return if (shown.images.isEmpty()) shown.text else ("$photo\n" + shown.text).trim()
}

/** A tool's own name without its MCP prefix (iOS `ToolPresentation.baseName`): "mcp__persona__agent_read" → "agent_read". */
fun toolBaseName(rawName: String): String = canonicalMcpToolName(rawName).let { name ->
    if (name.startsWith("mcp__")) name.substringAfterLast("__") else name
}

/**
 * The word for a tool call (iOS `ToolPresentation.verb`): a name the table knows, else its name in words ([toolWords]:
 * "GitHub · Create issue"); "tool" without one.
 */
fun toolVerb(name: String?): CoreText =
    if (name == null) CoreText.of(Key.ToolGeneric) else namedToolVerb(name) ?: CoreText.plain(toolWords(name))

/**
 * The words for a tool the table knows, null for any other ([toolWords] puts its name into words). Matched on the base
 * name of Persona's own tools ([isPersonaTool]), which first get their own words; the ones neither table knows read
 * "Call Persona · agent read" (iOS `PersonaToolNames`). Another server's MCP tool is that server's, never the
 * built-in one of the same bare name ("mcp__files__Read" is "Files · Read", iOS `ToolNaming.tableName`). A few tools say
 * what this call did instead ([callToolVerb]).
 */
fun namedToolVerb(name: String): CoreText? {
    if (ToolNaming.isOtherMcp(name)) return null
    val n = toolBaseName(name)
    if (isPersonaTool(name)) personaToolVerb(ProtocolCompatibility.canonicalTool(n))?.let { return CoreText.of(it) }
    genericToolVerb(n)?.let { return CoreText.of(it) }
    return if (isPersonaTool(name)) CoreText.of(Key.ToolPersonaCall, n.replace('_', ' ')) else null
}

/** The MCP server of a tool's full name ("mcp__persona__agent_read" → "persona"), null for any other tool. */
fun toolServer(rawName: String): String? {
    val name = canonicalMcpToolName(rawName)
    if (!name.startsWith("mcp__")) return null
    val rest = name.removePrefix("mcp__")
    val i = rest.lastIndexOf("__")
    return if (i <= 0) null else rest.substring(0, i)
}

/** Persona's MCP server, also exposed by the `plugin_persona` runtime plugin. */
fun isPersonaTool(name: String): Boolean = toolServer(name)?.removePrefix("plugin_") in ProtocolCompatibility.personaServers

private fun genericToolVerb(n: String): Key? {
    if (n.startsWith("phone_")) return Key.ToolPhone
    if (n.startsWith("mail_")) return Key.ToolMail
    return when (n) {
        "Bash", "PowerShell" -> Key.ToolRun
        "Read", "NotebookRead" -> Key.ToolRead
        "Edit", "MultiEdit", "NotebookEdit" -> Key.ToolEdit
        "Write" -> Key.ToolWrite
        "Grep" -> Key.ToolSearch
        "Glob", "LS" -> Key.ToolFind
        "WebFetch" -> Key.ToolWebFetch
        "WebSearch" -> Key.ToolWebSearch
        "Task", "Agent" -> Key.Subtask
        "TodoWrite" -> Key.ToolTodos
        // Claude Code's task list (TodoWrite's successor) and its background tasks.
        "TaskCreate" -> Key.ToolTaskCreate
        "TaskUpdate" -> Key.ToolTaskUpdate
        "TaskList" -> Key.ToolTaskList
        "TaskGet" -> Key.ToolTaskGet
        "TaskStop", "KillShell", "KillBash" -> Key.ToolTaskStop
        "TaskOutput", "BashOutput" -> Key.ToolTaskOutput
        // Other agents and sessions (cross-session messaging, agent teams).
        "SendMessage" -> Key.ToolAgentSend
        "ListAgents" -> Key.ToolAgentsList
        "SubagentHandback" -> Key.ToolSubagentHandback
        // Coming back later: a /loop's next round, prompts on a schedule here or in the cloud.
        "ScheduleWakeup" -> Key.ToolScheduleWakeup
        "CronCreate" -> Key.ToolRoutineSchedule
        "CronList" -> Key.ToolCronList
        "CronDelete" -> Key.ToolRoutineDelete
        "RemoteTrigger" -> Key.ToolRemoteTrigger
        "LSP" -> Key.ToolLsp
        "EnterWorktree" -> Key.ToolEnterWorktree
        "ExitWorktree" -> Key.ToolExitWorktree
        // What reaches the user: a file, a published page, a notification, a review's findings.
        "SendUserFile" -> Key.ToolSendUserFile
        "Artifact" -> Key.ToolArtifact
        "PushNotification" -> Key.ToolNotifyYou
        "ReportFindings" -> Key.ToolReportFindings
        "SendFeedback" -> Key.ToolSendFeedback
        "EndConversation" -> Key.ToolEndConversation
        "AskUserQuestion", "ask_user" -> Key.ToolAsk
        "ExitPlanMode" -> Key.ToolSubmitPlan
        "EnterPlanMode" -> Key.ToolEnterPlan
        "ToolSearch" -> Key.ToolLoadTools
        "WaitForMcpServers" -> Key.ToolWaitForMcp
        "ListMcpResourcesTool" -> Key.ToolListMcpResources
        "ReadMcpResourceTool" -> Key.ToolReadMcpResource
        "Skill" -> Key.ToolSkill
        "Monitor" -> Key.ToolMonitor
        "Workflow" -> Key.ToolWorkflow
        // Persona managing its own conversations (docs/assistant/assistant.md "Managing its own conversations").
        "conversations_list" -> Key.ToolConversationsList
        "conversation_read" -> Key.ToolConversationRead
        "conversation_rename" -> Key.ToolConversationRename
        "conversation_archive" -> Key.ToolConversationArchive
        "conversation_unarchive" -> Key.ToolConversationUnarchive
        "conversation_new" -> Key.ToolConversationNew
        "conversation_send" -> Key.ToolConversationSend
        // Operating a computer through Cua Driver (docs/extensions/computer-use.md).
        "computer_state" -> Key.ToolComputerState
        "computer_run" -> Key.ToolComputerRun
        "browser_run" -> Key.ToolBrowserRun
        "browser_look" -> Key.ToolBrowserLook
        // Operating an iPhone through a Mac it is paired with (docs/extensions/iphone.md).
        "iphone_status" -> Key.ToolIphoneStatus
        "iphone_look" -> Key.ToolIphoneLook
        "iphone_do" -> Key.ToolIphoneDo
        else -> null
    }
}

/**
 * The words for what this call did (iOS `ToolPresentation.verb(_ call:)`): a few tools say it from their input, a to-do
 * started, completed or deleted, a notebook cell added, deleted or edited, what a language server was asked, a loop
 * stopped, a worktree removed, the artifacts listed. Null for any other call, which reads as the tool's own words.
 */
fun callToolVerb(name: String, input: JsonElement?): CoreText? {
    if (ToolNaming.isOtherMcp(name)) return null
    val i = input as? JsonObject
    fun mode(k: String) = (i?.get(k) as? JsonPrimitive)?.takeUnless { it is JsonNull }?.content.orEmpty()
    val key = when (toolBaseName(name)) {
        "TaskUpdate" -> when (mode("status")) {
            "in_progress" -> Key.ToolTaskStart
            "completed" -> Key.ToolTaskComplete
            "deleted" -> Key.ToolTaskDelete
            else -> null
        }
        "NotebookEdit" -> when (mode("edit_mode")) {
            "insert" -> Key.ToolCellAdd
            "delete" -> Key.ToolCellDelete
            else -> Key.ToolCellEdit
        }
        "LSP" -> when (mode("operation")) {
            "goToDefinition" -> Key.ToolLspDefinition
            "goToImplementation" -> Key.ToolLspImplementations
            "findReferences" -> Key.ToolLspReferences
            "hover" -> Key.ToolLspHover
            "documentSymbol" -> Key.ToolLspSymbols
            "workspaceSymbol" -> Key.ToolLspWorkspaceSymbol
            "prepareCallHierarchy", "incomingCalls", "outgoingCalls" -> Key.ToolLspCalls
            else -> null
        }
        "ScheduleWakeup" -> Key.ToolStopLoop.takeIf { (i?.get("stop") as? JsonPrimitive)?.takeIf { !it.isString }?.booleanOrNull == true }
        "ExitWorktree" -> Key.ToolWorktreeRemove.takeIf { mode("action") == "remove" }
        "Artifact" -> Key.ToolArtifactsList.takeIf { mode("action") == "list" }
        else -> null
    }
    return key?.let { CoreText.of(it) }
}

/**
 * Claude Code's newer tools and the iPhone's with no icon of their own, whose row keeps the tool's words as its title and
 * draws the call's subject under them, as a tool no table knows does: the wrench says nothing of what the step did, and
 * Glass and Brief rows draw no verb. A tool the chat's own icons name (Read, Edit, Bash, the to-do list…) or a skill or a
 * monitor keeps the subject as its title.
 */
fun rowKeepsWords(name: String): Boolean = !ToolNaming.isOtherMcp(name) && toolBaseName(name) in wordedTools

private val wordedTools = setOf(
    "TaskStop", "KillShell", "KillBash", "TaskOutput", "BashOutput", "SubagentHandback", "SendMessage",
    "ListAgents", "ScheduleWakeup", "CronCreate", "CronList", "CronDelete", "RemoteTrigger", "LSP", "EnterWorktree",
    "ExitWorktree", "SendUserFile", "Artifact", "PushNotification", "ReportFindings", "SendFeedback", "EndConversation",
    "WaitForMcpServers", "ListMcpResourcesTool", "ReadMcpResourceTool", "Workflow", "iphone_status", "iphone_look", "iphone_do",
)

private fun personaToolVerb(n: String): Key? = when (n) {
    // Agents on the user's computers (docs/assistant/assistant.md "Current structure", Agents).
    "agent_read" -> Key.ToolAgentRead
    "agent_send", "send_to_agent" -> Key.ToolAgentSend
    "agents_list", "list_agents" -> Key.ToolAgentsList
    "agent_start", "task_plan" -> Key.ToolAgentStart
    "interaction_answer" -> Key.ToolInteractionAnswer
    "ask_persona" -> Key.ToolAskPersona
    // The knowledge base (docs/assistant/knowledge.md).
    "kb_get", "kb_list", "kb_search", "search_knowledge" -> Key.ToolKbSearch
    "kb_update" -> Key.ToolKbUpdate
    // Talking to the user.
    "ask_user" -> Key.ToolAskYou
    "notify_user" -> Key.ToolNotifyYou
    "schedule_followup" -> Key.ToolFollowup
    "list_followups" -> Key.ToolFollowupsList
    "cancel_followup" -> Key.ToolFollowupCancel
    "schedule_routine" -> Key.ToolRoutineSchedule
    "delete_routine" -> Key.ToolRoutineDelete
    "inbox_draft" -> Key.ToolInboxDraft
    "persona_settings" -> Key.ToolPersonaSettings
    // Live cards (docs/assistant/assistant-views.md).
    "view_show" -> Key.ToolViewShow
    "view_get" -> Key.ToolViewGet
    "view_update" -> Key.ToolViewUpdate
    else -> null
}

/** One line of what the call is about: the command, the file, the pattern… ([toolSubjectText] in [CoreText.words]). */
fun toolSubject(it: Item, pick: Boolean = true): String = CoreText.words(toolSubjectText(it, pick))

/**
 * [toolSubject] as words to resolve: computer_state without an app reads "machine · whole screen" (iOS
 * `ToolPresentation.subject`), so that part follows the app's language; a few subjects are words of their own, several
 * findings by their count and a wake-up without a reason by its delay ([wordedSubject]). A tool no table knows (another
 * server's, a new one) takes the field of its input that says the most ([ToolNaming.subject]); [pick] false leaves it ""
 * (a permission card has its own fallback). Unlike iOS, a tool a table names but no case here does (agent_read) keeps "":
 * the rows of Glass and Brief show no verb, and a picked subject would take its words' place; [pickNamed] picks one for it
 * all the same, for a row that keeps its words as the title and draws the subject under them ([rowKeepsWords]).
 * [machine] names the Mac a computer tool's `machine` is, as the user knows it ([ComputerSteps.machineName]).
 */
fun toolSubjectText(it: Item, pick: Boolean = true, machine: (String) -> String = { name -> name }, pickNamed: Boolean = false): CoreText {
    val i = it.toolInput as? JsonObject
    val name = it.toolName.orEmpty()
    if (i != null && ToolNaming.tableName(name) == "computer_state") {
        fun s(k: String) = (i[k] as? JsonPrimitive)?.content?.takeIf(String::isNotEmpty)
        val app = s("app")?.let(CoreText::plain) ?: CoreText.of(Key.ToolWholeScreen)
        return CoreText.joined(" · ", listOfNotNull(s("machine")?.let { named -> CoreText.plain(machine(named)) }, app))
    }
    wordedSubject(name, i)?.let { return it }
    val picked = pick && (pickNamed || namedToolVerb(name) == null)
    return CoreText.plain(plainToolSubject(it, machine) ?: if (picked) ToolNaming.subject(it.toolInput) else "")
}

/** Whether a case here reads this tool's subject (iOS `ToolPresentation.subject`'s own cases), not the field of its input that says the most. */
fun toolHasSubjectCase(it: Item): Boolean = plainToolSubject(it) { name -> name } != null

/**
 * The subjects that are words of their own, which need the app's language (iOS `ToolPresentation.subject`): several
 * findings by their count ("2 findings"), and a wake-up that gives no reason by its delay ("5 min"). Null for any other
 * call, whose subject is [plainToolSubject]'s.
 */
private fun wordedSubject(name: String, i: JsonObject?): CoreText? {
    if (i == null || ToolNaming.isOtherMcp(name)) return null
    fun text(k: String) = (i[k] as? JsonPrimitive)?.takeUnless { it is JsonNull }?.content
    return when (toolBaseName(name)) {
        "ReportFindings" -> (i["findings"] as? JsonArray)?.size?.takeIf { it > 1 }?.let { CoreText.of(Key.ToolFindings, it) }
        "ScheduleWakeup" -> if (!text("reason").isNullOrEmpty()) null
            else text("delaySeconds")?.toDoubleOrNull()?.takeIf(Double::isFinite)?.let { workTime(it.toLong()) }
        else -> null
    }
}

/** The subject of a tool a case here knows (iOS `ToolPresentation.subject`); null for any other. */
private fun plainToolSubject(it: Item, machine: (String) -> String): String? {
    val name = it.toolName.orEmpty()
    // Another server's tool is that server's, never the built-in one of the same bare name ("mcp__files__Read").
    if (ToolNaming.isOtherMcp(name)) return null
    val i = it.toolInput as? JsonObject ?: return null
    fun s(k: String) = (i[k] as? JsonPrimitive)?.content
    return when (name) {
        "Bash" -> s("description") ?: s("command")?.lineSequence()?.firstOrNull().orEmpty()
        "Read", "Edit", "MultiEdit", "Write", "NotebookEdit" -> (s("file_path") ?: s("notebook_path")).orEmpty().substringAfterLast('/')
        "Grep", "Glob" -> s("pattern").orEmpty()
        "WebFetch" -> s("url").orEmpty().removePrefix("https://").removePrefix("http://").substringBefore('/')
        "WebSearch" -> s("query").orEmpty()
        "Task", "Agent" -> s("description").orEmpty()
        "Skill" -> s("skill") ?: s("command").orEmpty()
        "AskUserQuestion" -> firstQuestion(i)
        "ExitPlanMode" -> s("plan")?.lineSequence()?.firstOrNull { l -> l.isNotBlank() }?.trimStart('#', ' ').orEmpty()
        else -> claudeCodeToolSubject(name, i) ?: namedToolSubject(toolBaseName(name), ::s, machine) { firstQuestion(i) }
    }
}

private fun firstQuestion(i: JsonObject) =
    runCatching { i["questions"]!!.jsonArray[0].jsonObject["question"]!!.jsonPrimitive.content }.getOrDefault("")

/**
 * The subject of Claude Code's newer tools (iOS `ToolPresentation.subject`): the to-do a task-list call names, else its
 * number; who a message went to and what about; the files sent; where a code lookup looked. Null for any other tool.
 */
private fun claudeCodeToolSubject(n: String, i: JsonObject): String? {
    fun s(k: String) = (i[k] as? JsonPrimitive)?.takeUnless { it is JsonNull }?.content?.takeIf(String::isNotEmpty)
    fun line(t: String?) = t?.let(ToolNaming::firstLine)
    fun file(k: String) = s(k)?.let(ToolNaming::lastPathComponent)
    return when (n) {
        "PowerShell" -> s("description") ?: line(s("command")).orEmpty()
        "NotebookRead" -> file("file_path") ?: file("notebook_path") ?: ""
        // "select:WebFetch,Monitor" loads those two by name.
        "ToolSearch" -> s("query").orEmpty().let { q -> if (q.startsWith("select:")) q.removePrefix("select:").split(',').joinToString(", ") else q }
        "Monitor" -> s("description") ?: line(s("command")) ?: ((i["ws"] as? JsonObject)?.get("url") as? JsonPrimitive)?.contentOrNull.orEmpty()
        // The node's name for its run: its `name`, the name in its script's meta, else its script file's (#3436).
        "Workflow" -> s("name") ?: s("script")?.let { workflowMetaName.find(it)?.groupValues?.get(1) } ?: s("scriptPath")?.let(::workflowScriptName) ?: ""
        "TaskCreate" -> s("subject") ?: line(s("description")).orEmpty()
        "TaskUpdate" -> s("subject") ?: s("activeForm") ?: s("taskId")?.let { "#$it" } ?: ""
        "TaskGet" -> s("taskId")?.let { "#$it" } ?: ""
        "TaskList", "ListAgents", "CronList", "ExitWorktree" -> ""
        "TaskStop", "KillShell", "KillBash", "TaskOutput", "BashOutput" -> s("task_id") ?: s("shell_id") ?: s("bash_id") ?: ""
        // Who it went to and what about: Claude's own one-line summary, else the message's first line.
        "SendMessage" -> listOfNotNull(s("to") ?: s("recipient"), s("summary") ?: line(s("message") ?: s("content"))
            ?: ((i["message"] as? JsonObject)?.get("type") as? JsonPrimitive)?.contentOrNull?.let(ToolNaming::humanize)).joinToString(" · ")
        "ScheduleWakeup" -> line(s("reason")).orEmpty()
        "CronCreate" -> listOfNotNull(s("cron"), line(s("prompt"))).joinToString(" · ")
        "CronDelete" -> s("id").orEmpty()
        "RemoteTrigger" -> listOfNotNull(s("action")?.let(ToolNaming::humanize), s("trigger_id")).joinToString(" · ")
        // Where it looked: the symbol searched for, else the file and line.
        "LSP" -> s("query")?.takeIf { s("operation") == "workspaceSymbol" } ?: listOfNotNull(file("filePath"), s("line")).joinToString(":")
        "EnterWorktree" -> s("name") ?: file("path") ?: ""
        "SendUserFile" -> {
            val listed = ((i["files"] ?: i["file_paths"] ?: i["paths"]) as? JsonArray).orEmpty().mapNotNull { f ->
                (f as? JsonPrimitive)?.takeIf { it.isString }?.content ?: (f as? JsonObject)?.let { o ->
                    ((o["path"] ?: o["file_path"]) as? JsonPrimitive)?.contentOrNull
                }
            } + listOfNotNull(s("file_path"), s("path"))
            if (listed.isNotEmpty()) listed.joinToString(", ") { ToolNaming.lastPathComponent(it) }
            else line(s("caption")) ?: ToolNaming.subject(i)
        }
        "Artifact" -> s("title") ?: file("file_path") ?: s("url") ?: ""
        "PushNotification" -> line(s("message")).orEmpty()
        // One finding by what it says; several by how many ([wordedSubject]).
        "ReportFindings" -> ((i["findings"] as? JsonArray)?.singleOrNull() as? JsonObject)?.let { f ->
            line(((f["short_summary"] ?: f["summary"]) as? JsonPrimitive)?.contentOrNull)
        }.orEmpty()
        "WaitForMcpServers" -> ((i["servers"] ?: i["server_names"]) as? JsonArray).orEmpty()
            .mapNotNull { (it as? JsonPrimitive)?.takeIf { p -> p.isString }?.content }.joinToString(", ").ifEmpty { s("server").orEmpty() }
        "ListMcpResourcesTool" -> s("server").orEmpty()
        "ReadMcpResourceTool" -> s("uri") ?: s("server") ?: ""
        else -> null
    }
}

/** The `name: '…'` a workflow script's meta gives it (iOS `workflowName`, the node's `wfNameRe`). */
private val workflowMetaName = Regex("\\bname:\\s*['\"`]([^'\"`\n]+)['\"`]")

/**
 * A workflow's name from its script file, as the node has it (iOS `workflowScriptName`): the file's name without ".js"
 * and the run id Claude Code adds ("…/parity-audit-wf_c1d9daf8.js" is "parity-audit").
 */
private fun workflowScriptName(path: String): String {
    val base = path.split('/', '\\').last().removeSuffix(".js")
    val cut = base.indexOf("-wf_")
    return if (cut > 0) base.substring(0, cut) else base
}

/**
 * The subject of Persona's own, the computer's, the phone's and mail tools (iOS `ToolPresentation.subject`); null for a
 * tool none of them is, whose subject comes from its input ([ToolNaming.subject]).
 */
private fun namedToolSubject(n: String, field: (String) -> String?, machine: (String) -> String, questions: () -> String): String? {
    fun s(k: String) = field(k)?.takeIf(String::isNotEmpty)
    fun firstLine(t: String?) = t?.trim()?.lineSequence()?.firstOrNull().orEmpty()
    return when (n) {
        "conversations_list" -> s("query").orEmpty()
        "conversation_rename" -> listOfNotNull(s("kind"), s("topic")).joinToString(" | ")
        "conversation_new", "conversation_send" -> firstLine(s("text"))
        "conversation_read", "conversation_archive", "conversation_unarchive" -> s("id").orEmpty().take(8)
        // ask_user is the assistant's own question tool (docs/assistant/assistant.md); it takes AskUserQuestion's questions.
        "ask_user" -> questions()
        "computer_run", "browser_look" -> s("machine")?.let(machine).orEmpty()
        "browser_run" -> listOfNotNull(s("machine")?.let(machine), s("script")?.let(::firstLine)?.takeIf(String::isNotEmpty)).joinToString(" · ")
        // The iPhone a tool looks at or operates (its steps are not read here yet, iOS `IPhoneSteps`).
        "iphone_status" -> ""
        "iphone_look" -> s("phone") ?: s("query").orEmpty()
        "iphone_do" -> s("phone").orEmpty()
        else -> if (n.startsWith("phone_") || n.startsWith("mail_")) n.split('_').drop(1).joinToString(" ") else null
    }
}

/** The details shown when a tool row is expanded. */
fun toolDetail(it: Item): String {
    val i = it.toolInput as? JsonObject ?: return ""
    fun s(k: String) = (i[k] as? JsonPrimitive)?.content
    // A shell's command line ("$ go test ./...", "PS> dir", a Monitor's command or address, iOS `ToolNaming.commandLine`).
    ToolNaming.commandLine(it.toolName.orEmpty(), i)?.let { return it }
    return when (it.toolName) {
        "Edit" -> "--- " + s("file_path").orEmpty() + "\n" +
            s("old_string").orEmpty().lines().joinToString("\n") { "- $it" } + "\n" +
            s("new_string").orEmpty().lines().joinToString("\n") { "+ $it" }
        "Write" -> s("file_path").orEmpty() + "\n" + s("content").orEmpty().take(4000)
        "ExitPlanMode" -> s("plan").orEmpty()
        else -> i.entries.joinToString("\n") { (k, v) -> "$k: " + ((v as? JsonPrimitive)?.content ?: v.toString()) }
    }
}
