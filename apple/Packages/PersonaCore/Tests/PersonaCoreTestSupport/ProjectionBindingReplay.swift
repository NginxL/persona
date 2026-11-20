import Foundation
import PersonaCore

/// macOS/iOS test callers replay the same unchanged corpus as Rust, C and JNI. These routines never ship in the app.
public enum ProjectionBindingReplay {
    public static func sort() throws -> Int {
        let fixtures = try ProjectionFixture.cases(in: "sort", operation: "agents_list")
        try ProjectionFixture.exactManifest(fixtures, sortCases, "agents_list")
        for fixture in fixtures {
            let actual = try ProjectionFixture.project(fixture.input, fixture.name, using: PersonaCoreProjections.sort)
            try ProjectionFixture.assertEqual(actual, fixture.expected, "sort/\(fixture.name)")
        }
        try ProjectionFixture.rejectMalformed(PersonaCoreProjections.sort, label: "sort")
        let first = try ProjectionFixture.require(fixtures.first, "sort fixture")
        let actual = try ProjectionFixture.project(first.input, first.name, using: PersonaCoreProjections.sort)
        try ProjectionFixture.assertWrongExpectationIsRejected(actual, wrong: .object([:]), label: "sort/\(first.name)")
        return fixtures.count
    }

    public static func answers() throws -> (questions: Int, recommendations: Int) {
        var counts: [String: Int] = [:]
        for (file, names) in answerManifests {
            let document = try ContinuityJSON.load(file: repositoryFixtures.appending(path: "answer/\(file)"))
            let cases = try ProjectionFixture.require(document["cases"]?.array, file)
            try ProjectionFixture.exactManifest(
                cases.map { ProjectionFixture(name: $0.text("name") ?? "", input: .null, expected: .null) },
                names, file
            )
            for item in cases {
                let name = try ProjectionFixture.require(item.text("name"), "answer name")
                let input = try ProjectionFixture.require(item["input"], "answer input")
                let expected = try ProjectionFixture.require(item["expected"], "answer expected")
                let actual = try answer(input, expected: expected, recommended: file == "recommended.json", name: name)
                try ProjectionFixture.assertEqual(actual, expected, "answer/\(name)")
                if var wrong = actual.object, let allowed = wrong["allowed"]?.bool {
                    wrong["allowed"] = .bool(!allowed)
                    try ProjectionFixture.assertWrongExpectationIsRejected(
                        actual, wrong: .object(wrong), label: "answer/\(name)"
                    )
                }
                counts[file, default: 0] += 1
            }
        }
        try ProjectionFixture.rejectMalformed(PersonaCoreProjections.answer, label: "answer")
        try ProjectionFixture.rejectMalformed(PersonaCoreProjections.displayAnswer, label: "display_answer")

        let question = try ProjectionFixture.require(
            try ProjectionFixture.cases(in: "answer").first { $0.name == "single_tap_sends_original_label" },
            "answer boundary case"
        )
        for (field, value) in [("page", 99), ("option_index", 99)] {
            var operation = try ProjectionFixture.require(question.input["operation"]?.object, "operation")
            operation[field] = .number(String(value))
            var input = try ProjectionFixture.require(question.input.object, "input")
            input["operation"] = .object(operation)
            do {
                let result = try PersonaCoreProjections.answer(inputJSON: ContinuityJSON.object(input).encoded())
                throw ProjectionFixtureError.invalidFixture("answer accepted out-of-range \(field): \(result)")
            } catch PersonaCoreProjectionError.emptyResult {}
        }
        try unicodeWhitespaceBoundary()
        return (counts["questions.json", default: 0], counts["recommended.json", default: 0])
    }

    public static func streamChunks() throws -> Int {
        let fixtures = try ProjectionFixture.cases(in: "stream-chunks")
        try ProjectionFixture.exactManifest(fixtures, streamCases, "stream_chunks")
        for fixture in fixtures {
            let actual = try ProjectionFixture.project(
                fixture.input, fixture.name, using: PersonaCoreProjections.streamChunks
            )
            try ProjectionFixture.assertEqual(actual, fixture.expected, "stream_chunks/\(fixture.name)")
            try ProjectionFixture.assertWrongExpectationIsRejected(
                actual, wrong: .object(["done": .array([]), "tail": .string("wrong")]),
                label: "stream_chunks/\(fixture.name)"
            )
        }
        try ProjectionFixture.rejectMalformed(PersonaCoreProjections.streamChunks, label: "stream_chunks")
        return fixtures.count
    }

    private static func answer(
        _ input: ContinuityJSON, expected: ContinuityJSON, recommended: Bool, name: String
    ) throws -> ContinuityJSON {
        let transition = try ProjectionFixture.project(input, "answer", using: PersonaCoreProjections.answer)
        let operation = try ProjectionFixture.require(input["operation"], "operation")
        let page = try ProjectionFixture.require(operation.number("page"), "page")
        let questions = try ProjectionFixture.require(input["interaction"]?["questions"]?.array, "questions")
        let question = try ProjectionFixture.require(questions[safe: page], "question index")
        let originalPicks = try ProjectionFixture.require(operation["picks"] ?? .object([:]), "input picks")
        let originalOther = try ProjectionFixture.require(operation["other"] ?? .object([:]), "input other")
        var expectedPicks = originalPicks
        var expectedOther = originalOther
        // The checked-in expected object intentionally selects only some Transition fields. Assert the remainder from
        // the fixture's own input and documented cases, never from the Rust output, so state outside that object is
        // still covered: an accepted single-select replaces one question's picks and clears its free text; an accepted
        // multi-select toggles one option. Sending-disabled picks and all other actions preserve both. Other
        // questions' state is never discarded.
        if operation.text("action") == "pick_option", operation.flag("sending") == false {
            let key = try ProjectionFixture.require(question.text("question"), "question")
            let optionIndex = try ProjectionFixture.require(operation.number("option_index"), "option_index")
            let options = try ProjectionFixture.require(question["options"]?.array, "options")
            let option = try ProjectionFixture.require(options[safe: optionIndex], "option index")
            let label = try ProjectionFixture.require(option.text("label"), "label")
            var picks = try ProjectionFixture.require(originalPicks.object, "picks object")
            if question.boolean("multi_select") == true {
                var selected = Set(originalPicks[key]?.array?.compactMap(\.string) ?? [])
                if selected.remove(label) == nil { selected.insert(label) }
                picks[key] = .array(selected.sorted().map(ContinuityJSON.string))
            } else {
                picks[key] = .array([.string(label)])
                var other = try ProjectionFixture.require(originalOther.object, "other object")
                other[key] = .string("")
                expectedOther = .object(other)
            }
            expectedPicks = .object(picks)
        }
        let expectedPage: ContinuityJSON = expected["next_page"] ?? .number(String(page))
        try ProjectionFixture.assertEqual(
            try ProjectionFixture.require(transition["page"], "page"), expectedPage, "answer/\(name)/page"
        )
        try ProjectionFixture.assertEqual(
            try ProjectionFixture.require(transition["other"], "other"), expectedOther, "answer/\(name)/other"
        )
        try ProjectionFixture.assertEqual(
            try ProjectionFixture.require(transition["picks"], "picks"), expectedPicks, "answer/\(name)/picks"
        )
        var actual: [String: ContinuityJSON] = [
            "allowed": try ProjectionFixture.require(transition["allowed"], "allowed"),
            "resolution": try ProjectionFixture.require(transition["resolution"], "resolution"),
        ]
        if transition["page"] != operation["page"] {
            actual["next_page"] = try ProjectionFixture.require(transition["page"], "page")
        }
        if operation.text("action") == "pick_option", transition.boolean("allowed") == true,
           question.boolean("multi_select") == true {
            actual["picks"] = try ProjectionFixture.require(transition["picks"], "picks")
        }
        if recommended {
            let optionIndex = try ProjectionFixture.require(operation.number("option_index"), "option_index")
            let options = try ProjectionFixture.require(question["options"]?.array, "options")
            let option = try ProjectionFixture.require(options[safe: optionIndex], "option index")
            let label = try ProjectionFixture.require(option.text("label"), "label")
            actual["display"] = try ProjectionFixture.project(
                .string(label), "display_answer", using: PersonaCoreProjections.displayAnswer
            )
        }
        return .object(actual)
    }

    private static func unicodeWhitespaceBoundary() throws {
        let input: ContinuityJSON = .object([
            "interaction": .object([
                "kind": .string("question"),
                "questions": .array([.object([
                    "question": .string("One"),
                    "options": .array([.object(["label": .string("Yes")])]),
                ])]),
            ]),
            "operation": .object([
                "action": .string("submit"), "page": .number("0"),
                "picks": .object([:]), "other": .object(["One": .string(" \u{200b} ")]),
            ]),
        ])
        let transition = try ProjectionFixture.project(input, "answer U+200B", using: PersonaCoreProjections.answer)
        try ProjectionFixture.assertEqual(transition, .object([
            "allowed": .bool(false), "page": .number("0"), "picks": .object([:]),
            "other": .object(["One": .string(" \u{200b} ")]), "resolution": .null,
        ]), "answer U+200B")
        let display = try ProjectionFixture.project(
            .string("\u{200b} A\n (Recommended)"), "display U+200B", using: PersonaCoreProjections.displayAnswer
        )
        try ProjectionFixture.assertEqual(
            display, .object(["text": .string("A\n"), "recommended": .bool(true)]), "display U+200B"
        )
        try ProjectionFixture.assertWrongExpectationIsRejected(
            display, wrong: .object(["text": .string("\u{200b}A"), "recommended": .bool(false)]), label: "display U+200B"
        )
    }

    private static let sortCases: Set<String> = [
        "composable_qualifiers_match_with_plain_term", "composable_qualifiers_reject_one_wrong_dimension",
        "dimension_settings_compose_with_plain_term", "dimension_settings_reject_wrong_model",
        "machine_qualifier_matches_registry_title", "machine_qualifier_rejects_session_title_match",
        "search_as_code_day_keyword_uses_ios_window", "search_as_code_disabled_keeps_plain_text_matches",
        "search_as_code_error_keyword_filters_plain_text", "search_as_code_pinned_keyword_composes_with_terms",
        "search_as_code_preserves_explicit_longer_window", "search_as_code_week_keyword_uses_ios_window",
        "search_scope_assistant_matches_prose_and_work", "search_scope_assistant_rejects_user_and_tool_fields",
        "search_scope_conversation_matches_prose_and_summaries", "search_scope_conversation_rejects_tool_noise",
        "search_scope_errors_matches_reason_fields", "search_scope_errors_rejects_non_error_evidence",
        "search_scope_errors_without_terms_lists_only_error_rows", "search_scope_title_matches_display_names",
        "search_scope_title_rejects_other_fields", "search_scope_user_matches_request_summaries",
        "search_scope_user_rejects_assistant_fields", "session_summary_fields_do_not_leak_into_all_scope",
        "subtask_summary_evidence_retains_parent_in_user_scope", "subtask_summary_evidence_does_not_leak_into_all_scope",
        "subtask_title_evidence_retains_parent_in_all_scope",
        "active_space_uses_rank_not_slots", "compact_expands_idle_without_reordering",
        "cross_machine_same_session_id", "cross_machine_same_session_id_reversed",
        "empty_list", "filter_all", "filter_needsyou", "filter_unread", "filter_working",
        "folded_machine_hides_rows", "folded_space_keeps_counts", "folded_state_and_expanded_idle",
        "just_finished_rank_and_precedence",
        "kept_cannot_escape_base", "held_quiet_row_keeps_unread_state_section", "held_quiet_space_stays_active",
        "held_row_stays_out_of_quiet_idle_fold", "machine_search_before_counts", "machine_uses_registry_order",
        "mute_allow_and_explicit_override", "pinned_offline_is_visible", "pinned_sessions_use_pin_order_once",
        "quick_filter_ignores_rank", "quick_spaces_use_updated_time", "rank_all_states_and_mutes",
        "search_expands_idle", "space_header_counts", "space_time_tie", "space_time_tie_reversed",
        "spaces_pin_busy_muted_quiet", "unread_keeps_seen_rows", "unread_reselected_drops_seen_rows",
        "working_since_then_id", "working_since_then_id_reversed",
    ]

    private static let answerManifests = [
        "questions.json": Set([
            "all_answered_can_submit_from_earlier_page", "blank_other_cannot_submit", "blank_other_uses_picks",
            "empty_answer_cannot_submit", "last_page_missing_answer_returns_to_gap", "multiple_other_overrides_picks",
            "multiple_pick_toggles_off", "multiple_pick_waits_for_submit", "multiple_picks_follow_option_order",
            "multiple_questions_send_original_keys", "next_page_does_not_send", "other_answer_trimmed",
            "sending_disables_option", "single_pick_clears_other", "single_tap_sends_original_label",
        ]),
        "recommended.json": Set([
            "recommended_case_sensitive", "recommended_chinese_ascii", "recommended_chinese_fullwidth",
            "recommended_suffix_only", "recommended_spaced", "recommended_trailing_space",
            "recommended_trims_prefix_space", "recommended_unspaced", "recommended_corefoundation_boundary",
        ]),
    ]

    private static let streamCases: Set<String> = [
        "cuts_after_blank_line_once_next_block_starts", "blank_line_at_end_is_not_cut_yet",
        "never_cuts_inside_code_fence", "keeps_indented_continuation_with_block", "prewarm_uses_the_tail",
        "longer_fence_ignores_shorter_inner_closer", "language_suffixed_line_does_not_close_fence",
        "reassembles_full_text", "empty_input", "consecutive_blank_lines_keep_their_newline",
        "space_padded_separator_is_omitted", "tab_indented_continuation_stays_with_block",
        "non_ascii_space_is_not_raw_indentation", "carriage_return_is_not_a_line_separator",
        "crlf_carriage_return_is_not_a_blank_line", "crlf_fence_is_not_recognized",
        "inline_backticks_do_not_open_a_fence", "unclosed_fence_keeps_remainder_in_tail",
        "indented_fence_preserves_original_spaces", "tilde_fence_requires_tilde_closer",
        "leading_blank_line_can_finish_an_empty_block",
    ]
}

private extension Collection {
    subscript(safe index: Index) -> Element? { indices.contains(index) ? self[index] : nil }
}
