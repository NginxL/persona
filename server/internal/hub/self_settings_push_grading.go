package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/nwverse/persona/server/internal/i18n"
)

// persona_settings notifications.low_distraction: the push grading switch (push_grading.go), the one Settings › Notifications
// shows on every client and GET / PUT /api/v1/push/ln-mode reads and writes. get answers what the route's GET answers;
// set is a card on the phone and changes the switch only once the owner approves it, like every hub setting here.
const pushGradingSelfSettingKey = "notifications.low_distraction"

func (h *Hub) pushGradingSelfSetting() selfSetting {
	return selfSetting{key: pushGradingSelfSettingKey, title: i18n.T(h.userLang(), "hub-self_settings_lnmode.title"),
		about: "low-distraction mode for every device of the owner (Settings › Notifications; the hub's push grading switch, " +
			"GET /api/v1/push/ln-mode). Off (the default): every push as before. On: only what needs the owner rings " +
			"(questions, plan and tool approvals, decisions only they can make, their reminders, main-conversation notices); " +
			"task results, \"Please verify\" asks, task progress, other conversations' notify_user and failures wait for the " +
			"briefing and the Held pushes list, and finished Agent turns are silent. Ask the owner before proposing it; never " +
			"turn it on just to quiet one task",
		value: "true or false",
		get:   func(context.Context) (string, error) { return fmt.Sprint(h.push.pushGradingOn()), nil },
		set:   h.setPushGradingSelf}
}

func (h *Hub) setPushGradingSelf(_ context.Context, v json.RawMessage) (*selfChange, error) {
	var on bool
	if err := json.Unmarshal(v, &on); err != nil {
		return nil, errors.New("value must be true or false")
	}
	l := h.userLang()
	before := h.push.pushGradingOn()
	if on == before {
		return &selfChange{summary: fmt.Sprintf("%s is already %v: nothing changed.", pushGradingSelfSettingKey, on)}, nil
	}
	word := map[bool]string{true: "hub-self_settings_lnmode.on", false: "hub-self_settings_lnmode.off"}
	what := map[bool]string{true: "hub-self_settings_lnmode.when_on", false: "hub-self_settings_lnmode.when_off"}
	return &selfChange{summary: i18n.T(l, "hub-self_settings_lnmode.change",
		i18n.Args{"before": i18n.T(l, word[before]), "after": i18n.T(l, word[on]), "what": i18n.T(l, what[on])}),
		apply: func(ctx context.Context, _ map[string]string) (string, error) {
			if err := h.setPushGrading(ctx, on); err != nil {
				return "", err
			}
			return fmt.Sprintf("%s is %v for every device from now on (Settings › Notifications shows it).", pushGradingSelfSettingKey, on), nil
		}}, nil
}
