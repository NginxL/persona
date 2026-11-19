package devices

import (
	"encoding/json"
	"strings"
)

// The manager supplies the language; neither clients nor caller-supplied spelling
// variants may choose it. Old nodes ignore this optional display-only field.
func (d *Devices) promptLanguageParams(method string, raw json.RawMessage) (json.RawMessage, error) {
	if method != "session.send" && method != "codex.answer" {
		return raw, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	if fields == nil {
		fields = map[string]json.RawMessage{}
	}
	for key := range fields {
		if strings.EqualFold(key, "prompt_language") {
			delete(fields, key)
		}
	}
	if d.opts.PromptLanguage != nil {
		if language := d.opts.PromptLanguage(); language != "" {
			fields["prompt_language"], _ = json.Marshal(language)
		}
	}
	return json.Marshal(fields)
}
