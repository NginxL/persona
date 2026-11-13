# GitHub star prompt fixtures

Android (with an iOS proposal in #5910) asks for a GitHub star in three places (#5761, `docs/design/DESIGN_SYSTEM.md`,
"GitHub star prompts"): a pill on the empty Agents page, a card in the Agents list's notice slot once this device has
started enough Agents, and a toast when first setup finishes. These cases pin that client's rules. The web removed
all three prompts in #6570 and instead shows the project source in Settings; it no longer consumes these fixtures.

| Rule | Cases |
| --- | --- |
| The pill shows until it is hidden or the user starred. | `a_fresh_install_shows_only_the_pill`, `hiding_the_pill_leaves_the_card` |
| Each Agent this device starts counts once; the card shows from the threshold (20 at first). | `a_launch_counts_and_reaches_the_threshold`, `the_card_waits_below_the_threshold` |
| Later (or ✕) on the card doubles the threshold until it is past the launches so far and holds the card for three days. | `later_doubles_the_threshold_and_waits_three_days`, `later_doubles_past_the_launches_so_far`, `the_wait_holds_even_past_the_threshold`, `the_card_returns_after_the_wait` |
| Star on GitHub, from any prompt, turns every prompt off on this device; launches still count. | `star_turns_every_prompt_off`, `launches_still_count_after_star` |
| The onboarding toast shows once per device, never after a star. | `onboarding_shows_the_toast_once`, `onboarding_again_shows_no_toast`, `no_toast_after_star` |

- Structure: `{"version": 1, "cases": [...]}`. Each case has the device's `state` (`launches`, `threshold`,
  `snoozedUntil` in Unix milliseconds or null, `done`, `pillHidden`, `welcomed`), the time `now` (Unix milliseconds),
  and an `event` applied at `now`: `launch`, `later`, `star`, `hidePill`, `onboarded` or null.
- `expected.state` is the state after the event; `expected.pill` and `expected.card` say whether each shows afterwards;
  `expected.toast` whether the event showed the onboarding toast.
- Not in these cases, because they belong to the screen: the card also waits while a session needs the user or a search
  is on, and the pill shows only on the empty Agents surface.
- Run: `cd android && ./gradlew :core:domain:test --tests '*StarPromptFixtureTest*'`;
  the proposed iOS implementation replays them with #5910.
