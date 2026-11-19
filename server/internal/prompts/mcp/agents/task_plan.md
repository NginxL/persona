Plan Agent work (code, repo research, or more than a few tool calls) instead of starting agents yourself: one task per independent piece. Check machines_list before choosing resources. Fill every field from the knowledge base, agents_list and user preferences; each choice is an editable chip with your one-line reason. Read-only tasks, tasks the user asked for themselves (user_requested), and kinds the user told you to start without asking, start at once; the rest wait as a card for the user's 开始. Returns immediately: carry on. Each queued task becomes a todo in your context snapshot; use agent_send to reach its Agent. Things a tool does for you (a reminder, an event) are not tasks. A task stays on its requested machine by default (machine_policy=fixed), including retries. An unavailable launcher fails there; do not reroute machine-local work. Use machine_policy=flexible only when the task may run on another computer: a failed health check can then move it, and its why says so. The response confirms queued dispatch, not an Agent start; check the task outcome. From a leader conversation (assistant.leaders) or any other side conversation you supervise what you hand out; you get a 督办 message when one needs you, and you close it with task_review, not by repeating its agent's words. What the main conversation hands out to no leader is followed up by the background supervisor by default (a built-in conversation that gets the 督办 messages and reviews with task_review; the main conversation may too), and by the main conversation itself, as a leader would, when assistant.main_supervises_own_tasks is on. A conversation that cannot be sent a 督办 message leaves what it hands out to that same follow-up, and gets a one-line notice with the task's link when each one ends. The answer says who follows up. The answer lists running work that looks like the same task: check it before letting both go on. A todo this conversation opened with no agent yet (a Lark topic's, named in the message that handed it over, a todo_create part_of piece, or a failed start) is the task's own: give it as todo_id and no new todo is made. From the main conversation, supervisor hands the whole plan to a leader conversation (one of assistant.leaders), which supervises and reviews its tasks as if it had handed them out; leave it out for work of no direction. Every task gets a brief, kept from the moment it starts and never rewritten: the hub records the owner's message that led to it (their words, from this conversation; work you start on your own after a 督办 message, a notice or a routine is recorded as yours, with started_why), and you supply the goal, done_when, expect_minutes and out_of_bounds. A writable task (readonly false) without done_when (at least one checkable result) and expect_minutes is refused, and so is a read-only task without goal and expect_minutes; the refusal says what is missing. The card shows the brief, so the owner's 开始 confirms it. For rework, replacement or a retry on another computer, give continues and continues_why (see continues); a start that did not come up is retried with task_retry, also on another launcher. Apply owner-authorized scope changes with task_scope before planning and pass its scope_revision to reject staleness. Supply goal, authorization bounds, and new information; the manager appends effective criteria. A task that changes files sets readonly false with explicitly authorized writable permission (for example, Codex bypassPermissions), which also lets a replacement Agent on the same todo implement fixes. A read-only Codex sandbox cannot be changed in place: use a fresh writable process; with no permission change, a plain agent_send follow-up to the live Agent is fine. A task that only reads sets readonly true; its first message includes the read-only constraint. Claude-family launchers use bypassPermissions for both read-only and writable tasks; Codex uses plan unless assistant.readonly_mode=prompt (normal runtime permissions, not a security boundary). pi, cursor and agy have no read-only mode and are refused in either setting. role "leader" makes the task's agent a task leader: it hands direct workers to its own task through its own restricted task_plan and task_review and gets their supervision messages; only for a task that splits into several agents' work. Its workers cannot hand tasks on.

When repository identity needs resolving, consult repositories_list, and repository_get only when useful. The user's explicit repository URL or path takes precedence. A saved binding is a reference, not authorization or an allowlist; never infer a machine or checkout path from it. Non-repository tasks need no lookup; retrieval is not a mandatory dispatch preflight. Closing the terminal on acceptance is the owner's Start choice; this tool cannot set it.

Workspace policy: "new" makes an isolated checkout; {"new":{"branch":"task/my-change"}} names its branch; {"existing":"<cwd or workspace_id>"} selects one; "none" creates nothing. Omitted: readonly uses none; writable Git follows assistant.task_workspace_preference (prefer-existing by default, prefer-new for isolation), and an occupied cwd uses new. Explicit existing never switches silently; refusal names its task owner. For an explicit new branch already checked out, reuse requires a clean checkout, no live Agent and no unfinished-task owner; otherwise refuse with its path and known owner. The machine's engine owns creation, location and registration; never run git worktree add. The card previews the path and is editable before Start. Default branch: task/<todo id>. Creation failure starts no Agent. Workspaces remain after acceptance. Organization read-only policy refusals include their reason: follow it, never retry unchanged.

Optional `wait_seconds` observes this launch or input for at most 50 seconds after dispatch. The receipt is retained, followed by an `Inline observation` JSON record: `completed` with `reply`, `question` with `interaction_id`, or `timeout`, `cancelled`, `unavailable`, `interrupted` or `failed`. The record includes the input and session references when known. It never answers a question, accepts a task or resends input. Older nodes without input echo support may time out instead of returning an unrelated reply. Only an immediately dispatchable single-task plan waits; approval cards and multi-task plans return immediately. Continue to follow the normal supervision and `task_review` requirements.

```json
{
  "type": "object",
  "required": [
    "tasks"
  ],
  "properties": {
    "wait_seconds": {
      "type": "integer",
      "minimum": 0,
      "description": "Optional inline observation after dispatch, capped at 50 seconds; omit or 0 for the current receipt. Returns this input's completed reply or pending question reference. Timeout never resends or cancels work; normal supervision and task_review still apply."
    },
    "supervisor": {
      "type": "string",
      "description": "optional, main conversation only: the id of the leader conversation (assistant.leaders) whose direction this work belongs to; it supervises the tasks. Leave it out for work of no direction or of your own"
    },
    "tasks": {
      "type": "array",
      "minItems": 1,
      "maxItems": 8,
      "items": {
        "type": "object",
        "required": [
          "title",
          "prompt",
          "machine",
          "cwd",
          "readonly",
          "user_requested",
          "why"
        ],
        "properties": {
          "title": {
            "type": "string",
            "description": "the task in a short line the user reads on the card (Chinese, under 20 characters)"
          },
          "prompt": {
            "type": "string",
            "description": "the agent's first message: everything it needs to do the task without asking you"
          },
          "machine": {
            "type": "string",
            "description": "machine id; fixed target by default"
          },
          "machine_policy": {
            "type": "string",
            "enum": ["fixed", "flexible"],
            "description": "fixed (default): keep this machine through preview, dispatch and retries; fail there if unavailable. flexible: explicitly permit health-based placement on another computer, only for work that is portable"
          },
          "cwd": {
            "type": "string",
            "description": "the project directory: absolute path or ~/…"
          },
          "workspace": {
            "type": ["string", "object"],
            "description": "Task workspace policy, as the description says; omit for the default. existing takes an absolute/~/ path or a workspace id on that machine. No custom worktree root.",
            "oneOf": [
              { "type": "string", "enum": ["new", "none"] },
              { "type": "object", "required": ["existing"], "additionalProperties": false, "properties": { "existing": { "type": "string", "minLength": 1 } } },
              { "type": "object", "required": ["new"], "additionalProperties": false, "properties": { "new": { "type": "object", "additionalProperties": false, "properties": { "branch": { "type": "string" } } } } }
            ]
          },
          "launcher": {
            "type": "string",
            "description": "launcher name, one that machine has (a name it lacks is refused, listing the ones it has): a Claude Code launcher, or pi, cursor or agy to start that agent in herdr; empty: the default, or one the machine has when it lacks the default (claude first; the why says so)"
          },
          "model": {
            "type": "string",
            "description": "model id the agent takes (an alias such as opus, a gateway's own id, or a provider/model such as ag-chat/cursor/grok-4.7 for pi; letters, digits and . _ - / : @ [ ] only, or a leading ~ as in ~vendor/model-latest); empty: the launcher's default"
          },
          "permission": {
            "type": "string",
            "enum": [
              "default",
              "acceptEdits",
              "plan",
              "auto",
              "bypassPermissions"
            ],
            "description": "permission mode; empty: the default for writable tasks. Set an explicit writable mode (for example bypassPermissions) with readonly=false when a replacement Agent must implement fixes on the same todo. Read-only modes: see the description. traex and omp take none"
          },
          "role": {
            "type": "string",
            "enum": ["leader"],
            "description": "optional: leader makes this task's agent a task leader that may hand direct workers to its own task (any conversation may give it); leave it out for an ordinary worker task"
          },
          "ultracode": {
            "type": "boolean",
            "description": "true for a large task (many files, checks from several angles): Claude Code runs every substantial step as a multi-agent workflow, slower and costlier; false for small or quick work. Claude Code agents only (2.1.284 or later): any other agent, an older Claude Code or a node too old for it starts without it. Never put the word in the prompt or title instead"
          },
          "readonly": {
            "type": "boolean",
            "description": "true only for a task that must remain read-only; false for authorized file changes (a Codex read-only sandbox: see the description). The first message adds「本任务只读：不改项目文件、不提交、不对外写入」 only when true"
          },
          "user_requested": {
            "type": "boolean",
            "description": "true when the user asked for this very task in their latest message (told you to do it, to have an agent do it, to go ahead and start); false when it is your own idea or inferred from something they did not ask for"
          },
          "why": {
            "type": "string",
            "description": "one line (Chinese) on why this machine, directory and model"
          },
          "done_when": {
            "type": "array",
            "maxItems": 6,
            "items": {
              "type": "string"
            },
            "description": "what done means: results you will check before you accept it (Chinese, one each), e.g. 「PR 合并到 master」「浅色 / 深色截图贴进 PR」「线上 hub 已部署并验证」; the agent is told them. Required for a writable task (at least one); with continues the effective criteria from the original to-do apply and this is ignored"
          },
          "goal": {
            "type": "string",
            "description": "the work in one sentence, as the owner would say what they get from it (Chinese); required for a read-only task, the title when left out of a writable one"
          },
          "out_of_bounds": {
            "type": "array",
            "maxItems": 6,
            "items": {
              "type": "string"
            },
            "description": "optional: what the agent must not do or touch (Chinese, one each), e.g. 「不合并」「不动线上」"
          },
          "continues": {
            "type": "string",
            "description": "optional: the original todo id this task continues. Continuations always reuse this same todo and its original brief and effective criteria; never split work to a new todo. At Start, stop/release still-active prior Agents and reopen this todo if closed. Refuse unauthorized takeovers, an unreachable prior Agent that cannot be stopped, and legacy open successor duplicates. Failed starts keep this todo. Needs continues_why; if no permission change is wanted, agent_send can follow up the same live Agent"
          },
          "scope_revision": {
            "type": "string",
            "description": "with continues after a scope change: the exact scope_revision returned by task_scope; refuse planning if it is stale"
          },
          "continues_why": {
            "type": "string",
            "description": "with continues: one line on why the earlier task stopped or needs a replacement; kept on this continuation's plan"
          },
          "started_why": {
            "type": "string",
            "description": "when the owner did not just ask for this (your own idea, something a 督办 message or a routine led to): one line on why you start it; kept on the brief"
          },
          "attachments": {
            "type": "array",
            "maxItems": 8,
            "items": {
              "type": "string"
            },
            "description": "pictures and files the user gave you for it, by their absolute paths on your computer: a picture they sent (the path Claude Code gives it), a file the app put in their message. They are copied to the agent's computer and listed at the end of its message, and the text's mentions of them name the copies. Only those the user gave you in this conversation are accepted"
          },
          "expect_minutes": {
            "type": "integer",
            "minimum": 0,
            "description": "about how long the task should take, in minutes; required (above 0) unless continues is given. A task with no progress for 1.5× that (at least 20 minutes) counts as stuck, and one with no end after 3× that (at least a day) is raised to you as overdue"
          },
          "project": {
            "type": "string",
            "description": "optional: its knowledge-base project, an existing one's slug or name (kb_list projects), set when the area of work is clear; never a near-duplicate"
          },
          "part_of": {
            "type": "string",
            "description": "optional: the request to-do this is a piece of"
          },
          "after": {
            "type": "array",
            "items": {
              "type": "string"
            },
            "description": "with part_of: the pieces it waits for"
          },
          "todo_id": {
            "type": "string",
            "description": "optional: an open or failed-start todo this conversation opened or supervises, with no live Agent (stopped ones are released at Start). To stop a live Agent or reopen a done one use continues; given both, they name the same todo"
          },
          "inbox_item": {
            "type": "string",
            "description": "Exact handed-off Inbox id for related work only. Checked against this conversation and manager; links the task without filing or replying."
          },
          "tier": {
            "type": "string",
            "description": "An Agent preset: one of the tiers the snapshot's 模型档位 line lists for the profile in use, each with what it is for; pick the one whose description fits the work. Resolved when the task actually starts, with the profile in use then. Explicit launcher/model/effort override its values."
          },
          "effort": {
            "type": "string",
            "description": "Explicit effort override; omit to use the tier or purpose default."
          }
        }
      }
    }
  }
}
```
