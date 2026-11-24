package lark

import "time"

// The module's times, by name: the defaults, which Host.Timings overrides (a test and tuning knob of the core's,
// assistant.Options.LarkTimings; never a user setting).
var (
	defaultTimings = map[string]time.Duration{
		// answer_card_wait is how long the answer waits out a refused update of the progress card before giving up on
		// the card: the run is over, so no later update writes it again (cardBackoffFirst).
		"answer_card_wait": cardBackoffFirst,
		// kit_check_every is how often an entity's message is read back to see it is still there (kitGone).
		"kit_check_every": 30 * time.Second,
		// progress_after and progress_idle: the Feishu card comes a second or two after the message and its elapsed
		// time moves every few seconds, new steps or not: a long turn must not look stuck.
		"progress_after": time.Second,
		"progress_idle":  3 * time.Second,
	}
	defaultTimingLists = map[string][]time.Duration{
		// cot_end_tries are the waits between tries of cotEnd (a rate limit that hit the end likely hits the first
		// try too).
		"cot_end_tries": {0, time.Second, 3 * time.Second},
	}
)

// timing is the time called name: the host's, else the default.
func (s core) timing(name string) time.Duration {
	if one, _ := s.h.Timings(); one != nil {
		if d, ok := one[name]; ok {
			return d
		}
	}
	return defaultTimings[name]
}

// timingList is the list of times called name: the host's, else the default.
func (s core) timingList(name string) []time.Duration {
	if _, lists := s.h.Timings(); lists != nil {
		if l, ok := lists[name]; ok {
			return l
		}
	}
	return defaultTimingLists[name]
}
