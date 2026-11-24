// Package office writes Word, PowerPoint and Excel files for the assistant and agents (`personad office`, the office
// skill, #1300): a document or a deck from Markdown, a workbook from JSON or CSV, and reads any of them back for a
// look at what is in it (check). It writes the smallest Office Open XML the three need, itself: no Office, Python or
// Node on the machine, and no library whose license Persona could not ship.
package office

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

const usage = `usage:
  personad office docx <file.md> [-o out.docx] [--title T]   a Word document from Markdown
  personad office pptx <file.md> [-o out.pptx] [--title T]   a 16:9 deck from Markdown: --- or a # / ## heading starts a slide
  personad office xlsx <file.json|file.csv> [-o out.xlsx]   a workbook from JSON ({"sheets":[…]}) or CSV
  personad office check <file.docx|.xlsx|.pptx>              what is in it, to look over what was made
Writing prints {"file": <absolute path>, …what check says}. Exit 0 ok, 1 failed, 2 usage.`

// Usage is the command's lines in personad's usage text.
const Usage = `  personad office docx|pptx|xlsx|check …
                           write a Word document or a deck from Markdown, a workbook from JSON / CSV, or say what is in
                           one (the office skill): personad office --help`

// Main runs `personad office args…`: it writes or checks a file and prints JSON on stdout; a failure is one line on
// stderr.
func Main(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	kind := args[0]
	fs := flag.NewFlagSet("office "+kind, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	out := fs.String("o", "", "")
	title := fs.String("title", "", "")
	// Flags may come after the input as well as before it.
	var inputs []string
	rest := args[1:]
	for {
		if err := fs.Parse(rest); err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		if fs.NArg() == 0 {
			break
		}
		inputs = append(inputs, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	if len(inputs) != 1 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	in := inputs[0]
	if kind == "check" {
		info, err := Check(in)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return printJSON(stdout, stderr, info)
	}
	ext := map[string]string{"docx": ".docx", "pptx": ".pptx", "xlsx": ".xlsx"}[kind]
	if ext == "" {
		fmt.Fprintf(stderr, "unknown office command %q\n%s\n", kind, usage)
		return 2
	}
	if *out == "" {
		*out = strings.TrimSuffix(in, filepath.Ext(in)) + ext
	}
	// Only a file of its kind: Persona's agents may run this without asking (docs/assistant/office.md "No approval every time"), so -o
	// must not write over ~/.zshrc or anything else.
	if !strings.EqualFold(filepath.Ext(*out), ext) {
		fmt.Fprintf(stderr, "office %s writes a %s file: -o %s\n", kind, ext, *out)
		return 2
	}
	abs, err := filepath.Abs(*out)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	// Never over the input, however -o names it (`office docx notes.docx`, a link, another case of the name): that
	// would read the document as Markdown and replace it with what that makes.
	if outInfo, err := os.Stat(abs); err == nil {
		if inInfo, err := os.Stat(in); err == nil && os.SameFile(inInfo, outInfo) {
			fmt.Fprintf(stderr, "office %s would write over its input %s: give -o another file\n", kind, in)
			return 2
		}
	}
	src, err := os.ReadFile(in)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	// An Office file is a zip, never the text these are made from.
	if bytes.HasPrefix(src, []byte("PK\x03\x04")) {
		fmt.Fprintf(stderr, "office %s: %s is an Office file, not Markdown, JSON or CSV\n", kind, in)
		return 2
	}
	// An earlier output (a file; a link is replaced, its target never opened) is replaced only if the user may write
	// it, as writing into it was (cp refuses a read-only file too), and keeps its permissions. The probe does not
	// truncate, so it changes nothing. It runs after the checks on the input, so their errors come first, and before
	// the conversion, so a refused output costs no work.
	var keep *os.FileMode
	if st, err := os.Lstat(abs); err == nil && st.Mode().IsRegular() {
		f, err := os.OpenFile(abs, os.O_WRONLY, 0)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		f.Close()
		perm := st.Mode().Perm()
		keep = &perm
	}
	dir := filepath.Dir(in)
	if *title == "" {
		*title = strings.TrimSuffix(filepath.Base(*out), ext)
	}
	var data []byte
	switch kind {
	case "docx":
		data, err = Docx(src, dir, *title)
	case "pptx":
		data, err = Pptx(src, dir, *title)
	case "xlsx":
		if strings.EqualFold(filepath.Ext(in), ".csv") {
			data, err = XlsxFromCSV(src, strings.TrimSuffix(filepath.Base(in), filepath.Ext(in)), *title)
		} else {
			data, err = Xlsx(src, *title)
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	// In one step (a temporary file beside it, renamed over it): a write that fails part way, on a full disk say,
	// leaves the earlier file as it was. A link at -o is replaced, not written through.
	if err := replaceFile(abs, data, keep); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	info, err := Check(abs)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return printJSON(stdout, stderr, map[string]any{"file": abs, "bytes": len(data), "check": info})
}

// replaceFile writes path in one step: a temporary file beside it, synced, then renamed over it. A new file gets
// 0644 less the umask, as os.WriteFile gives it; keep, when set, is the mode of the file it replaces.
func replaceFile(path string, data []byte, keep *os.FileMode) error {
	dir, base := filepath.Dir(path), filepath.Base(path)
	var f *os.File
	for i := 0; ; i++ {
		// Named after the file (not hidden), as managerconf.WriteFileAtomic names its own. Not os.CreateTemp: it
		// makes the file 0600, and a fixed chmod after it would ignore the umask.
		name := filepath.Join(dir, base+".tmp-"+strconv.FormatUint(rand.Uint64(), 36))
		var err error
		if f, err = os.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o644); err == nil {
			break
		}
		if !errors.Is(err, os.ErrExist) || i == 99 {
			return err
		}
	}
	tmp := f.Name()
	err := func() error {
		if keep != nil {
			if err := f.Chmod(*keep); err != nil {
				return err
			}
		}
		if _, err := f.Write(data); err != nil {
			return err
		}
		return f.Sync()
	}()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	// The rename itself must survive a power cut too.
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}

func printJSON(stdout, stderr io.Writer, v any) int {
	enc := json.NewEncoder(stdout)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

// part is one file of the package.
type part struct {
	name string
	data string
}

// pack zips the parts, [Content_Types].xml first (as Office writes them).
func pack(parts []part) ([]byte, error) {
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	for _, p := range parts {
		w, err := z.CreateHeader(&zip.FileHeader{Name: p.name, Method: zip.Deflate, Modified: time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)})
		if err != nil {
			return nil, err
		}
		if _, err := io.WriteString(w, p.data); err != nil {
			return nil, err
		}
	}
	if err := z.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// esc is s as XML text or an attribute's value, without the characters XML 1.0 does not allow.
func esc(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '&':
			b.WriteString("&amp;")
		case r == '<':
			b.WriteString("&lt;")
		case r == '>':
			b.WriteString("&gt;")
		case r == '"':
			b.WriteString("&quot;")
		case r == '\t' || r == '\n' || r == '\r' || r >= 0x20 && r < 0xD800 || r >= 0xE000 && r < 0xFFFE || r >= 0x10000 && r <= 0x10FFFF:
			b.WriteRune(r)
		}
	}
	return b.String()
}

const xmlHead = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n"

// coreProps is docProps/core.xml: the title, made by Persona, now.
func coreProps(title string) string {
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	return xmlHead + `<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" ` +
		`xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:dcterms="http://purl.org/dc/terms/" ` +
		`xmlns:dcmitype="http://purl.org/dc/dcmitype/" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">` +
		`<dc:title>` + esc(title) + `</dc:title><dc:creator>Persona</dc:creator><cp:lastModifiedBy>Persona</cp:lastModifiedBy>` +
		`<dcterms:created xsi:type="dcterms:W3CDTF">` + now + `</dcterms:created>` +
		`<dcterms:modified xsi:type="dcterms:W3CDTF">` + now + `</dcterms:modified></cp:coreProperties>`
}

// appProps is docProps/app.xml, with what the application shows about the file (extra: its own elements).
func appProps(extra string) string {
	return xmlHead + `<Properties xmlns="http://schemas.openxmlformats.org/officeDocument/2006/extended-properties" ` +
		`xmlns:vt="http://schemas.openxmlformats.org/officeDocument/2006/docPropsVTypes"><Application>Persona</Application>` +
		extra + `</Properties>`
}

// rootRels is _rels/.rels: the main part and the properties.
func rootRels(mainType, main string) string {
	return xmlHead + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
		`<Relationship Id="rId1" Type="` + mainType + `" Target="` + main + `"/>` +
		`<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties" Target="docProps/core.xml"/>` +
		`<Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/extended-properties" Target="docProps/app.xml"/>` +
		`</Relationships>`
}

const officeDocRel = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument"

// rels is a part's relationships file: id → type and target (external for a link).
type rels struct {
	list []string
}

func (r *rels) add(typ, target string, external bool) string {
	id := fmt.Sprintf("rId%d", len(r.list)+1)
	mode := ""
	if external {
		mode = ` TargetMode="External"`
	}
	r.list = append(r.list, `<Relationship Id="`+id+`" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/`+typ+
		`" Target="`+esc(target)+`"`+mode+`/>`)
	return id
}

func (r *rels) xml() string {
	return xmlHead + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` + strings.Join(r.list, "") + `</Relationships>`
}

// contentTypes is [Content_Types].xml: the defaults by extension and the parts by name.
func contentTypes(overrides map[string]string) string {
	var b strings.Builder
	b.WriteString(xmlHead + `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
		`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
		`<Default Extension="xml" ContentType="application/xml"/>` +
		`<Default Extension="png" ContentType="image/png"/><Default Extension="jpeg" ContentType="image/jpeg"/>` +
		`<Default Extension="gif" ContentType="image/gif"/>` +
		`<Override PartName="/docProps/core.xml" ContentType="application/vnd.openxmlformats-package.core-properties+xml"/>` +
		`<Override PartName="/docProps/app.xml" ContentType="application/vnd.openxmlformats-officedocument.extended-properties+xml"/>`)
	for _, name := range slices.Sorted(maps.Keys(overrides)) {
		b.WriteString(`<Override PartName="/` + name + `" ContentType="` + overrides[name] + `"/>`)
	}
	b.WriteString(`</Types>`)
	return b.String()
}

var errEmpty = errors.New("nothing to write: the input has no content")
