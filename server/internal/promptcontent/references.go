// Package promptcontent reads the shared leading context envelope used by composers and headless turns.
package promptcontent

import (
	"encoding/xml"
	"errors"
	"strings"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/lazyre"
)

var leading = lazyre.New(`(?s)^\s*<` + compat.ContextPattern() + `\b([^>]*)>.*?</` + compat.ContextPattern() + `>`)

// Split returns the scheduled picker envelope. A blank line ends the selections and begins literal prose.
func Split(text string) (prefix, body string) {
	rest := text
	for {
		if rest != text {
			space := rest[:len(rest)-len(strings.TrimLeft(rest, " \t\r\n"))]
			if strings.Count(space, "\n") >= 2 {
				break
			}
		}
		m := leading.FindStringIndex(rest)
		if m == nil {
			break
		}
		rest = rest[m[1]:]
	}
	return text[:len(text)-len(rest)], rest
}

type File struct{ Path, Machine, Cwd, Launcher, Runtime string }

var ErrFile = errors.New("selected file is unavailable or its execution environment changed; remove it and select it again")

// Files recognizes explicit file selections only. Display text never supplies executable paths.
func Files(text string) ([]File, error) {
	var out []File
	for {
		m := leading.FindStringSubmatchIndex(text)
		if m == nil {
			return out, nil
		}
		tag := text[m[2]:m[3]]
		text = text[m[1]:]
		token, err := xml.NewDecoder(strings.NewReader("<context" + tag + "/>")).Token()
		if err != nil {
			if strings.Contains(tag, `"file"`) {
				return nil, ErrFile
			}
			continue
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		file := false
		for _, a := range start.Attr {
			file = file || a.Name.Local == "kind" && a.Value == "file"
		}
		if !file {
			continue
		}
		attrs := map[string]string{}
		for _, a := range start.Attr {
			if _, dup := attrs[a.Name.Local]; dup || a.Name.Space != "" {
				return nil, ErrFile
			}
			switch a.Name.Local {
			case "kind", "title", "url", "path", "machine", "cwd", "launcher", "runtime":
			default:
				return nil, ErrFile
			}
			if strings.ContainsAny(a.Value, "\x00\r\n") || len(a.Value) > 8192 {
				return nil, ErrFile
			}
			attrs[a.Name.Local] = a.Value
		}
		if attrs["path"] == "" || attrs["machine"] == "" || attrs["cwd"] == "" || attrs["launcher"] == "" || len(out) >= 16 {
			return nil, ErrFile
		}
		out = append(out, File{attrs["path"], attrs["machine"], attrs["cwd"], attrs["launcher"], attrs["runtime"]})
	}
}
