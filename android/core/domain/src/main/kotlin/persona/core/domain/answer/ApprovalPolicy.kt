package persona.core.domain.answer

enum class AnswerEntry(val wire: String) {
    Conversation("conversation"), Notification("notification"), LockScreen("lock_screen"),
    Watch("watch"), SiriShortcut("siri_shortcut"),
}

enum class ApprovalAction(val wire: String) {
    ApproveAll("approve_all"), ApproveEdits("approve_edits"),
    /** Approve the plan and keep confirming each step (mode `default`, web's "Approve, confirm each step"). */
    ApproveStepwise("approve_stepwise"), Reject("reject"),
    Allow("allow"), Deny("deny"), Always("always"), Answer("answer"),
    ScreenAnswer("screen_answer"), ChooseOption("choose_option"), OpenApp("open_app"), Dismiss("dismiss"),
}

enum class Authentication(val wire: String) {
    Required("required"), None("none"), PlatformDefault("platform_default"), Unspecified("unspecified"),
}

/** Interaction answers are one-shot operations; reconcile an unknown result instead of replaying it. */
enum class AnswerRetry { Never }

enum class AnswerRequirement(val wire: String) {
    FullPlanRead("full_plan_read"), FeedbackEmpty("feedback_empty"), ReasonEmpty("reason_empty"),
    SessionPresent("session_present"), SingleQuestionSingleChoice("single_question_single_choice"),
    QuestionNotIrreversible("question_not_irreversible"), PlainDialogOption("plain_dialog_option"),
    /** Claude's card, or another agent's that names what "always" allows (always_label), as on iOS. */
    AlwaysOffered("always_offered"),
}

data class AvailableAction(
    val id: ApprovalAction,
    val authentication: Authentication,
    val requiresUnlock: Boolean?,
    val mode: String? = null,
    val requirements: List<AnswerRequirement> = emptyList(),
    val input: String? = null,
    val maxOptions: Int? = null,
    val retry: AnswerRetry = AnswerRetry.Never,
) {
    val availableNow: Boolean? get() = requiresUnlock?.not()
}

/** Unified policy only. Unknown authentication never grants permission while locked. */
object ApprovalPolicy {
    fun actions(entry: AnswerEntry, kind: String, locked: Boolean): List<AvailableAction> {
        fun action(
            id: ApprovalAction,
            auth: Authentication = Authentication.Required,
            mode: String? = null,
            requirements: List<AnswerRequirement> = emptyList(),
            input: String? = null,
            maxOptions: Int? = null,
        ) = AvailableAction(id, auth, when {
            !locked || auth == Authentication.None -> false
            auth == Authentication.Required -> true
            else -> null
        }, mode, requirements, input, maxOptions)

        val open = action(ApprovalAction.OpenApp)
        val edits = action(ApprovalAction.ApproveEdits, mode = "acceptEdits")
        val allow = action(ApprovalAction.Allow)
        val deny = action(ApprovalAction.Deny, Authentication.None)
        val dismiss = action(ApprovalAction.Dismiss,
            if (entry == AnswerEntry.Watch) Authentication.PlatformDefault else Authentication.Required)
        return when (entry) {
            AnswerEntry.Conversation -> when (kind) {
                "plan" -> listOf(
                    action(ApprovalAction.ApproveAll, mode = "bypassPermissions", requirements = listOf(
                        AnswerRequirement.FullPlanRead, AnswerRequirement.FeedbackEmpty)),
                    edits.copy(requirements = listOf(AnswerRequirement.FeedbackEmpty)),
                    action(ApprovalAction.ApproveStepwise, mode = "default", requirements = listOf(AnswerRequirement.FeedbackEmpty)),
                    action(ApprovalAction.Reject), dismiss)
                "permission" -> listOf(
                    allow.copy(requirements = listOf(AnswerRequirement.ReasonEmpty)),
                    action(ApprovalAction.Deny),
                    action(ApprovalAction.Always, requirements = listOf(
                        AnswerRequirement.SessionPresent, AnswerRequirement.ReasonEmpty, AnswerRequirement.AlwaysOffered)), dismiss)
                "question" -> listOf(action(ApprovalAction.Answer), dismiss)
                "screen" -> listOf(action(ApprovalAction.ScreenAnswer), dismiss)
                else -> emptyList()
            }
            AnswerEntry.Notification -> when (kind) {
                "plan" -> listOf(edits, action(ApprovalAction.Reject, Authentication.None, input = "feedback"), open)
                "permission" -> listOf(allow, deny.copy(input = "message"), open)
                "question" -> listOf(action(ApprovalAction.ChooseOption, requirements = listOf(
                    AnswerRequirement.SingleQuestionSingleChoice, AnswerRequirement.QuestionNotIrreversible), maxOptions = 3), open)
                "screen" -> listOf(action(ApprovalAction.ChooseOption, requirements = listOf(
                    AnswerRequirement.PlainDialogOption), maxOptions = 3), open)
                else -> emptyList()
            }
            AnswerEntry.LockScreen -> when (kind) {
                "plan" -> listOf(edits, open)
                "permission" -> listOf(allow, deny.copy(input = "none"), open)
                "question", "screen" -> listOf(open)
                else -> emptyList()
            }
            AnswerEntry.Watch -> when (kind) {
                "plan" -> listOf(edits, action(ApprovalAction.Reject, Authentication.None), dismiss)
                "permission" -> listOf(allow, deny, dismiss)
                "question" -> listOf(action(ApprovalAction.Answer, Authentication.PlatformDefault), dismiss)
                "screen" -> listOf(action(ApprovalAction.ScreenAnswer, Authentication.PlatformDefault), dismiss)
                else -> emptyList()
            }
            AnswerEntry.SiriShortcut -> when (kind) {
                "plan" -> listOf(edits)
                "permission" -> listOf(allow)
                "question", "screen" -> listOf(open)
                else -> emptyList()
            }
        }
    }
}
