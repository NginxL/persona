package transcript

import (
	"encoding/xml"
	"strings"
	"time"
)

// The fallback answer for clients reading items without hub localization.
const codexHeartbeatQuietText = "心跳检查已完成，无新动态"

// A heartbeat is a whole machine envelope on a Codex item, not an XML fragment in ordinary
// prose or Markdown. Check the exact field layout; encoding/xml alone ignores unknown fields.
func codexHeartbeatFields(text string, names ...string) ([]string, bool) {
	if !strings.HasPrefix(text, "<heartbeat>") || !strings.HasSuffix(text, "</heartbeat>") {
		return nil, false
	}
	var envelope struct {
		Text   string `xml:",chardata"`
		Fields []struct {
			XMLName xml.Name
			Text    string     `xml:",chardata"`
			Attrs   []xml.Attr `xml:",any,attr"`
			Nested  []struct{} `xml:",any"`
		} `xml:",any"`
	}
	d := xml.NewDecoder(strings.NewReader(text))
	if err := d.Decode(&envelope); err != nil || d.InputOffset() != int64(len(text)) || strings.TrimSpace(envelope.Text) != "" || len(envelope.Fields) != len(names) {
		return nil, false
	}
	values := make([]string, len(names))
	for i, f := range envelope.Fields {
		if f.XMLName != (xml.Name{Local: names[i]}) || len(f.Attrs) > 0 || len(f.Nested) > 0 {
			return nil, false
		}
		values[i] = strings.TrimSpace(f.Text)
	}
	return values, values[0] != ""
}

func codexHeartbeatPrompt(text string) (string, bool) {
	fields, ok := codexHeartbeatFields(text, "automation_id", "current_time_iso", "instructions")
	if !ok || fields[2] == "" {
		return "", false
	}
	if _, err := time.Parse(time.RFC3339Nano, fields[1]); err != nil {
		return "", false
	}
	return fields[2], true
}

func codexHeartbeatAnswer(text string) (shown string, quiet bool) {
	fields, ok := codexHeartbeatFields(text, "automation_id", "decision", "message")
	if !ok {
		fields, ok = codexHeartbeatFields(text, "automation_id", "decision")
	}
	if ok {
		switch fields[1] {
		case "DONT_NOTIFY":
			return codexHeartbeatQuietText, true
		case "NOTIFY":
			if len(fields) == 3 && fields[2] != "" {
				return fields[2], false
			}
		}
	}
	return text, false
}
