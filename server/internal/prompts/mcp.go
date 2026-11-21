package prompts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"sync"
)

// MCPTool is one MCP tool as the model sees it: what it is for and its arguments (with their descriptions).
type MCPTool struct {
	Name        string
	Description string
	InputSchema json.RawMessage // compact
}

// mcp/<scene>/<tool>.md: the description (one paragraph), an empty line, then the input schema in a ```json block.
// Rendered with the caller's Lang; the empty language preserves the historical bytes.
var mcpTools = sync.OnceValue(func() map[string]MCPTool { return loadMCP("") })
var mcpEnglish = sync.OnceValue(func() map[string]MCPTool { return loadMCP("en") })
var mcpJapanese = sync.OnceValue(func() map[string]MCPTool { return loadMCP("ja") })

// MCPData carries the owner language, never request text or source content.
type MCPData struct{ Lang string }

func mcpForLanguage(lang string) map[string]MCPTool {
	switch lang {
	case "", "zh-Hans":
		return mcpTools()
	case "ja":
		return mcpJapanese()
	default:
		return mcpEnglish()
	}
}

const schemaFence = "\n\n```json\n"

func loadMCP(lang string) map[string]MCPTool {
	out := map[string]MCPTool{}
	err := fs.WalkDir(files, "mcp", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw := render(strings.TrimSuffix(p, ".md"), MCPData{Lang: lang}) + "\n"
		desc, rest, ok := strings.Cut(raw, schemaFence)
		schema, ok2 := strings.CutSuffix(rest, "\n```\n")
		if !ok || !ok2 || desc == "" {
			return fmt.Errorf("%s: want the description, an empty line and a ```json block with the input schema", p)
		}
		var b bytes.Buffer
		if err := json.Compact(&b, []byte(schema)); err != nil {
			return fmt.Errorf("%s: input schema: %w", p, err)
		}
		name := strings.TrimSuffix(path.Base(p), ".md")
		if _, dup := out[name]; dup {
			return fmt.Errorf("%s: tool %s defined twice", p, name)
		}
		out[name] = MCPTool{Name: name, Description: desc, InputSchema: b.Bytes()}
		return nil
	})
	if err != nil {
		panic("prompts: " + err.Error())
	}
	return out
}

// Tool is the MCP tool named name (mcp/<scene>/<name>.md); an unknown name is a bug and panics.
func Tool(name string, language ...string) MCPTool {
	lang := ""
	if len(language) > 0 {
		lang = language[0]
	}
	t, ok := mcpForLanguage(lang)[name]
	if !ok {
		panic("prompts: no MCP tool " + name)
	}
	t.InputSchema = bytes.Clone(t.InputSchema)
	return t
}

// LocalizeTool renders only an unchanged built-in definition. Connector/phone/extension
// authored text, including a tool reusing a built-in name, remains source data.
// Leader tools are exposed under aliases but retain their restricted definitions.
func LocalizeTool(t MCPTool, lang string) MCPTool {
	names := []string{t.Name}
	switch t.Name {
	case "task_plan":
		names = append(names, "leader_task_plan")
	case "task_review":
		names = append(names, "leader_task_review")
	case "delegate_task":
		names = append(names, "meeting_delegate_task")
	}
	for _, name := range names {
		if _, ok := mcpTools()[name]; !ok {
			continue
		}
		for _, source := range []string{"", "en", "ja"} {
			original, ok := mcpForLanguage(source)[name]
			if !ok || t.Description != original.Description || !bytes.Equal(t.InputSchema, original.InputSchema) {
				continue
			}
			localized := Tool(name, lang)
			localized.Name = t.Name
			return localized
		}
	}
	return t
}
