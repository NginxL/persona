# Knowledge prompt language migration (#6272)

Audience: people reading the knowledge base and the Assistant's explanations. New
non-recording explanations follow the owner's hub language, with Chinese as the
legacy default. Names, quotations, identifiers, dates, source content and confirmed
facts remain in their recorded language. Changing the interface language does not
schedule a refresh, translate a stored page, or change permissions.

## Inventory and bounded phases

The inventory preceded implementation. Phase one traced every render caller and
include, source-language precedence and text parser. Phase two localized the
non-recording model calls and their input wrappers together, with one locale
snapshot per call. Phase three covered the live digest, compatibility, persistence
and parsers. The existing recording-language paths remain under their own contract
(#5906, #6011, #6045); this migration does not redo them.

| Templates under `server/internal/prompts/kb/` | Production callers and classification |
| --- | --- |
| `compress`, `compress_input` | `kb/compress.go`: new chat summary, one-line explanation, tags, project updates and people notes use owner language. Source messages, existing summaries and pinned facts are data. Notification priority and project eligibility rules remain intact. |
| `project`, `project_input` | `kb/project_refresh.go`: a normal dirty-project refresh writes new explanations in owner language, preserving source names and quotes. `kb/project_commit.go` also renders the input to compare frozen source snapshots; that comparison uses the call's captured language even if the preference has since changed. Source changes still reject the model's stale answer. |
| `items`, `items_input`, `known_projects_localized` | `kb/items.go`: new tagging notes use owner language; item IDs, titles, timestamps and known project names remain exact. The localized project-list wrapper is used only by non-recording calls; the recording list retains its original contract. |
| `dedupe`, `dedupe_input`, `dedupe_side` | `kb/dedupe.go`: new reason uses owner language, while identity rules, `same`/`sure`/`keep`, source names and conservative refusal are preserved. The existing hedge parser now recognizes English and Japanese uncertainty as well as Chinese. |
| `moments_link`, `moments_link_input` | `kb/moments_link.go`: new relationship notes use owner language. Post IDs, source text and the conservative relevance decision remain unchanged. |
| `topic_distill`, `topic_distill_input` | `kb/topic_distill.go`: new explanations use owner language; existing topic names, paths, source evidence, dates and identifiers remain data. Kind enums, count/length limits, exclusions and provenance tracking remain intact. |
| `topic_merge`, `topic_merge_input` | `kb/topic_merge.go`: new notes/point explanations use owner language. Unchanged existing point fields are omitted from updates, rather than translated. Owner-confirmed facts, chronology, candidate/source attribution, enums and limits remain authoritative. |
| `digest` | `kb/items.go` `Engine.Prompt`, called by `assistant/context.go`: transient Assistant snapshot headings follow owner language. Stored names and one-line summaries are inserted verbatim; no file is written. |
| `topic_summary` | `kb/topics.go`: deterministic persisted projection of source points, including their existing category labels. It stays in its recorded form; changing interface language must not rewrite it or its points. No model call or owner locale is introduced into the store. |
| `readme` | `kb/store.go`: stable data-layout reference created only if missing, before an Engine/owner-language provider exists. It is stored reference material, not a newly generated explanation. Existing and new store layout/README semantics are preserved. |
| `recording`, `recording_title`, `minutes`, `minutes_merge`, `day_recap`, `day_recap_merge`, `write_language` | `kb/recordings.go`, `recording_title.go`, `minutes.go`, `day_recap_write.go`: already choose recording language, then owner language, then Chinese. No change to these generation instructions or their language precedence. |
| `known_projects`, `recording_input`, `recording_title_input`, `minutes_input`, `minutes_meeting`, `minutes_merge_input`, `minutes_card`, `minutes_marks`, `day_recap_day`, `day_recap_input`, `day_recap_merge_input` | Inputs/includes of those existing recording calls: transcript/source and structured model-contract wrappers, retained under the recording-language contract. The source-project list and recording wrappers retain their original bytes. No recording output or parser contract is changed. |

All 36 original templates and the localized project-list helper are accounted for. Remaining historical templates stay visible in
the downward-only language inventory; no exemption or increased allowance is used.
The migrated non-recording system and input templates have whole English and
Japanese branches. Empty-language and explicit Chinese output keep the old bytes.

## Data, parsers and authorization

Models still return the same JSON fields. Callers parse JSON, not translated
headings. The exception is the existing conservative project-deduplication hedge
check on `reason`, extended for the two new languages and tested through actual
merging. Uncertain or missing `sure` still prevents merging. Topic operation/source
validation, pinned and locked fields, data-only names and the source snapshot
comparison remain in their production paths.

No new injected user message, approval, permission, outbound action, model choice,
background refresh trigger or client interface is added. The owner language is
read through the existing `Engine.OwnerLanguage` callback; nil or unsupported
values preserve Chinese behavior.

## Verification design

Caller-level English, Simplified Chinese and Japanese goldens capture real Engine
model calls, including empty/full optional inputs and pinned facts. Existing empty
language goldens remain unchanged. Production-path tests cover locale changes
during a project model call, stale source rejection, locked owner facts, file
immutability across language changes, supported/fallback languages, and uncertain
multilingual deduplication. Existing recording-language and source-provenance tests
continue to cover their unchanged paths. Golden tests prove emitted text and
wiring, not a real model's semantic quality; no real-model result is inferred from
the synthetic JSON replies.
