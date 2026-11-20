Create a recurring routine, or pass `id` from list_followups to edit only the fields supplied. `enabled: false` pauses it. Use `time` with optional `weekdays`, or `cron` instead. Times use the user's zone; cron has 5 fields, runs at least 5m apart, supports the listed aliases, treats 0/7 as Sunday and restricted day fields as either. Seconds, @every and TZ= are refused; inspect the reply's next runs. `until` cannot be a bare clock time.

Runs inherit Main chat's launcher/model/effort. Omit overrides to keep them; an empty launcher returns to Main chat. Permission mode stays the user's choice. `inbox` is only for requested Inbox results. `fresh_each_run: true` starts a new chat per run with its state note. See field descriptions for push and end-time edit rules.

For approved work advancing a supervised to-do, pass its explicit `todo_id`; this conversation must supervise it. Omission keeps its association; "" clears it. From the second run Persona checks Agent answers, tool calls (not waits or repeats), criterion changes and progress reports; a long stall asks Change approach / Keep waiting, even with `push: asked`. Get owner consent before creating or changing recurring work.

```json
{
  "type": "object",
  "properties": {
    "todo_id": {
      "type": "string",
      "description": "explicit supervised to-do this routine advances"
    },
    "fresh_each_run": { "type": "boolean" },
    "id": {
      "type": "string",
      "description": "to change an existing routine"
    },
    "title": {
      "type": "string",
      "description": "short name shown in the app, e.g. 早报"
    },
    "prompt": {
      "type": "string",
      "description": "what to do each time, self-contained"
    },
    "time": {
      "type": "string",
      "description": "HH:MM, 24-hour"
    },
    "weekdays": {
      "type": "array",
      "items": {
        "type": "integer",
        "minimum": 1,
        "maximum": 7
      },
      "description": "1 = Monday … 7 = Sunday; omit or empty = every day"
    },
    "cron": {
      "type": "string",
      "description": "instead of time / weekdays: minute hour day-of-month month day-of-week in the user's time zone, e.g. */30 9-18 * * 1-5 or 0 10 1 * *; or @hourly / @daily / @weekly / @monthly / @yearly"
    },
    "enabled": {
      "type": "boolean"
    },
    "report": {
      "type": "boolean",
      "description": "a daily report (such as a leader conversation's): each run opens with Persona's north-star lines (today's interruptions and availability, the change against yesterday until the same time, the source), which the report starts with. Omit to keep an existing routine's"
    },
    "push": {
      "type": "string",
      "enum": ["every", "asked"],
      "description": "every (default for a new task): each run's answer is pushed to the user; asked: only what a run sends with notify_user, for a task that watches something (it then runs in its own conversation). Omit to keep an existing task's"
    },
    "inbox": {
      "type": "string",
      "enum": ["off", "worthy", "every"],
      "description": "what of a run goes into the user's Inbox, independent of push: off (default for a new task), worthy (a run puts a row there with inbox_post only when it finds something worth a look; for a task that watches something), every (every run ends with a row made by Persona; for a briefing the user follows daily). Set it only when the user asked for it. Omit to keep an existing task's"
    },
    "run_if": {
      "type": ["object", "null"],
      "description": "Optional owner-approved shell check on the routine computer before scheduled runs. Exit 0 runs; exit 1 skips. Other errors follow on_error. Omit to keep it or use null to remove it.",
      "properties": {
        "command": {"type": "string", "maxLength": 4096},
        "timeout_s": {"type": "integer", "minimum": 1, "maximum": 60, "default": 30},
        "on_error": {"type": "string", "enum": ["run", "skip"], "default": "run"}
      },
      "required": ["command"],
      "additionalProperties": false
    },
    "until": {
      "type": ["string", "null"],
      "description": "optional end time (RFC 3339 or 30m / 2h / 1d). After it, Persona turns the task off and records a skipped run instead of running. Omit to keep an existing task's end time; null clears it. Ignored for a one-off."
    },
    "launcher": {
      "type": "string",
      "description": "this routine's own launcher, instead of Main chat's (a name on the computer where its runs start). Empty clears the override and follows Main chat again. Omit to keep an existing routine's"
    },
    "model": {
      "type": "string",
      "description": "model id for the launcher above; without a launcher it pins the launcher the routine runs on now. Empty uses the launcher's default. Omit to keep an existing routine's"
    },
    "effort": {
      "type": "string",
      "enum": ["low", "medium", "high", "xhigh", "max"],
      "description": "thinking effort for Claude launchers; without a launcher it pins the launcher the routine runs on now. Omit to keep an existing routine's; empty clears it"
    }
  }
}
```
