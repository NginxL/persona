//! Replays the Agents list projection fixtures, with the fixture names as the case-set oracle.
//!
//! The expected values are only read from `spec/fixtures/`; none are derived from the Rust implementation.

use std::collections::BTreeSet;
use std::fs;
use std::path::{Path, PathBuf};

use persona_core::sort::{Input, Session};
use serde_json::Value;

#[test]
fn traecli_has_one_display_name_for_existing_identifiers() {
    for agent in ["traex", "traecli", "trae-cli"] {
        let session = Session {
            agent: Some(agent.to_owned()),
            ..Session::default()
        };
        assert_eq!(session.agent_label(), "TRAE CLI");
    }
}

fn fixture_dir() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../../../../spec/fixtures/sort")
}

fn documents() -> Vec<(String, Value)> {
    let mut paths: Vec<PathBuf> = fs::read_dir(fixture_dir())
        .expect("spec/fixtures/sort")
        .map(|entry| entry.expect("directory entry").path())
        .filter(|path| path.extension().is_some_and(|extension| extension == "json"))
        .collect();
    paths.sort();
    paths
        .into_iter()
        .map(|path| {
            let name = path.file_name().expect("file name").to_string_lossy().into_owned();
            let document =
                serde_json::from_str(&fs::read_to_string(path).expect("fixture file")).expect("fixture JSON");
            (name, document)
        })
        .collect()
}

fn names(documents: &[(String, Value)]) -> BTreeSet<String> {
    let mut result = BTreeSet::new();
    for (_, document) in documents {
        for case in document["cases"].as_array().expect("cases") {
            let name = case["name"].as_str().expect("case name").to_owned();
            assert!(result.insert(name.clone()), "duplicate sort case name: {name}");
        }
    }
    result
}

#[test]
fn fixture_manifest_has_unique_names_and_projection_shape() {
    let documents = documents();
    let actual = names(&documents);
    let files: BTreeSet<&str> = documents.iter().map(|(name, _)| name.as_str()).collect();
    assert_eq!(
        files,
        BTreeSet::from([
            "filters.json",
            "folding.json",
            "grouping.json",
            "held.json",
            "hub_state.json",
            "ordering.json",
            "session_state.json",
        ]),
        "sort fixture file set changed"
    );
    // A manifest snapshot, not a count of the cases the loop happens to run. Adding, dropping or renaming any case fails
    // here; the shape assertions below also pin each operation rather than allowing a misfiled case to pass unnoticed.
    let expected: BTreeSet<String> = [
        "active_space_uses_rank_not_slots",
        "client_state_precedence",
        "compact_expands_idle_without_reordering",
        "composable_qualifiers_match_with_plain_term",
        "composable_qualifiers_reject_one_wrong_dimension",
        "cross_machine_same_session_id",
        "cross_machine_same_session_id_reversed",
        "dimension_settings_compose_with_plain_term",
        "dimension_settings_reject_wrong_model",
        "empty_list",
        "filter_all",
        "filter_needsyou",
        "filter_unread",
        "filter_working",
        "folded_machine_hides_rows",
        "folded_space_keeps_counts",
        "folded_state_and_expanded_idle",
        "held_quiet_row_keeps_unread_state_section",
        "held_quiet_space_stays_active",
        "held_row_stays_out_of_quiet_idle_fold",
        "hub_blocked",
        "hub_done_seen_after",
        "hub_done_seen_at_tolerance",
        "hub_done_seen_before_tolerance",
        "hub_done_seen_equal",
        "hub_done_unseen",
        "hub_idle",
        "hub_offline",
        "hub_pending_offline",
        "hub_unknown",
        "hub_working_seen",
        "just_finished_rank_and_precedence",
        "kept_cannot_escape_base",
        "machine_search_before_counts",
        "machine_qualifier_matches_registry_title",
        "machine_qualifier_rejects_session_title_match",
        "machine_uses_registry_order",
        "mute_allow_and_explicit_override",
        "pinned_offline_is_visible",
        "pinned_sessions_use_pin_order_once",
        "quick_filter_ignores_rank",
        "quick_spaces_use_updated_time",
        "rank_all_states_and_mutes",
        "search_as_code_day_keyword_uses_ios_window",
        "search_as_code_disabled_keeps_plain_text_matches",
        "search_as_code_error_keyword_filters_plain_text",
        "search_as_code_pinned_keyword_composes_with_terms",
        "search_as_code_preserves_explicit_longer_window",
        "search_as_code_week_keyword_uses_ios_window",
        "search_expands_idle",
        "search_scope_assistant_matches_prose_and_work",
        "search_scope_assistant_rejects_user_and_tool_fields",
        "search_scope_conversation_matches_prose_and_summaries",
        "search_scope_conversation_rejects_tool_noise",
        "search_scope_errors_matches_reason_fields",
        "search_scope_errors_rejects_non_error_evidence",
        "search_scope_errors_without_terms_lists_only_error_rows",
        "search_scope_title_matches_display_names",
        "search_scope_title_rejects_other_fields",
        "search_scope_user_matches_request_summaries",
        "search_scope_user_rejects_assistant_fields",
        "session_summary_fields_do_not_leak_into_all_scope",
        "space_header_counts",
        "space_time_tie",
        "space_time_tie_reversed",
        "spaces_pin_busy_muted_quiet",
        "subtask_summary_evidence_retains_parent_in_user_scope",
        "subtask_summary_evidence_does_not_leak_into_all_scope",
        "subtask_title_evidence_retains_parent_in_all_scope",
        "unread_keeps_seen_rows",
        "unread_reselected_drops_seen_rows",
        "waiting_requires_quiet_state",
        "working_since_then_id",
        "working_since_then_id_reversed",
    ]
    .into_iter()
    .map(str::to_owned)
    .collect();
    assert_eq!(actual, expected, "sort fixture manifest changed");

    for (file, document) in &documents {
        assert_eq!(document["schema_version"], 1, "{file}: schema");
        for case in document["cases"].as_array().expect("cases") {
            let name = case["name"].as_str().expect("name");
            let operation = document["operation"].as_str().expect("operation");
            let expected_operation = match name {
                "client_state_precedence" | "waiting_requires_quiet_state" => "session_state",
                name if name.starts_with("hub_") => "hub_state",
                _ => "agents_list",
            };
            assert_eq!(operation, expected_operation, "{file}/{name}: operation");
            assert!(case["input"].is_object(), "{file}/{name}: input shape");
            assert!(case["expected"].is_object(), "{file}/{name}: expected shape");
            if operation == "agents_list" {
                for key in ["sessions", "machines", "pins", "mutes", "settings"] {
                    assert!(case["input"].get(key).is_some(), "{file}/{name}: missing input.{key}");
                }
                for key in ["counts", "groups", "unread_shown"] {
                    assert!(
                        case["expected"].get(key).is_some(),
                        "{file}/{name}: missing expected.{key}"
                    );
                }
                for group in case["expected"]["groups"].as_array().expect("groups") {
                    for key in ["id", "title", "rows", "visible_rows", "folded"] {
                        assert!(group.get(key).is_some(), "{file}/{name}: missing group.{key}");
                    }
                }
            }
        }
    }
}

fn projection_names(documents: &[(String, Value)]) -> BTreeSet<String> {
    documents
        .iter()
        .filter(|(_, document)| document["operation"] == "agents_list")
        .flat_map(|(_, document)| document["cases"].as_array().expect("cases").iter())
        .map(|case| case["name"].as_str().expect("name").to_owned())
        .collect()
}

#[test]
fn agents_list_projection_replays_every_fixture() {
    let mut replayed = BTreeSet::new();
    for (file, document) in documents() {
        if document["operation"] != "agents_list" {
            continue;
        }
        for case in document["cases"].as_array().expect("cases") {
            let name = case["name"].as_str().expect("name");
            let input: Input = serde_json::from_value(case["input"].clone())
                .unwrap_or_else(|error| panic!("{file}/{name}: input: {error}"));
            let actual = serde_json::to_value(persona_core::sort::project(&input)).expect("projection JSON");
            assert_projection(&actual, &case["expected"], &format!("{file}/{name}"));
            assert!(replayed.insert(name.to_owned()), "replayed {name} twice");
        }
    }
    let documents = documents();
    // The declared document operation, not a case-name prefix or a relayed count, determines the replay subset.
    let expected = projection_names(&documents);
    assert_eq!(replayed, expected, "projection replay set differs from the manifest");
    let count = replayed.len();
    eprintln!("sort projection: {count} cases replayed, with full expected-object equality");
}

#[test]
fn multi_hub_preferences_scoped_pin_mute_and_order_match_ios() {
    let input: Input = serde_json::from_value(serde_json::json!({
        "sessions": [
            {"machine_id": "shared", "session_id": "second", "app_hub_group": "hub-a", "title": "needle", "state": "idle"},
            {"machine_id": "shared", "session_id": "first", "app_hub_group": "hub-a", "title": "needle", "state": "unread"},
            {"machine_id": "shared", "session_id": "first", "app_hub_group": "hub-b", "title": "needle", "state": "idle"},
        ],
        "machines": [
            {"id": "shared", "name": "Fixture", "group": "hub-a"},
            {"id": "shared", "name": "Fixture", "group": "hub-b"},
        ],
        "pins": {}, "mutes": {},
        "hub_preferences": {
            "hub-a": {"pins": {"sessions": ["shared/first", "shared/second"]}, "mutes": {"sessions": ["shared/first"]}},
            "hub-b": {"pins": {"sessions": ["shared/second"]}, "mutes": {"sessions": ["shared/first"], "allowed": ["shared/first"]}},
        },
        "settings": {"grouping": "state", "quick": "all"},
    }))
    .expect("multi-hub input");
    let actual = serde_json::to_value(persona_core::sort::project(&input)).expect("projection");
    assert_eq!(
        actual["groups"][0]["rows"],
        serde_json::json!(["hub-a|shared/first", "hub-a|shared/second"]),
        "the hub's own pin order is retained even though its first row is also muted"
    );
    assert_eq!(actual["groups"][0]["id"], "pinned");
    assert_eq!(actual["groups"][1]["id"], "state/idle");
    assert_eq!(actual["groups"][1]["rows"], serde_json::json!(["hub-b|shared/first"]));
    assert_eq!(actual["unread_shown"], serde_json::json!(["hub-a|shared/first"]));

    let mut legacy = serde_json::json!({
        "sessions": [
            {"machine_id": "shared", "session_id": "first", "title": "needle", "state": "idle"},
            {"machine_id": "shared", "session_id": "second", "title": "needle", "state": "idle"},
            {"machine_id": "shared", "session_id": "third", "title": "needle", "state": "idle"},
        ],
        "machines": [{"id": "shared", "name": "Fixture"}],
        "pins": {"sessions": ["shared/first", "shared/second"]}, "mutes": {},
        "settings": {"grouping": "state", "quick": "all"},
    });
    let input: Input = serde_json::from_value(legacy.take()).expect("legacy input");
    let actual = serde_json::to_value(persona_core::sort::project(&input)).expect("projection");
    assert_eq!(
        actual["groups"][0]["rows"],
        serde_json::json!(["shared/first", "shared/second"]),
        "legacy preferences retain their old semantics"
    );
}

fn hub_selection_input(multi_hub: bool, hub_filter: &str, quick: &str) -> Input {
    serde_json::from_value(serde_json::json!({
        "sessions": [
            {"machine_id": "shared", "session_id": "default", "app_hub_group": "", "title": "needle", "state": "needs_you"},
            {"machine_id": "shared", "session_id": "same", "app_hub_group": "hub-a", "title": "needle", "state": "working"},
            {"machine_id": "shared", "session_id": "same", "app_hub_group": "hub-b", "title": "needle", "state": "unread"},
        ],
        "machines": [
            {"id": "shared", "name": "Fixture"},
            {"id": "shared", "name": "Fixture", "group": "hub-a"},
            {"id": "shared", "name": "Fixture", "group": "hub-b"},
        ],
        "pins": {}, "mutes": {},
        "settings": {"grouping": "state", "quick": quick, "multi_hub": multi_hub, "hub_filter": hub_filter},
    }))
    .expect("hub selection input")
}

fn all_hubs_projection_expectation() -> Value {
    serde_json::json!({
        "counts": {"needsYou": 1, "working": 1, "unread": 1, "backgroundWaiting": 0},
        "groups": [
            {"id": "state/needs", "title": "需要你", "rows": ["shared/default"], "visible_rows": ["shared/default"], "folded": false},
            {"id": "state/unread", "title": "完成未看", "rows": ["hub-b|shared/same"], "visible_rows": ["hub-b|shared/same"], "folded": false},
            {"id": "state/working", "title": "工作中", "rows": ["hub-a|shared/same"], "visible_rows": ["hub-a|shared/same"], "folded": false},
        ],
        "unread_shown": ["hub-b|shared/same"],
    })
}

#[test]
fn selected_hub_scopes_counts_groups_and_unread_match_ios() {
    let input = hub_selection_input(true, "=hub-a", "all");
    let actual = serde_json::to_value(persona_core::sort::project(&input)).expect("selected hub projection");
    // HubFilter.matches("=hub-a", group) admits only hub-a before any of these outputs are derived.
    assert_eq!(
        actual,
        serde_json::json!({
            "counts": {"needsYou": 0, "working": 1, "unread": 0, "backgroundWaiting": 0},
            "groups": [
                {"id": "state/working", "title": "工作中", "rows": ["hub-a|shared/same"], "visible_rows": ["hub-a|shared/same"], "folded": false},
            ],
            "unread_shown": [],
        }),
        "the selected non-default hub retains its full path without admitting the default or hub-b rows"
    );
}

#[test]
fn selected_hub_filter_precedes_quick_filter_match_ios() {
    let input = hub_selection_input(true, "=hub-a", "unread");
    let actual =
        serde_json::to_value(persona_core::sort::project(&input)).expect("selected hub quick-filter projection");
    // iOS counts the selected hub's base first, then applies Unread. hub-b's matching unread row is already absent.
    assert_eq!(
        actual,
        serde_json::json!({
            "counts": {"needsYou": 0, "working": 1, "unread": 0, "backgroundWaiting": 0},
            "groups": [],
            "unread_shown": [],
        }),
        "another hub's unread session must not satisfy the selected hub's quick filter"
    );
}

#[test]
fn empty_hub_filter_selects_all_hubs_with_distinct_full_paths() {
    let input = hub_selection_input(true, "", "all");
    let actual = serde_json::to_value(persona_core::sort::project(&input)).expect("all-hubs projection");
    assert_eq!(
        actual,
        all_hubs_projection_expectation(),
        "the empty HubFilter selects every group and identical short IDs remain distinct by full path"
    );
}

#[test]
fn default_hub_filter_selects_only_the_empty_group() {
    let input = hub_selection_input(true, "=", "all");
    let actual = serde_json::to_value(persona_core::sort::project(&input)).expect("default hub projection");
    // HubFilter.value("") is "=", which selects the default empty group rather than every group.
    assert_eq!(
        actual,
        serde_json::json!({
            "counts": {"needsYou": 1, "working": 0, "unread": 0, "backgroundWaiting": 0},
            "groups": [
                {"id": "state/needs", "title": "需要你", "rows": ["shared/default"], "visible_rows": ["shared/default"], "folded": false},
            ],
            "unread_shown": [],
        })
    );
}

#[test]
fn legacy_multi_hub_false_ignores_hub_filter() {
    let input = hub_selection_input(false, "=hub-a", "all");
    let actual = serde_json::to_value(persona_core::sort::project(&input)).expect("legacy multi-hub projection");
    assert_eq!(
        actual,
        all_hubs_projection_expectation(),
        "without multiHub, iOS does not apply HubFilter even when a selected value is present"
    );
}

#[test]
fn awaiting_reply_is_not_folded_like_plain_idle() {
    let document: Value =
        serde_json::from_str(&fs::read_to_string(fixture_dir().join("folding.json")).expect("folding")).expect("JSON");
    let case = document["cases"]
        .as_array()
        .expect("cases")
        .iter()
        .find(|case| case["name"] == "folded_state_and_expanded_idle")
        .expect("idle fixture");
    let mut input = case["input"].clone();
    input["sessions"]
        .as_array_mut()
        .expect("sessions")
        .iter_mut()
        .find(|session| session["session_id"] == "quiet")
        .expect("idle session")["awaits_reply"] = Value::Bool(true);
    let input: Input = serde_json::from_value(input).expect("mutated fixture input");
    let actual = serde_json::to_value(persona_core::sort::project(&input)).expect("projection JSON");
    // Derived from the specified iOS rank rule (idle + awaits_reply + unmuted), not from this Rust implementation.
    assert_eq!(actual["groups"][1]["id"], "state/awaiting");
    assert_eq!(actual["groups"][1]["rows"], serde_json::json!(["machine-a/quiet"]));
    assert_eq!(
        actual["groups"][1]["visible_rows"],
        serde_json::json!(["machine-a/quiet"])
    );
}

#[test]
fn held_in_list_matches_the_complete_ios_session_key_path() {
    let input: Input = serde_json::from_value(serde_json::json!({
        "sessions": [
            {"machine_id": "m", "session_id": "held", "app_hub_group": "hub-a", "state": "idle"},
            {"machine_id": "m", "session_id": "held", "app_hub_group": "hub-b", "state": "idle"},
        ],
        "machines": [
            {"id": "m", "name": "Example", "group": "hub-a"},
            {"id": "m", "name": "Example", "group": "hub-b"},
        ],
        "pins": {}, "mutes": {},
        "settings": {"grouping": "state", "quick": "all", "held": "hub-a|m/held"},
    }))
    .expect("multi-hub held input");
    let actual = serde_json::to_value(persona_core::sort::project(&input)).expect("projection");
    // iOS `AgentListOrder.held` compares `SessionKey.path`, not a machine/session pair without its hub.
    assert_eq!(actual["groups"][0]["id"], "state/unread");
    assert_eq!(actual["groups"][0]["rows"], serde_json::json!(["hub-a|m/held"]));
    assert_eq!(actual["groups"][1]["id"], "state/idle");
    assert_eq!(actual["groups"][1]["rows"], serde_json::json!(["hub-b|m/held"]));
}

#[test]
fn held_in_list_keeps_position_and_stays_visible() {
    // An Unread agent that is opened keeps its rank-1 position and is not folded into idle, per
    // `AppModel.heldInList` (#2213). It does NOT enter the Pinned section; it stays in its rank section.
    let input: Input = serde_json::from_value(serde_json::json!({
        "sessions": [
            {"machine_id": "m", "session_id": "a", "state": "idle", "updated_at": "2026-01-01T00:00:01Z"},
            {"machine_id": "m", "session_id": "b", "state": "working", "updated_at": "2026-01-01T00:00:02Z"},
            {"machine_id": "m", "session_id": "c", "state": "idle", "updated_at": "2026-01-01T00:00:03Z"},
        ],
        "machines": [{"id": "m", "name": "Machine"}],
        "pins": {},
        "mutes": {},
        "settings": {
            "grouping": "state",
            "quick": "all",
            "held": "m/a",
        }
    }))
    .expect("held input");
    let actual = serde_json::to_value(persona_core::sort::project(&input)).expect("projection");
    // "a" is idle but held → keeps rank 1 (unread), stays visible, NOT in the pinned section.
    let groups = actual["groups"].as_array().expect("groups");
    // No pinned section should exist (nothing is explicitly pinned).
    assert!(
        !groups.iter().any(|g| g["id"] == "pinned"),
        "held agent does not create a pinned section"
    );
    // "a" appears in the unread section (rank 1), not idle.
    let unread = groups.iter().find(|g| g["id"] == "state/unread").expect("unread group");
    assert!(
        unread["rows"]
            .as_array()
            .expect("unread rows")
            .iter()
            .any(|v| v == "m/a"),
        "held agent keeps unread rank"
    );
    // "c" is idle without held → goes into state/idle.
    let idle = groups.iter().find(|g| g["id"] == "state/idle").expect("idle group");
    assert!(idle["rows"].as_array().expect("idle rows").iter().any(|v| v == "m/c"));
}

#[test]
fn since_query_filters_by_updated_at() {
    let base_sessions = serde_json::json!([
        {"machine_id": "m", "session_id": "recent", "state": "idle", "updated_at": "2026-01-01T12:00:00Z"},
        {"machine_id": "m", "session_id": "old", "state": "idle", "updated_at": "2025-12-31T00:00:00Z"},
    ]);
    let base = serde_json::json!({
        "sessions": base_sessions,
        "machines": [{"id": "m", "name": "Machine"}],
        "pins": {},
        "mutes": {},
        "settings": {"grouping": "state", "quick": "all", "expanded_idle": ["state/idle"]},
    });

    // Without a `since:` token both sessions appear.
    let input: Input = serde_json::from_value(base.clone()).expect("no since");
    let actual = serde_json::to_value(persona_core::sort::project(&input)).expect("projection");
    let rows = &actual["groups"][0]["rows"];
    assert_eq!(rows.as_array().expect("rows").len(), 2);

    // With `since:24h` and `now` at 2026-01-01T18:00:00Z, only the recent one (12:00) matches.
    let mut with_since = base.clone();
    with_since["settings"]["query"] = serde_json::json!("since:24h");
    with_since["settings"]["now"] = serde_json::json!("2026-01-01T18:00:00Z");
    let input: Input = serde_json::from_value(with_since).expect("since input");
    let actual = serde_json::to_value(persona_core::sort::project(&input)).expect("projection");
    let rows = &actual["groups"][0]["rows"];
    assert_eq!(rows, &serde_json::json!(["m/recent"]));

    // Without `now` the since query matches nothing (no clock).
    let mut no_now = base.clone();
    no_now["settings"]["query"] = serde_json::json!("since:24h");
    let input: Input = serde_json::from_value(no_now).expect("no now");
    let actual = serde_json::to_value(persona_core::sort::project(&input)).expect("projection");
    let total: usize = actual["groups"]
        .as_array()
        .expect("groups")
        .iter()
        .map(|g| g["rows"].as_array().map_or(0, |a| a.len()))
        .sum();
    assert_eq!(total, 0, "since: without now must match nothing");
}

#[test]
fn background_waiting_quick_filter_shows_only_rank_4() {
    // BackgroundWaiting quick filter should only show rank-4 (background waiting) section.
    let input: Input = serde_json::from_value(serde_json::json!({
        "sessions": [
            {"machine_id": "m", "session_id": "bg", "state": "working", "background_waiting": true, "updated_at": "2026-01-01T00:00:01Z"},
            {"machine_id": "m", "session_id": "w", "state": "working", "updated_at": "2026-01-01T00:00:02Z"},
            {"machine_id": "m", "session_id": "n", "state": "needsYou", "updated_at": "2026-01-01T00:00:03Z"},
        ],
        "machines": [{"id": "m", "name": "Machine"}],
        "pins": {},
        "mutes": {},
        "settings": {"grouping": "state", "quick": "backgroundWaiting"},
    }))
    .expect("bg input");
    let actual = serde_json::to_value(persona_core::sort::project(&input)).expect("projection");
    let group_ids: Vec<&str> = actual["groups"]
        .as_array()
        .expect("groups")
        .iter()
        .map(|g| g["id"].as_str().expect("id"))
        .collect();
    assert_eq!(
        group_ids,
        vec!["state/background"],
        "only the background-waiting section appears"
    );
    assert_eq!(actual["groups"][0]["rows"], serde_json::json!(["m/bg"]));
}

#[test]
fn compact_tab_rows_preserve_pane_order_and_fold_ids() {
    // Independently derived from iOS's TabPanes: slot order, then pane_index / base-36 pane id.
    let input: Input = serde_json::from_value(serde_json::json!({
        "sessions": [
            {"machine_id": "box", "session_id": "lead", "state": "working", "space_id": "w1", "space_kind": "herdr", "space_name": "app", "slot_id": "s1", "slot_index": 1, "pane_id": "w1:pW", "pane_index": 0},
            {"machine_id": "box", "session_id": "branch", "state": "idle", "space_id": "w1", "space_kind": "herdr", "space_name": "app", "slot_id": "s1", "slot_index": 1, "pane_id": "w1:p2E", "pane_index": 1},
            {"machine_id": "box", "session_id": "other", "state": "working", "space_id": "w1", "space_kind": "herdr", "space_name": "app", "slot_id": "s2", "slot_index": 2, "pane_id": "w1:p3A", "pane_index": 0},
        ],
        "machines": [{"id": "box", "name": "Fixture"}], "pins": {}, "mutes": {},
        "settings": {"grouping": "space", "quick": "all", "presentation_rows": true, "folded": ["tab/box/app/1"]},
    })).expect("tab input");
    let actual = serde_json::to_value(persona_core::sort::project(&input)).expect("projection");
    assert_eq!(
        actual["rows"],
        serde_json::json!([
            {"id": "tab|box/herdr:w1/s1", "fold_id": "tab/box/app/1", "rows": ["box/lead", "box/branch"], "visible_rows": [], "folded": true},
            {"id": "box/other", "fold_id": "box/other", "rows": ["box/other"], "visible_rows": ["box/other"], "folded": false},
        ])
    );
    assert_eq!(
        actual["groups"][0]["rows"],
        serde_json::json!(["box/lead", "box/branch", "box/other"])
    );
}

#[test]
fn aggregate_waiting_never_underflows_spoken_counts() {
    let input: Input = serde_json::from_value(serde_json::json!({
        "sessions": [{"machine_id": "m", "session_id": "w", "state": "working", "background_waiting": true}],
        "machines": [{"id": "m", "name": "Fixture"}], "pins": {}, "mutes": {},
        "settings": {"grouping": "space", "quick": "all", "aggregate": {"m/-": {"state": {"working": 1, "idle": 0, "unknown": 0}}}},
    })).expect("aggregate input");
    let actual = serde_json::to_value(persona_core::sort::project(&input)).expect("projection");
    assert_eq!(
        actual["groups"][0]["spoken"],
        serde_json::json!("1 个工作中，1 个后台等待中")
    );
}

#[test]
fn compact_search_and_quick_filter_show_all_retained_tab_rows() {
    let input: Input = serde_json::from_value(serde_json::json!({
        "sessions": [
            {"machine_id": "box", "session_id": "lead", "state": "working", "title": "needle", "space_id": "w1", "space_kind": "herdr", "space_name": "app", "slot_id": "s1", "slot_index": 1, "pane_id": "w1:pW", "pane_index": 0},
            {"machine_id": "box", "session_id": "branch", "state": "working", "title": "needle", "space_id": "w1", "space_kind": "herdr", "space_name": "app", "slot_id": "s1", "slot_index": 1, "pane_id": "w1:p2E", "pane_index": 1},
        ],
        "machines": [{"id": "box", "name": "Fixture"}], "pins": {}, "mutes": {},
        "settings": {"grouping": "space", "quick": "working", "query": "needle", "presentation_rows": true},
    })).expect("search tab input");
    let actual = serde_json::to_value(persona_core::sort::project(&input)).expect("projection");
    assert_eq!(
        actual["groups"][0]["visible_rows"],
        serde_json::json!(["box/lead", "box/branch"])
    );
    assert_eq!(
        actual["rows"][0]["visible_rows"],
        serde_json::json!(["box/lead", "box/branch"])
    );
}

#[test]
fn compact_idle_tabs_expand_for_search_and_quick_filter_only() {
    let sessions = serde_json::json!([
        {"machine_id": "box", "session_id": "lead", "state": "idle", "title": "needle", "space_id": "w1", "space_kind": "herdr", "space_name": "app", "slot_id": "s1", "slot_index": 1, "pane_id": "w1:pW", "pane_index": 0},
        {"machine_id": "box", "session_id": "branch", "state": "idle", "title": "needle", "space_id": "w1", "space_kind": "herdr", "space_name": "app", "slot_id": "s1", "slot_index": 1, "pane_id": "w1:p2E", "pane_index": 1},
    ]);
    let input = |query: &str, quick: &str| {
        serde_json::from_value::<Input>(serde_json::json!({
            "sessions": sessions,
            "machines": [{"id": "box", "name": "Fixture"}], "pins": {}, "mutes": {},
            "settings": {"grouping": "space", "quick": quick, "query": query, "presentation_rows": true},
        }))
        .expect("idle tab input")
    };
    let folded = serde_json::to_value(persona_core::sort::project(&input("", "all"))).expect("idle folded");
    assert!(
        folded["rows"].as_array().expect("rows").is_empty(),
        "idle tabs stay behind the idle fold without a query"
    );
    let searched = serde_json::to_value(persona_core::sort::project(&input("needle", "all"))).expect("search expands");
    assert_eq!(
        searched["rows"].as_array().map(|rows| rows.len()),
        Some(2),
        "quiet spaces show flat panes, not tab headers"
    );
    assert_eq!(searched["rows"][0]["visible_rows"], serde_json::json!(["box/lead"]));
    assert_eq!(searched["rows"][1]["visible_rows"], serde_json::json!(["box/branch"]));
    for (query, quick) in [("needle", "working"), ("", "working")] {
        let filtered = serde_json::to_value(persona_core::sort::project(&input(query, quick))).expect("quick filter");
        assert!(
            filtered["rows"].as_array().expect("rows").is_empty(),
            "idle members are excluded by the working quick filter"
        );
    }
}

#[test]
fn compact_idle_space_search_and_quick_filter_expand_independently() {
    let input = |query: &str, quick: &str| {
        serde_json::from_value::<Input>(serde_json::json!({
            "sessions": [
                {"machine_id": "m", "session_id": "idle", "state": "idle", "title": "needle", "space_id": "w1", "space_name": "app"},
                {"machine_id": "m", "session_id": "unread", "state": "idle", "title": "needle", "space_id": "w1", "space_name": "app"},
            ],
            "machines": [{"id": "m", "name": "Fixture"}], "pins": {}, "mutes": {},
            "settings": {"grouping": "space", "quick": quick, "query": query, "presentation_rows": true, "kept": if quick == "all" { serde_json::json!([]) } else { serde_json::json!(["m/idle"]) }},
        })).expect("quiet input")
    };
    assert!(
        serde_json::to_value(persona_core::sort::project(&input("", "all"))).expect("folded")["rows"]
            .as_array()
            .expect("rows")
            .is_empty()
    );
    for (query, quick, count, expected) in [("needle", "all", 2, "m/idle"), ("needle", "working", 1, "m/idle")] {
        let projected = serde_json::to_value(persona_core::sort::project(&input(query, quick))).expect("expanded");
        assert_eq!(
            projected["rows"].as_array().map(|rows| rows.len()),
            Some(count),
            "query={query} quick={quick}"
        );
        assert_eq!(projected["rows"][0]["visible_rows"], serde_json::json!([expected]));
    }
}

#[test]
fn compact_active_space_expands_idle_pane_for_query_and_quick_filter() {
    let input = |query: &str, quick: &str| {
        serde_json::from_value::<Input>(serde_json::json!({
            "sessions": [
                {"machine_id": "box", "session_id": "lead", "state": "working", "title": "needle", "space_id": "w1", "space_kind": "herdr", "space_name": "app", "slot_id": "s1", "slot_index": 1, "pane_id": "w1:pW", "pane_index": 0},
                {"machine_id": "box", "session_id": "branch", "state": "idle", "title": "needle", "space_id": "w1", "space_kind": "herdr", "space_name": "app", "slot_id": "s1", "slot_index": 1, "pane_id": "w1:p2E", "pane_index": 1},
            ],
            "machines": [{"id": "box", "name": "Fixture"}], "pins": {}, "mutes": {},
            "settings": {"grouping": "space", "quick": quick, "query": query, "presentation_rows": true, "kept": if quick == "all" { serde_json::json!([]) } else { serde_json::json!(["box/branch"]) }},
        })).expect("active tab input")
    };
    let folded = serde_json::to_value(persona_core::sort::project(&input("", "all"))).expect("mixed tab");
    assert_eq!(
        folded["rows"][0]["visible_rows"],
        serde_json::json!(["box/lead", "box/branch"]),
        "a busy tab keeps all its panes visible"
    );
    for (query, quick) in [("needle", "all"), ("", "working")] {
        let expanded = serde_json::to_value(persona_core::sort::project(&input(query, quick))).expect("expanded pane");
        assert_eq!(
            expanded["rows"][0]["visible_rows"],
            serde_json::json!(["box/lead", "box/branch"]),
            "query={query} quick={quick}"
        );
    }
}

#[test]
fn compact_active_space_expands_an_independent_idle_row_for_query_and_quick_filter() {
    let input = |query: &str, quick: &str| {
        serde_json::from_value::<Input>(serde_json::json!({
            "sessions": [
                {"machine_id": "box", "session_id": "busy", "state": "working", "title": "needle", "space_id": "w1", "space_name": "app"},
                {"machine_id": "box", "session_id": "idle", "state": "idle", "title": "needle", "space_id": "w1", "space_name": "app"},
            ],
            "machines": [{"id": "box", "name": "Fixture"}], "pins": {}, "mutes": {},
            "settings": {"grouping": "space", "quick": quick, "query": query, "presentation_rows": true, "kept": if quick == "all" { serde_json::json!([]) } else { serde_json::json!(["box/idle"]) }},
        })).expect("independent idle row")
    };
    let folded = serde_json::to_value(persona_core::sort::project(&input("", "all"))).expect("idle fold");
    assert!(
        !folded["rows"]
            .as_array()
            .expect("rows")
            .iter()
            .any(|row| row["id"] == "box/idle"),
        "the independent idle row stays folded"
    );
    for (query, quick) in [("needle", "all"), ("", "working")] {
        let expanded =
            serde_json::to_value(persona_core::sort::project(&input(query, quick))).expect("expanded idle row");
        assert!(
            expanded["rows"]
                .as_array()
                .expect("rows")
                .iter()
                .any(|row| row["id"] == "box/idle"),
            "query={query} quick={quick}"
        );
        assert!(
            expanded["rows"]
                .as_array()
                .expect("rows")
                .iter()
                .any(|row| row["id"] == "box/idle" && row["visible_rows"] == serde_json::json!(["box/idle"]))
        );
    }
}

#[test]
fn nested_rows_and_folded_children_match_ios_counts() {
    let input: Input = serde_json::from_value(serde_json::json!({
        "sessions": [
            {"machine_id": "m", "session_id": "parent", "state": "working", "background_waiting": true, "children": {"working": 1, "needs_you": 0}},
            {"machine_id": "m", "session_id": "orphan", "state": "working", "nested_subtask": true},
            {"machine_id": "m", "session_id": "waiting", "state": "working", "background_waiting": true},
            {"machine_id": "m", "session_id": "ordinary", "state": "working"},
        ],
        "machines": [{"id": "m", "name": "Fixture"}], "pins": {}, "mutes": {},
        "settings": {"grouping": "space", "quick": "all", "aggregate": {"m/-": {"state": {"needs_you": 0, "working": 7, "unread": 0, "idle": 1, "unknown": 0}}}},
    })).expect("folded parent input");
    let actual = serde_json::to_value(persona_core::sort::project(&input)).expect("projection");
    assert_eq!(
        actual["counts"],
        serde_json::json!({"needsYou": 0, "working": 2, "unread": 0, "backgroundWaiting": 1})
    );
    assert_eq!(
        actual["groups"][0]["state_counts"],
        serde_json::json!({"needs_you": 0, "working": 7, "unread": 0, "idle": 1, "unknown": 0})
    );
    assert_eq!(
        actual["groups"][0]["spoken"],
        serde_json::json!("7 个工作中，1 个后台等待中")
    );
}

#[test]
fn search_retains_parent_when_nested_subtask_matches() {
    // iOS retains a prepared root when any task below it admits the query. Pinning the offline parent makes its
    // otherwise invisible inventory membership observable in the one section that can render an offline row.
    let input: Input = serde_json::from_value(serde_json::json!({
        "sessions": [
            {"machine_id": "machine-a", "session_id": "e", "title": "ordinary parent", "state": "offline", "status": "offline", "nested_match": true},
        ],
        "machines": [{"id": "machine-a", "name": "Machine A"}],
        "pins": {"sessions": ["machine-a/e"]}, "mutes": {},
        "settings": {"grouping": "state", "quick": "all", "query": "needle", "inventory": true},
    }))
    .expect("nested search input");
    let actual = serde_json::to_value(persona_core::sort::project(&input)).expect("projection");
    assert_eq!(
        actual["groups"],
        serde_json::json!([{
            "id": "pinned", "title": "置顶", "rows": ["machine-a/e"],
            "visible_rows": ["machine-a/e"], "folded": false,
        }]),
        "the matching nested row retains its parent, not a flattened subtask row"
    );
    assert_eq!(actual["filtered"], serde_json::json!(["machine-a/e"]));
    assert_eq!(actual["unpinned"], serde_json::json!([]));
}

#[test]
fn search_keeps_promoted_subtask_root_when_parent_cannot_render() {
    // A live subtask is an independent root when no rendered ancestor is live or pinned. Parent identity alone must
    // not fold that root back into an offline parent (regression control for the prepared-input contract).
    let input: Input = serde_json::from_value(serde_json::json!({
        "sessions": [
            {"machine_id": "machine-a", "session_id": "e", "title": "ordinary parent", "state": "offline", "status": "offline"},
            {"machine_id": "machine-a", "session_id": "child", "title": "needle", "state": "working", "status": "idle", "source_kind": "runtime", "nested_subtask": true, "parent_session_id": "e"},
        ],
        "machines": [{"id": "machine-a", "name": "Machine A"}],
        "pins": {}, "mutes": {},
        "settings": {"grouping": "state", "quick": "all", "query": "needle", "inventory": true},
    }))
    .expect("promoted subtask input");
    let actual = serde_json::to_value(persona_core::sort::project(&input)).expect("projection");
    assert_eq!(
        actual["groups"],
        serde_json::json!([{
            "id": "state/working", "title": "工作中", "rows": ["machine-a/child"],
            "visible_rows": ["machine-a/child"], "folded": false,
        }])
    );
    assert_eq!(actual["filtered"], serde_json::json!(["machine-a/child"]));
    assert_eq!(actual["unpinned"], serde_json::json!(["machine-a/child"]));
}

#[test]
fn missing_machine_stays_in_filtered_inventory_without_a_group() {
    // Inventory membership is independent of whether the machine registry can supply a section. The Needs You quick
    // filter also removes the registered Working row, leaving only the unregistered, unpinned session.
    let input: Input = serde_json::from_value(serde_json::json!({
        "sessions": [
            {"machine_id": "machine-a", "session_id": "working", "state": "working"},
            {"machine_id": "machine-missing", "session_id": "orphan", "state": "needs_you"},
        ],
        "machines": [{"id": "machine-a", "name": "Machine A"}],
        "pins": {}, "mutes": {},
        "settings": {"grouping": "machine", "quick": "needsYou", "inventory": true},
    }))
    .expect("missing machine input");
    let actual = serde_json::to_value(persona_core::sort::project(&input)).expect("projection");
    assert_eq!(actual["filtered"], serde_json::json!(["machine-missing/orphan"]));
    assert_eq!(actual["unpinned"], serde_json::json!(["machine-missing/orphan"]));
    assert_eq!(
        actual["groups"],
        serde_json::json!([]),
        "an absent registry entry emits no machine section"
    );
}

#[test]
fn fixture_assertion_catches_a_wrong_sort_comparison() {
    let document: Value =
        serde_json::from_str(&fs::read_to_string(fixture_dir().join("ordering.json")).expect("ordering"))
            .expect("JSON");
    let case = document["cases"]
        .as_array()
        .expect("cases")
        .iter()
        .find(|case| case["name"] == "working_since_then_id")
        .expect("date-order fixture");
    let input: Input = serde_json::from_value(case["input"].clone()).expect("input");
    let correct = serde_json::to_value(persona_core::sort::project(&input)).expect("projection JSON");
    assert_eq!(
        correct, case["expected"],
        "the reference comparison must agree with the fixture"
    );

    // Deliberately reverse the production comparison, then run it against the fixture-derived row set and expectation.
    // The assertion helper is shared with the replay test: the mutation must be a mismatch, not merely a code review.
    let input: Input = serde_json::from_value(case["input"].clone()).expect("mutation fixture input");
    let wrong = serde_json::to_value(persona_core::sort::project_with_order(&input)).expect("projection JSON");
    let failure = std::panic::catch_unwind(|| {
        assert_projection(
            &wrong,
            &case["expected"],
            "ordering.json/working_since_then_id (oldest first)",
        );
    });
    assert!(
        failure.is_err(),
        "reversing the date comparison must fail the shared fixture assertion"
    );
}

#[test]
fn just_finished_stays_active_but_its_muted_row_folds_with_seen() {
    let document: Value =
        serde_json::from_str(&fs::read_to_string(fixture_dir().join("ordering.json")).expect("ordering"))
            .expect("JSON");
    let case = document["cases"]
        .as_array()
        .expect("cases")
        .iter()
        .find(|case| case["name"] == "just_finished_rank_and_precedence")
        .expect("just finished fixture");
    let mut input = case["input"].clone();
    input["sessions"]
        .as_array_mut()
        .expect("sessions")
        .retain(|session| matches!(session["session_id"].as_str(), Some("finished" | "muted")));
    input["settings"]["grouping"] = Value::String("space".to_owned());
    input["settings"]["expanded_quiet"] = serde_json::json!(["machine-a/space-muted"]);
    let input: Input = serde_json::from_value(input).expect("space input");
    let actual = serde_json::to_value(persona_core::sort::project(&input)).expect("space projection");

    assert_eq!(actual["groups"][0]["id"], serde_json::json!("machine-a/space-live"));
    assert_eq!(actual["groups"][0]["parent"], Value::Null);
    assert_eq!(
        actual["groups"][0]["visible_rows"],
        serde_json::json!(["machine-a/finished"])
    );
    assert_eq!(actual["groups"][1]["id"], serde_json::json!("machine-a/space-muted"));
    assert_eq!(actual["groups"][1]["parent"], serde_json::json!("quiet"));
    assert_eq!(
        actual["groups"][1]["visible_rows"],
        serde_json::json!(["machine-a/muted"])
    );
    assert_eq!(actual["quiet_title"], serde_json::json!("空闲的空间"));
}

fn assert_projection(actual: &Value, expected: &Value, label: &str) {
    assert_eq!(actual, expected, "{label}: complete projection");
}
