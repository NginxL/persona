package node

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Antigravity CLI (agy) settings in ~/.gemini/antigravity-cli/settings.json, and Oh My Pi (omp) settings in
// ~/.omp/agent/config.yml. Both follow ClaudeSetting's shape so AgentSettings in the web / app renders them.

var agySettings = []ClaudeSetting{
	{Key: "model", Title: "默认模型", Detail: "没有指定模型时使用 Antigravity 自己的默认模型", Type: "string", Default: ""},
	{Key: "agentMode", Title: "执行模式", Detail: "改文件、跑命令时的默认模式；auto 自动放行安全操作，需要时仍会询问", Type: "enum", Options: []string{"default", "accept-edits", "auto", "plan"}, Default: "default"},
	{Key: "toolPermission", Title: "工具权限", Detail: "调用工具或跑命令前要不要问你；选 always-proceed 会自动放行所有权限检查（全部放行）", Type: "enum", Options: []string{"ask", "always-proceed"}, Default: "ask"},
	{Key: "modelProvider", Title: "模型接入方式", Detail: "oauth 走 Google / Antigravity 登录；填了 GOOGLE_GEMINI_BASE_URL + GEMINI_API_KEY 时选 gemini", Type: "enum", Options: []string{"oauth", "gemini"}, Default: "oauth"},
	{Key: "showFeedbackSurvey", Title: "显示反馈问卷", Detail: "在终端里偶尔弹出反馈调查", Type: "bool", Default: true},
}

func (n *Node) agyDataRoot() string {
	if dir := os.Getenv("ANTIGRAVITY_CLI_CONFIG_DIR"); dir != "" {
		return dir
	}
	return filepath.Join(n.home, ".gemini", "antigravity-cli")
}

func (n *Node) agyConfigPath() string {
	p := filepath.Join(n.agyDataRoot(), "settings.json")
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	return p
}

func (n *Node) getAgySettings() ([]ClaudeSetting, error) {
	raw, err := readSettingsFile(n.agyConfigPath())
	if err != nil {
		return nil, err
	}
	out := make([]ClaudeSetting, len(agySettings))
	for i, s := range agySettings {
		s.Value = raw[s.Key]
		if s.Key == "model" {
			if catalog, ok := n.agyRuntimeModels(); ok && len(catalog.Models) > 0 {
				s.Type = "enum"
				for _, model := range catalog.Models {
					s.Options = append(s.Options, model.Name)
				}
				if value, ok := s.Value.(string); ok && !slices.Contains(s.Options, value) {
					s.Options = append(s.Options, value)
				}
			}
		}
		out[i] = s
	}
	return out, nil
}

func (n *Node) setAgySetting(key string, value any) error {
	if key == "agentMode" && value == "bypassPermissions" {
		value = "auto" // older clients used Persona's permission name in the native settings field
	}
	var def *ClaudeSetting
	for i := range agySettings {
		if agySettings[i].Key == key {
			def = &agySettings[i]
		}
	}
	if def == nil {
		return fmt.Errorf("setting %q is not editable from the app", key)
	}
	if err := checkSettingValue(*def, value); err != nil {
		return err
	}
	if key == "modelProvider" && value == "oauth" {
		value = nil
	}
	return writeSetting(n.agyConfigPath(), key, value)
}

var ompSettings = []ClaudeSetting{
	{Key: "modelRoles.default", Title: "默认模型", Detail: "omp 默认使用的模型，如 google-antigravity/gemini-4-argon:high", Type: "string", Default: ""},
	{Key: "defaultThinkingLevel", Title: "思考强度", Detail: "默认思考等级；auto 由模型自己决定", Type: "enum", Options: []string{"auto", "off", "minimal", "low", "medium", "high", "xhigh", "max"}, Default: "auto"},
	{Key: "steeringMode", Title: "插话模式", Detail: "模型正在工作时发消息怎么排队", Type: "enum", Options: []string{"one-at-a-time", "immediate", "after-turn"}, Default: "one-at-a-time"},
	{Key: "hideThinkingBlock", Title: "隐藏思考过程", Detail: "在终端里折叠思考块（不影响模型思考）", Type: "bool", Default: false},
	{Key: "computer.enabled", Title: "操控电脑 (Computer Use)", Detail: "允许 omp 在 eval 中使用桌面自动化工具", Type: "bool", Default: true},
	{Key: "github.enabled", Title: "GitHub 工具", Detail: "启用内置 GitHub issue / PR / 代码工具", Type: "bool", Default: true},
	{Key: "astGrep.enabled", Title: "AST Grep 工具", Detail: "启用语法树结构搜索与重构工具", Type: "bool", Default: true},
	{Key: "autolearn.enabled", Title: "自动学习 (Autolearn)", Detail: "会话结束后自动提炼记忆", Type: "bool", Default: false},
}

func (n *Node) ompConfigPath() string {
	p := filepath.Join(n.home, ".omp", "agent", "config.yml")
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	return p
}

func (n *Node) getOmpSettings() ([]ClaudeSetting, error) {
	b, err := os.ReadFile(n.ompConfigPath())
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	vals := yamlSimpleParse(string(b))
	out := make([]ClaudeSetting, len(ompSettings))
	for i, s := range ompSettings {
		if v, ok := vals[s.Key]; ok {
			s.Value = v
		}
		out[i] = s
	}
	return out, nil
}

func (n *Node) setOmpSetting(key string, value any) error {
	var def *ClaudeSetting
	for i := range ompSettings {
		if ompSettings[i].Key == key {
			def = &ompSettings[i]
		}
	}
	if def == nil {
		return fmt.Errorf("setting %q is not editable from the app", key)
	}
	if err := checkSettingValue(*def, value); err != nil {
		return err
	}
	path := n.ompConfigPath()
	agentConfigMu.Lock()
	defer agentConfigMu.Unlock()
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	text := yamlSimpleSet(string(b), key, value)
	return writeConfigFile(path, []byte(text), 0o600)
}

// yamlLine splits a `key: value` line into its indent, key and value, with a trailing comment removed from the value
// (`computer: # desktop` is a section header). ok is false for blank lines, comments and lines without a key.
func yamlLine(line string) (indent int, k, v string, ok bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return 0, "", "", false
	}
	k, v, ok = strings.Cut(trimmed, ":")
	if !ok {
		return 0, "", "", false
	}
	v = strings.TrimSpace(v)
	if strings.HasPrefix(v, "#") {
		v = ""
	} else if i := strings.Index(v, " #"); i >= 0 {
		v = strings.TrimSpace(v[:i])
	}
	return len(line) - len(strings.TrimLeft(line, " ")), strings.TrimSpace(k), v, true
}

func yamlSimpleParse(text string) map[string]any {
	out := map[string]any{}
	var section string
	childIndent := 0 // the indent of the current section's keys: its first key's (2, 4, …)
	for _, line := range strings.Split(text, "\n") {
		indent, k, v, ok := yamlLine(line)
		if !ok {
			continue
		}
		if indent == 0 {
			section, childIndent = "", 0
			if v == "" {
				section = k
				continue
			}
			if parsed, ok := yamlScalar(v); ok {
				out[k] = parsed
			}
			continue
		}
		if section == "" {
			continue
		}
		if childIndent == 0 {
			childIndent = indent
		}
		if indent == childIndent && v != "" {
			if parsed, ok := yamlScalar(v); ok {
				out[section+"."+k] = parsed
			}
		}
	}
	return out
}

func yamlScalar(raw string) (any, bool) {
	if i := strings.Index(raw, " #"); i >= 0 {
		raw = strings.TrimSpace(raw[:i])
	}
	switch raw {
	case "true":
		return true, true
	case "false":
		return false, true
	}
	if len(raw) >= 2 && ((raw[0] == '"' && raw[len(raw)-1] == '"') || (raw[0] == '\'' && raw[len(raw)-1] == '\'')) {
		return raw[1 : len(raw)-1], true
	}
	return raw, raw != ""
}

func yamlFormatScalar(v any) string {
	switch x := v.(type) {
	case bool:
		if x {
			return "true"
		}
		return "false"
	case string:
		if strings.ContainsAny(x, ":#{}[]&*!|>'\"%@`") || strings.TrimSpace(x) != x {
			return fmt.Sprintf("%q", x)
		}
		return x
	default:
		return fmt.Sprint(v)
	}
}

func yamlSimpleSet(text, key string, value any) string {
	lines := strings.Split(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	remove := value == nil || value == ""
	parent, child, nested := strings.Cut(key, ".")
	if !nested {
		prefix := key + ":"
		for i, line := range lines {
			if len(line)-len(strings.TrimLeft(line, " ")) == 0 && strings.HasPrefix(strings.TrimSpace(line), prefix) {
				if remove {
					lines = append(lines[:i], lines[i+1:]...)
				} else {
					lines[i] = key + ": " + yamlFormatScalar(value)
				}
				return strings.Join(lines, "\n") + "\n"
			}
		}
		if !remove {
			lines = append(lines, key+": "+yamlFormatScalar(value))
		}
		return strings.Join(lines, "\n") + "\n"
	}

	secIdx := -1
	for i, line := range lines {
		if indent, k, v, ok := yamlLine(line); ok && indent == 0 && k == parent && v == "" {
			secIdx = i
			break
		}
	}
	if secIdx >= 0 {
		endIdx := len(lines)
		childIndent := 0 // the section's own indent, so a 4-space block stays 4-space
		for j := secIdx + 1; j < len(lines); j++ {
			indent, k, _, ok := yamlLine(lines[j])
			if !ok {
				continue
			}
			if indent == 0 {
				endIdx = j
				break
			}
			if childIndent == 0 {
				childIndent = indent
			}
			if indent == childIndent && k == child {
				if remove {
					lines = append(lines[:j], lines[j+1:]...)
				} else {
					lines[j] = strings.Repeat(" ", indent) + child + ": " + yamlFormatScalar(value)
				}
				return strings.Join(lines, "\n") + "\n"
			}
		}
		if childIndent == 0 {
			childIndent = 2
		}
		if !remove {
			next := append([]string{}, lines[:endIdx]...)
			next = append(next, strings.Repeat(" ", childIndent)+child+": "+yamlFormatScalar(value))
			next = append(next, lines[endIdx:]...)
			lines = next
		}
		return strings.Join(lines, "\n") + "\n"
	}
	if !remove {
		lines = append(lines, parent+":", "  "+child+": "+yamlFormatScalar(value))
	}
	return strings.Join(lines, "\n") + "\n"
}
