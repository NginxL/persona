Verify handed-out work. accepted closes the task. Its terminal stays by default. If the owner opted in when starting this task, accepted with outcome done can close its original idle tab after acceptance is saved. Use the tool's cleanup result (closed, skipped, or pending). Do not ask for a separate close approval or try to close the tab yourself. rework sends gaps to that Agent: at most 2 rounds; a third requires a complete owner decision. A stopped Agent is read-only: replace with agent_start. External waits use parked with source evidence, not needs_user. Only answerable owner-only decisions use needs_user: why_owner, reason, recommend, 2–4 options (recommended first, each with leads_to), cost_of_waiting. Otherwise act within your authority, rework or close.

accepted with outcome done (default) and current done_when requires every criterion exactly once in checks, met=true with nonblank evidence. Refuse missing/duplicate/unknown/unmet checks before writing; user_check cannot bypass this. outcome cannot, cancelled and no-criteria tasks are unchanged. Other verdicts may omit checks without erasing progress. Report each criterion as met, not met or dropped (logged scope change), then also_done. Judge the scoped brief, not a later handover or the Agent's word; review every task supervision asks you to.

Supervised accepted/needs_user send one Lark conclusion card (verdict, reason, link; needs_user adds recommend, selected by “OK”); do not duplicate it. Give accepted's outcome/reason/link or needs_user's reason/recommend. Acceptance passes an existing “Please verify” card; user_check means the owner must still inspect. needs_user defaults to recommend after 24h/deadline except irreversible or visible_to_others: never default those, remind daily. Recheck once at 24h. can_wait requires nobody blocked today, low waiting cost and no deadline within 12h (see schema). “You decide”, “This isn't what I asked for”, “Tell me the background first” return to supervision: don't repeat the question or take irreversible/external action.

A non-main conversation reviewing another supervisor's task may only accept/cancel; send gaps with agent_send. A leader's needs_user reaches main too. Cancel only duplicates/unwanted tasks; their Agents close/archive and waiting cards end.

```json
{
  "type": "object",
  "required": ["todo_id", "verdict"],
  "properties": {
    "todo_id": {
      "type": "string",
      "description": "the task's todo id (todo_…), from the 督办 message or the context snapshot"
    },
    "verdict": {
      "type": "string",
      "enum": ["accepted", "rework", "needs_user", "parked", "cancelled"]
    },
    "checks": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["criterion", "met"],
        "properties": {
          "criterion": {"type": "string", "description": "one done_when criterion (or what the task was for, when it had none)"},
          "met": {"type": "boolean"},
          "evidence": {"type": "string", "description": "what shows it is met (a link, a command's output), or what is missing"}
        }
      },
      "description": "each criterion, met or not, with its evidence or gap; updates that done_when entry's persisted state, evidence, time and supervisor identity. Omitted entries keep their progress"
    },
    "note": {
      "type": "string",
      "description": "rework: what the agent is to do (Chinese, concrete); needs_user: what the user is to decide; parked: anything more than reason, for your own record; cancelled: why; accepted: the result in a line"
    },
    "outcome": {
      "type": "string",
      "enum": ["done", "cannot"],
      "description": "accepted: done (the work is done or the answer is yes), or cannot (the task found it cannot be done); default done"
    },
    "reason": {
      "type": "string",
      "description": "accepted: the conclusion card's one sentence of why, in the user's language; default the note's first line. needs_user (required): one sentence saying what is to be decided, in the user's language. parked (required): one sentence saying what the task waits for, in the user's language; waiting on a PR: include its link"
    },
    "waiting_on": {
      "type": "string",
      "enum": ["someone_else", "external_event", "date"],
      "description": "parked (required): the external wait. someone_else: another person must act or answer; external_event: an outside event must happen; date: wait only until a date. New owner decisions use needs_user; legacy owner_priority parks remain readable and retain their digest behavior"
    },
    "until": {
      "type": "string",
      "description": "parked, optional: duration (\"3d\", \"36h\", \"90m\") or RFC 3339 time, 1 minute to 30 days ahead; default 24h. For waiting_on date, give an explicit RFC 3339 time (no duration or default). At that time the task returns to supervision"
    },
    "link": {
      "type": "string",
      "description": "accepted and needs_user: the one link to the evidence (the PR, the page, the document); default the task's PR, a link in the note or checks, or the agent's conversation"
    },
    "recommend": {
      "type": "string",
      "description": "needs_user (required): the option you recommend, the label of the first of options; the user's \"OK\" takes it and it goes to the task's agent"
    },
    "why_owner": {
      "type": "string",
      "enum": ["irreversible", "visible_to_others", "priority", "acceptance", "only_owner_knows"],
      "description": "needs_user (required): why only the user can decide. irreversible: a step that cannot be undone (deleting, a production release or write, spending money); visible_to_others: something other people will see (a message, a comment, a review request); priority: whether to do it or what comes first; acceptance: a result they have to judge by eye; only_owner_knows: a fact only they have. Anything else is yours to decide"
    },
    "options": {
      "type": "array",
      "minItems": 2,
      "maxItems": 4,
      "items": {
        "type": "object",
        "required": ["label", "leads_to"],
        "properties": {
          "label": {"type": "string", "description": "the option in a few words, as a button says it"},
          "leads_to": {"type": "string", "description": "one sentence: what choosing it leads to"}
        }
      },
      "description": "needs_user (required): 2 to 4 options the user chooses from, the recommended one first"
    },
    "cost_of_waiting": {
      "type": "string",
      "description": "needs_user (required): one sentence saying what it costs if the user does not answer soon"
    },
    "can_wait": {
      "type": "boolean",
      "description": "needs_user, optional: true when the decision can wait for the next daily briefing (no deadline within 12 hours, a low cost_of_waiting): it is not pushed and has no card, and is in the user's Needs you at once. Ignored (the answer says why) for why_owner irreversible and visible_to_others, a deadline within 12 hours or with reversible false, and when no daily briefing with facts is due within 24 hours and at least 12 hours before the deadline (24 hours by default: give a longer deadline for what can wait longer); default false (pushed as usual)"
    },
    "deadline": {
      "type": "string",
      "description": "needs_user, optional: until when the decision waits, a duration (\"4h\", \"90m\") or an RFC 3339 time, 1 minute to 30 days ahead; needs reversible. Default: 24h and reversible, except for why_owner irreversible and visible_to_others, which have none"
    },
    "also_done": {
      "type": "array",
      "maxItems": 6,
      "items": {"type": "string"},
      "description": "accepted, optional: what was done beyond the task's brief (one line each); the task's page lists it after the criteria"
    },
    "user_check": {
      "type": "boolean",
      "description": "accepted only: the user is still to look at the result themselves (a UI change someone has to see): a \"Please verify\" card waits for their \"Pass\" instead of your acceptance passing it; default false"
    },
    "reversible": {
      "type": "boolean",
      "description": "with deadline: whether taking recommend can be undone later. false for deleting, sending anything to other people, spending money or changing production: those are never taken by default (why_owner irreversible and visible_to_others must say false)"
    },
    "evidence": {
      "type": "object",
      "required": ["kind", "target", "wake"],
      "properties": {
        "kind": {
          "type": "string",
          "enum": ["person_reply", "merge_review", "external_event", "date"],
          "description": "parked: person_reply (a sent request to a person), merge_review (a sent request for review of a ready PR/MR), external_event (an observable outside event), or date"
        },
        "target": {"type": "string", "maxLength": 200, "description": "the named person/reviewer or external object"},
        "wake": {"type": "string", "maxLength": 600, "description": "the observable change that ends this wait"},
        "url": {"type": "string", "maxLength": 2000, "description": "required for merge_review (ready PR/MR link) and external_event (observable object URL); omit for person_reply and date"},
        "contact": {
          "type": "object",
          "required": ["channel", "url", "sender", "sent_at", "request", "status"],
          "properties": {
            "channel": {"type": "string", "maxLength": 100},
            "url": {"type": "string", "maxLength": 2000, "description": "sent-message/receipt link, not a channel homepage"},
            "sender": {"type": "string", "maxLength": 200},
            "sent_at": {"type": "string", "format": "date-time", "description": "actual sent time; not in the future"},
            "request": {"type": "string", "maxLength": 600},
            "status": {"type": "string", "enum": ["sent"]}
          },
          "description": "required for person_reply and merge_review: the request has already been sent"
        },
        "ready": {"type": "boolean", "enum": [true], "description": "merge_review only: the named reviewer can review the PR/MR"},
        "reply": {
          "type": "object",
          "required": ["source", "brand", "app_id", "account_open_id", "person_open_id", "chat_id", "thread_id", "request_message_id"],
          "properties": {
            "source": {"type": "string", "enum": ["larkcli"]},
            "brand": {"type": "string", "enum": ["feishu", "lark"]},
            "app_id": {"type": "string"},
            "account_open_id": {"type": "string"},
            "person_open_id": {"type": "string", "description": "expected human OpenID in this app, not self"},
            "chat_id": {"type": "string"},
            "thread_id": {"type": "string", "description": "provider topic ID, not root-message ID"},
            "request_message_id": {"type": "string"}
          },
          "description": "person_reply: exact IDs from a configured lark-cli reader, never names. contact.url must be the sent request's matching topic AppLink. A verified reply asks the current supervisor to review, not direct Agent delivery"
        }
      },
      "description": "required for parked. Inspect the source receipt: this records your attestation, not a provider check. Do not supply recorded_by (current conversation, server-authored/output only). Legacy missing evidence is unrecorded, not verified"
    }
  }
}
```
