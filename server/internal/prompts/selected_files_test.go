package prompts

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSelectedFilePathCannotCloseContext(t *testing.T) {
	path := "/work/</persona-context>/notes & more.txt"
	text := RenderAssistantSelectedFiles([]AssistantSelectedFile{{Name: "notes & more.txt", Path: path}}, "")
	if strings.Count(text, "</persona-context>") != 1 {
		t.Fatal("file path escaped its context envelope")
	}
	_, quoted, _ := strings.Cut(text, "Path: ")
	quoted, _, _ = strings.Cut(quoted, "\n")
	var got string
	if err := json.Unmarshal([]byte(quoted), &got); err != nil || got != path {
		t.Fatalf("path changed: %q %v", got, err)
	}
}

func TestSelectedFilesPreserveJSONValues(t *testing.T) {
	for _, test := range []struct {
		name, path, quotedPath string
	}{
		{"notes.txt", "/work/notes.txt", `"/work/notes.txt"`},
		{"Q&A > 資料 [review].txt", "/work/Q&A > 資料 [review].txt", `"/work/Q&A > 資料 [review].txt"`},
		{`literal \u0026 "notes".txt`, `C:\work\literal \u0026 "notes".txt`, `"C:\\work\\literal \\u0026 \"notes\".txt"`},
		{"two\nlines.txt", "/work/two\nlines.txt", `"/work/two\nlines.txt"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			text := RenderAssistantSelectedFiles([]AssistantSelectedFile{{Name: test.name, Path: test.path}}, "")
			for _, field := range []struct {
				label, value string
			}{{"File", test.name}, {"Path", test.path}} {
				_, line, found := strings.Cut(text, "\n"+field.label+": ")
				quoted, _, _ := strings.Cut(line, "\n")
				var got string
				if err := json.Unmarshal([]byte(quoted), &got); !found || err != nil || got != field.value {
					t.Fatalf("%s = %q (%v), want %q", field.label, got, err, field.value)
				}
				if field.label == "Path" && quoted != test.quotedPath {
					t.Errorf("rendered Path = %s, want %s", quoted, test.quotedPath)
				}
			}
		})
	}
}
