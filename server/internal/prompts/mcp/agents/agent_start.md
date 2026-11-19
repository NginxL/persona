Start a new agent in the terminal engine (herdr or Orca) of one of the user's computers, in a project directory, with a first prompt. Use for real development work instead of doing it here. Check machines_list before choosing a machine. Returns the new session id. When the launcher failed its last health check on that machine, the agent starts on a machine where it works (a directory under the home moves to that home) and the answer says so. Claude-family launchers and Paseo providers with Auto Accept always start in bypassPermissions; traex and omp: no effort.

The launcher decides the agent. A Claude Code launcher starts Claude Code. A launcher named pi, cursor or agy (or one the machine lists as that agent) starts that agent in a herdr pane with the model you give: pi, the Cursor CLI (cursor-agent) or the Antigravity CLI (agy). pi's first message goes through its Persona extension; cursor's and agy's go through herdr once it recognises the agent. Those three take no permission mode and no ultracode, and their effort is their own.

When the work needs a branch and checkout of its own (work in parallel with what is already open in that repository), give `worktree.branch`: Persona makes the worktree through the machine's engine first (herdr's or Orca's own, so it is listed there and in the app, under the workspace `cwd` is in) and starts the agent in it. Do not tell the agent to run `git worktree add` itself: a worktree made that way is invisible to Orca and to the app. An existing local branch is checked out; a new one starts at the HEAD of the workspace `cwd` is in. A branch already checked out elsewhere is refused.

When the agent needs credentials the user keeps in a secret provider (a token in 1Password, say), give `secret_env`: each environment variable it should have, with the reference its value comes from (such as `op://vault/item/field`), never the secret itself. The user approves the hand-off on a card on iPhone or the web before anything starts, and the values reach only that agent's environment, for this run: you never see them, and the answer lists each variable as granted, declined, expired or failed. Only Claude Code agents on a computer this manager connects take them. Tell the agent the variables exist; never ask it to print them.

Give `ultracode: true` for a large task: one that spans many files or needs checking from several angles (design, tests, review). Claude Code then runs every substantial step as a multi-agent workflow: slower and costlier: off for small work. Do not write the word "ultracode" into the prompt instead. Claude Code agents only (2.1.284 or later): any other agent, an older Claude Code or a node too old for it starts without it, and the answer says so. Once Claude Code confirms it, agents_list shows `ultracode=on`. An organization policy may keep some conversations read-only: a call from such a conversation is refused with the policy's reason. Do not retry; follow the refusal.

`wait_seconds` observes up to 50 seconds after dispatch, appending `Inline observation` JSON: `completed`(this input's `reply`), `question`(pending `interaction_id`), `timeout`/`cancelled`/`unavailable`/`interrupted`/`failed`, with known input/session ids. Observation never answers questions, accepts tasks, resends or cancels work; normal supervision/`task_review` applies. Older nodes may time out rather than return an unrelated reply.

```json
{
  "type": "object",
  "required": [
    "machine",
    "cwd",
    "prompt"
  ],
  "properties": {
    "wait_seconds": {
      "type": "integer",
      "minimum": 0,
      "description": "0/omitted: normal receipt"
    },
    "machine": {
      "type": "string"
    },
    "cwd": {
      "type": "string",
      "description": "absolute path or ~/…; with worktree, a directory of the repository to branch from"
    },
    "prompt": {
      "type": "string"
    },
    "attachments": {
      "type": "array",
      "maxItems": 8,
      "items": { "type": "string" },
      "description": "absolute paths of pictures/files the user gave in this conversation (Claude Code path or app attachment); copied to the agent, appended to its message with mentions updated; others refused"
    },
    "launcher": {
      "type": "string",
      "description": "listed launcher (Claude Code, pi, cursor or agy); unknown names refused with choices; empty: default/fallback, claude first, answer says which"
    },
    "secret_env": {
      "type": "object",
      "maxProperties": 8,
      "additionalProperties": { "type": "string" },
      "description": "NAME → secret-provider reference (op://vault/item/field), never a value; card approval first"
    },
    "ultracode": {
      "type": "boolean",
      "description": "Claude Code multi-agent workflows for substantial steps; default false"
    },
    "worktree": {
      "type": "object",
      "description": "engine-made worktree of cwd's repository",
      "required": [
        "branch"
      ],
      "properties": {
        "branch": {
          "type": "string",
          "description": "the branch to check out (made from the workspace's HEAD when it does not exist)"
        }
      }
    },
    "tier": {
      "type": "string",
      "description": "Agent preset from the snapshot's 模型档位 line; choose by purpose; resolved before approval and shown on the card; explicit launcher/model/effort/speed overrides"
    },
    "speed": {
      "type": "string",
      "enum": ["standard", "fast", "ultrafast"],
      "description": "new-start speed; explicit choice overrides the tier. standard/omitted: launcher default. Codex: fast, ultrafast; Claude Code: fast only"
    },
    "effort": {
      "type": "string",
      "description": "Explicit effort; omit for tier/purpose default."
    },
    "model": {
      "type": "string",
      "description": "Explicit model id or alias the agent takes; omit for the tier or purpose default."
    }
  }
}
```
