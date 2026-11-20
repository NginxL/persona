# Scheduled execution and integration lessons

Task-specific scheduling, runtime-directory and editor pitfalls, plus verification lessons from integrating this feature.
Keep Task dispatch helpers in Task-owned modules and leave shared scheduler and Node entry points as orchestration.

## Assistant scheduling and delivery
- Localized project prompt goldens must carry the template's existing source-state facts, including the empty project.
  Regenerate only the affected locale and case, inspect the diff, and retain the original no-language goldens,
  prompt body and comparison assertions.

- Context API changes must cover the empty branch and real prompt callers. A project context paired with project
  slugs returns both empty values when no project matches; a new supervising-conversation argument is also passed by
  language golden tests (empty when there is no supervisor). Keep the prompt and golden assertions unchanged, then
  compile and run the caller checks together.

- A selected schedule copy is not its admitted execution snapshot (#6800). Initialize to-do progress history after
  reading the task and model together, including failed admission. A new or cleared association, or a change to a
  one-off, must describe that admitted task rather than the scheduler's older copy. Test those edits before any send.




- A task directory cannot replace a space's authority (#6800). Passing a resolved `cwd` to a shared start helper
  skipped Health preparation while retaining its Health label. The node classifies its process and outbound gates
  from the actual startup directory, so a label alone cannot preserve isolation. Reject Health task directory
  overrides after sparse edits and before executing stored tasks; always prepare the Health directory at shared
  startup. An old source chat's label can also be wrong: compare its actual directory with fresh node preparation
  and enforce that directory on resume, replacing it before delivery when it cannot be verified. Other execution
  choices can remain supported. Test actual prepare instructions and outbound sends, then
  a fake node process's Health environment and single-use phone confirmation even with an allow rule and after `cd`.

- Removing a model runtime upstream also changes a task's separate resolution and picker defaults (#6800).
  Resolve the type and protocol conflicts without restoring the removed path or dropping strict task sends.
  Keep an old run snapshot's runtime only as compatibility evidence so that task starts fresh; new Claude runs
  follow the CLI-only policy. Exercise stored-task reconstruction, outbound parameters and picker defaults, and
  adapt retry fixtures to the current fake CLI while retaining their same-model and no-fallback assertions.
## An old-node capability refusal happens before delivery (#6800)

The device layer returns `node_outdated` before dispatching unsupported requests. A source-chat task may safely fall
back to its own conversation on that typed refusal. Keep it distinct from timeouts, closed links and unclassified
errors, which cannot prove that a turn never started and must not cause replacement sends.

## A new conversation's lost reply is not an unstarted task (#6800)

`startConversation` can fail after `assistant.send` reached the node. Treating every error as a one-off that never
started arms a delayed duplicate, even when source and resumed sends already refuse replacement. Use the existing
`sendUnknown` delivery classification for the new-chat path too: retain the failure notice without retrying a possibly
accepted turn. Persist `delivery_unknown` so a later manual send with a lost receipt cancels older pending retries too.
Keep `rpc.ErrNotSent` and preparation failures retryable, and notify on a failed task-owned resume as well. A virtual-clock
test must advance past the retry delay; checking only the immediate send count misses this failure.

A fixed-facts briefing fallback must also cover task/model admission before ordinary fact preparation. Capture that
failed run's fresh facts before its early return; a previous run's cached facts are not its snapshot. Keep this
confirmed pre-delivery path separate from unknown delivery, which must neither resend the turn nor start a fallback.

## A schedule grant must bind to the model record its round consumed (#6800)

Task model overrides live in their own settings record. Hashing only the task row misses model-only and effort-only
edits when the execution computer is already pinned. Compute configured grants from the effective provider, model,
effort and runtime configuration, checking the candidate record in atomic saves and the authoritative record at use.
Keep the legacy hash for untouched inheritance. Freeze that same basis with the run's actual model snapshot and its
pending plans: an old conversation cannot adopt a new model's grant. Test real sends, owner authorization, edits,
clears, service reconstruction and automatic dispatch, rather than copied model fields or hash equality alone.

## A pre-run check must use the task's selected computer (#6800)

An explicit execution computer or directory is also part of the schedule's start-grant basis. An edit must revoke
the old grant and keep a pending plan from granting the changed schedule. Preserve the old basis when both fields
are empty, so an upgrade alone does not revoke legacy grants. The atomic task-and-model save must run the same
stale-grant cleanup as the ordinary schedule save; otherwise a prompt edit together with a model edit retains a
grant that can revive when the old prompt returns.

A task's last run and the assistant default can point to different computers from its explicit execution target.
Run the pre-run command on the explicit target first, falling back to the historical run and assistant only when no
target was chosen. Cover both a first run and a task reconfigured after an earlier run; a skip result must still
prevent the model send.

A local launcher name is not a portable provider identity. Resolve task inheritance on its execution computer before
merging sparse overrides; a local alias there takes its own model defaults, while a shared managed provider can retain
its configured binding. Include target identity metadata in the execution fingerprint. Explicit overrides still fail
when unavailable, and changing only the computer must not pin a copied default.

Saving an explicit model or effort override must retain both the provider and the computer on which it was validated,
even when the choice equals today's default. The execution editor supplies `model_revision` for that intent; an old
unversioned launcher/effort-only echo with the same effective binding must preserve inheritance instead. Compare the
effective patched and stored bindings before normalizing, and keep explicit empty values as clears. Normalize before
comparing the model record; persist the computer even when the model record itself is unchanged. Preserve untouched
inheritance. Test after changing the
Assistant defaults and reconstructing the service, asserting the actual send target as well as storage. A fake's
allowed-computer list must include the replacement, or its settings fallback silently undoes the test's default change.

Do not infer catalog uncertainty from the number of entries. An explicit successful empty task catalog confirms
absence; a failed read or omitted/null catalog does not. Carry that distinction through the node-list merge and check
the task's exact launcher before sending, including configured wrappers and managed providers. An unreadable managed
catalog is also unknown. Test zero sends for authoritative deletion and incompatibility, alongside preserved exact
bindings for older or unreadable discovery. Keep task-only inheritance behind an explicit picker purpose so ordinary
Assistant defaults do not change. In the editor, scope every default and catalog read to the same chosen manager as
the task write, and include that manager in response keys as well as effect dependencies.

Exercise an undiscovered launcher's first save as well as a seeded run. A placeholder only in the run path can hide
the generic validator's global-provider rejection in the save path. Share that task-specific validation between both
paths, and retain negative checks for model validation, advertised effort and managed-provider computer scope.
Legacy source-chat launcher/effort metadata was inert: permit unchanged saves and source resumes, while refusing
new overrides and any stored explicit model, directory or computer. Do not let raw request-key presence decide intent.

A runtime's session inventory is authoritative even without a local Claude/Codex state record. Strict task resumes
validate the owning assistant-capable runtime, launcher and physical directory before sending; local id shape and state
cannot classify a runtime-owned session as gone. Missing metadata for a known owner is uncertain, so it must fail without
triggering a replacement send. Regress both runtime-shaped and UUID sessions, mismatches, missing sessions and lost replies.


## Runtime directory ownership

## Runtime memory and task-directory preparation are separate (#6800)

A runtime's first assistant turn must read the same merged memory as Claude and Codex. Keep that read through
`memoryForUse`, including the index sent in the first turn. It does not make a user-selected task directory an
assistant-owned directory: sync generated runtime skills only after `assistantDirectoryPrepared`. Cover both the
actual runtime send with a note from another profile and strict task resumes whose selected directory stays untouched;
either side alone misses a merge regression.

## Integration verification

- A source-based desktop fixture must delimit the setup code at its current loader. After the packaged setup page
  moves from `loadFile` to its registered URL scheme, update that boundary while retaining every recovery assertion.

- A mocked hook's mutable object does not notify React. Render again after changing its loading or streaming state,
  so an unrelated pending import or timer cannot decide whether the test exercises that state. Keep the original
  foreground/background assertions; a deterministic render can expose a real missing Processing fallback. When the
  answer itself streams, include that existing feedback in the activity assertion rather than requiring a second spinner.
- A fixture's CLI result file can precede its caller's recovery bookkeeping. Await the observable lost-task record
  clearing with the existing bounded helper, while retaining the origin, permission, tool and no-stall assertions.
- Executable trust includes every path ancestor. Use a protected, user-owned executable directory and a short compiler
  path; do not relax ownership checks or change shared directory owners.
  A child mount namespace can change the UID seen for system ancestors: inspect that mapping before running executable
  trust tests there. For tests that require a short `/tmp`, an isolated child mount can supply fixture capacity without
  changing the host's temporary files. Record denied native syscalls and nested-namespace skips as coverage gaps.

- Cache-age fixtures must settle every startup file the probe watches. Settling only `.zshrc` while the same fake
  home just created `.bashrc` legitimately bypasses the cache; a fast temporary filesystem exposes that timing
  assumption. Date only the fixture's files with the existing helper, keeping retry/freshness limits and assertions.

- A fixture exercising a new read must answer with the real contract: register the same synthetic computer in the
  fleet and transcript provider, and return a complete Assistant snapshot when an unlisted chat triggers a refresh.
  An empty operation receipt is not a snapshot; do not weaken the registration or model-selection assertions.

- Match a flaky full-suite failure to its actual scheduling boundary (#6800). A direct import of an image gate can
  finish before React.lazy's own request; after rendering, await `vi.dynamicImportSettled()` inside `act`. A list test
  asserting the state before its next idle batch must hold and release idle callbacks explicitly: wall time between
  `act` calls can fire the 50 ms fallback and mount another batch. Check that unmount cancels pending callbacks too.
  Collect cold component graphs and language-specific search fixtures before timed assertions, retaining fresh stores
  per test and separate module registries per language. Build those language fixtures sequentially, since
  `vi.resetModules()` changes the registry. For a paged conversation, wait for its observed message request, not a fixed
  number of microtasks. Preserve the assertions and deadlines; compare the same fixed main and candidate under the
  same ordering, and distinguish naturally reproduced failures from deliberately delayed imports or responses.
  After merging a newer main, rerun the normal production build under the branch's committed byte limit.
  An upstream budget increase does not prove that stricter limit. Keep a deferred confirmation and its failure
  rendering in the same existing chunk; regress cancellation, retry and failed chunk loading before measuring again.
  At 2× text, check every failure-dialog choice against the viewport: a clipped Cancel can be missed by a page-level
  horizontal-overflow check. Let the action row wrap while each button stays on one line.
  A Markdown image fixture must reach the component it asserts: once the reply-picture plugin loads, block images
  use the gallery, while inline images keep the external-image gate. Preload that plugin and use an inline fixture
  for the gate's privacy/retry assertions; otherwise an idle import makes the same fixture change components.
  Freeze the Date clock at local noon when a formatting case assumes a recent message is from today. One minute
  before 00:00 belongs to yesterday and correctly carries a date; do not loosen the same-day/date assertions.
  A runtime gate fixture must prepare the assistant directory through `assistant.prepare` before sending, as the
  hub does. Merely writing a user skill does not authorize copying it into an unprepared task directory; keep that
  boundary and the gate's read, refused-write and conversation-ownership assertions intact.
  For scheduled delivery, distinguish a real RPC peer closed before its write (`rpc.ErrNotSent`) from one closed
  after accepting the request. Wrap the actual errors and advance a `synctest` clock past the production retry delay;
  count attempts and effects separately, including persistent failure and uncertain delivery. Keep scheduled turns
  and their completion watchers in the same clock bubble. A busy-process fixture must acknowledge its current mode
  and effort, or an unrelated control request can obscure the model-compatibility boundary being tested.
  Resume guards must apply the sender's launcher inheritance before comparing execution contexts. Exercise an
  omitted launcher through both the actual send and a runtime peer, including a session row without a launcher.
  Keep stored-launcher precedence and reject a stale stored launcher belonging to another connected runtime;
  accepting the listed owner alone can validate one runtime while the sender routes to another.
  A transport fixture for scheduled execution must mark its computer online, answer the exact synthetic model
  probe, and recognize a listed source session when `resume_only` is sent. Otherwise setup failures hide the
  category/inheritance behavior it asserts. Keep the original send-parameter and refusal assertions.
  Trusted-adapter fixtures also validate their test executable's ancestors: use private temporary directories
  and an owner-only process umask, since a group-writable Go build directory or test binary is correctly refused.
  An empty external allocation still hands its initialized process off from the startup RPC before returning.
  Regress cancellation of that RPC followed by an acknowledged control and the first real message on the same
  process and pinned binding, with no early model input or context receipt. A new empty-start return can otherwise
  report success just before deferred startup cleanup stops its native process.
  When a model catalog folds aliases into one row, task saves and stored runs must still enforce that row's advertised
  thinking levels, and the editor must use its default effort without rewriting the saved alias. Exercise an actual
  folded catalog through rejected saves, refused dispatch and a supported choice; checking only the canonical ID misses it.

## Web editing and model selection

## A new settings tab needs its deep-link entry

Keep `DEEP_SETTINGS_TABS` aligned with the settings navigation when adding a tab. A missing entry sends an app link
to the default Settings page even though direct navigation offers the tab. Keep the existing index consistency test
and check the actual app-link round trip; unlisted query keys still must not change settings or carry credentials.

## A retained task draft must accept canonical pins field by field (#6800)

Run now saves before sending. A title edit during that save makes the draft a new object; accepting only server
metadata then leaves canonical provider/computer pins out of the open form. A later unrelated save can clear them.
Compare each execution field with the submitted snapshot: accept the canonical value only if that field was not
edited in the meantime. Test save, a pending title edit, run failure, changed defaults and another save in the same
form; reopening first masks the bug.

A source task's old launcher/effort-only metadata is not a new execution choice. Pass the saved task to both the form
validator and the execution-options notice, preserve sparse title saves, and still block changed choices. Otherwise
the server can accept the compatibility case while the editor's Save button or duplicate notice still refuses it.

A preset description and a truncated model's full-ID line still need their launcher name. With no current launcher,
all groups start collapsed: an identity-comparison test must search or expand the groups before asserting its rows.
Keep the launcher-identity assertions; do not restore an obsolete always-expanded view just to expose the fixture.

## A failed launcher catalog must not disable inheritance (#6800)

A task's saved launcher can outlive discovery. Keep that value visible and preserve its model/effort until the user
selects Assistant inheritance; disabling the whole select also disables the recovery choice. Clearing explicitly sends
empty launcher/model/effort values. Test failed, empty and omitted catalogs through save and reopen.

Model and launcher discovery belong to the exact computer/launcher/request context. Clearing a list in an effect alone
does not fence an older successful response. Hide mismatched catalogs during render, guard completed requests after
cancellation, and disable model picking while the current read is pending. A new read's failure must not revive old rows
or disable the launcher inheritance recovery control. Test deferred responses arriving before and after that failure.

A component shared by two lazy screens can add a chunk and preload entries to the startup bundle even when none of
its component code loads at startup. Sharing the scheduled-task model picker with Settings added 95 B against a
main with no room. Load Settings' picker when its editor opens so the first screen need not carry that dependency.

The scheduled-task editor can stay open after Save then Run fails. Its save response must accept canonical execution metadata and the state note without replacing ordinary edits typed while the request was pending. Regress that sequence with a deferred save, a later title edit and a failed run, then save again and check that the new title survives without resubmitting an unchanged note.
