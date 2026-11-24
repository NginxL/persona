package office

import (
	"bufio"
	"bytes"
	"fmt"
	"html"
	"image"
	_ "image/gif" // image.DecodeConfig for a picture's size
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	gmhtml "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// The Markdown a document or a deck is written in (GitHub's: tables, strikethrough, task lists), as blocks of runs
// both writers lay out their own way.

type run struct {
	text                       string
	bold, italic, code, strike bool
	link                       string // the URL it goes to
	br                         bool   // a line break, no text
}

type blockKind int

const (
	bHeading blockKind = iota
	bPara
	bItem  // a list item's first paragraph (later ones are bPara at its depth)
	bCode  // a code block's lines
	bQuote // a paragraph in a quote
	bTable
	bImage
	bBreak // --- : a page break in a document, a new slide in a deck
)

type block struct {
	kind    blockKind
	level   int // a heading's (1–6); a list item's depth (0 outermost), and a paragraph's under one
	ordered bool
	cont    bool // a paragraph that goes on with the list item above it
	list    int  // which list an item is in (numbering restarts with each)
	start   int  // the list's first number
	runs    []run
	lines   []string // a code block's
	table   *table
	img     *picture
}

type table struct {
	align []east.Alignment
	rows  [][][]run // the first is the header
}

type picture struct {
	alt  string
	data []byte
	ext  string // png, jpeg, gif
	w, h int    // pixels
}

// parse reads Markdown src; pictures are files relative to dir (one on the web is left as its words: nothing is
// fetched).
func parse(src []byte, dir string) ([]block, error) {
	md := goldmark.New(goldmark.WithExtensions(extension.GFM))
	doc := md.Parser().Parse(text.NewReader(src))
	p := parser{src: src, dir: dir}
	if err := p.blocks(doc, 0, false); err != nil {
		return nil, err
	}
	if len(p.out) == 0 {
		return nil, errEmpty
	}
	return p.out, nil
}

type parser struct {
	src   []byte
	dir   string
	out   []block
	lists int
}

func (p *parser) blocks(n ast.Node, depth int, inItem bool) error {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch c := c.(type) {
		case *ast.Heading:
			p.out = append(p.out, block{kind: bHeading, level: c.Level, runs: p.text(c)})
		case *ast.Paragraph, *ast.TextBlock:
			if img := soleImage(c); img != nil {
				pic, err := p.picture(img)
				if err != nil {
					return err
				}
				if pic != nil {
					p.out = append(p.out, block{kind: bImage, img: pic, level: depth})
					continue
				}
			}
			kind := bPara
			if _, quoted := n.(*ast.Blockquote); quoted {
				kind = bQuote
			}
			b := block{kind: kind, runs: p.text(c)}
			if inItem {
				b.level, b.cont = depth, true
			}
			p.out = append(p.out, b)
		case *ast.List:
			p.lists++
			id := p.lists
			for item := c.FirstChild(); item != nil; item = item.NextSibling() {
				first := len(p.out)
				if err := p.blocks(item, depth+boolInt(inItem), true); err != nil {
					return err
				}
				// The item's first paragraph carries its bullet or number; nested lists keep their own.
				if first < len(p.out) && p.out[first].kind == bPara {
					b := &p.out[first]
					b.kind, b.ordered, b.list, b.start, b.cont = bItem, c.IsOrdered(), id, c.Start, false
					b.level = depth + boolInt(inItem)
				}
			}
		case *ast.FencedCodeBlock, *ast.CodeBlock:
			var lines []string
			for i := 0; i < c.Lines().Len(); i++ {
				seg := c.Lines().At(i)
				lines = append(lines, strings.TrimRight(string(seg.Value(p.src)), "\r\n"))
			}
			p.out = append(p.out, block{kind: bCode, lines: lines})
		case *ast.Blockquote:
			if err := p.blocks(c, depth, inItem); err != nil {
				return err
			}
		case *ast.ThematicBreak:
			p.out = append(p.out, block{kind: bBreak})
		case *east.Table:
			t := &table{align: c.Alignments}
			for row := c.FirstChild(); row != nil; row = row.NextSibling() {
				var cells [][]run
				for cell := row.FirstChild(); cell != nil; cell = cell.NextSibling() {
					cells = append(cells, p.text(cell))
				}
				t.rows = append(t.rows, cells)
			}
			p.out = append(p.out, block{kind: bTable, table: t})
		case *ast.HTMLBlock:
			// Raw HTML has no place in a document.
		default:
			if err := p.blocks(c, depth, inItem); err != nil {
				return err
			}
		}
	}
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// soleImage is the image a paragraph is made of alone, nil otherwise.
func soleImage(n ast.Node) *ast.Image {
	if n.ChildCount() != 1 {
		return nil
	}
	img, _ := n.FirstChild().(*ast.Image)
	return img
}

// picture reads an image's file; nil for one on the web.
func (p *parser) picture(img *ast.Image) (*picture, error) {
	dest := destination(img.Destination)
	if strings.Contains(dest, "://") {
		return nil, nil
	}
	path := dest
	if !filepath.IsAbs(path) {
		path = filepath.Join(p.dir, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("image %s: %w", dest, err)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("image %s: only PNG, JPEG and GIF go in: %w", dest, err)
	}
	return &picture{alt: plain(p.inlines(img, run{})), data: data, ext: format, w: cfg.Width, h: cfg.Height}, nil
}

// text is n's words as runs, those next to each other in the same style as one.
func (p *parser) text(n ast.Node) []run {
	var out []run
	for _, r := range p.inlines(n, run{}) {
		if last := len(out) - 1; last >= 0 && !r.br && !out[last].br && sameStyle(out[last], r) {
			out[last].text += r.text
			continue
		}
		out = append(out, r)
	}
	return out
}

func sameStyle(a, b run) bool {
	a.text, b.text = "", ""
	return a == b
}

// inlines are n's words as runs, each with the style of what it is in.
func (p *parser) inlines(n ast.Node, style run) []run {
	var out []run
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch c := c.(type) {
		case *ast.Text:
			r := style
			if c.IsRaw() {
				r.text = codeLine(c.Value(p.src))
			} else {
				r.text = unescape(c.Value(p.src))
			}
			out = append(out, r)
			if c.HardLineBreak() {
				out = append(out, run{br: true})
			} else if c.SoftLineBreak() {
				r.text = " "
				out = append(out, r)
			}
		case *ast.String:
			r := style
			r.text = string(c.Value)
			if !c.IsRaw() && !c.IsCode() {
				r.text = unescape(c.Value)
			}
			out = append(out, r)
		case *ast.CodeSpan:
			s := style
			s.code = true
			out = append(out, p.inlines(c, s)...)
		case *ast.Emphasis:
			s := style
			if c.Level >= 2 {
				s.bold = true
			} else {
				s.italic = true
			}
			out = append(out, p.inlines(c, s)...)
		case *east.Strikethrough:
			s := style
			s.strike = true
			out = append(out, p.inlines(c, s)...)
		case *ast.Link:
			s := style
			s.link = destination(c.Destination)
			out = append(out, p.inlines(c, s)...)
		case *ast.AutoLink:
			r := style
			r.text, r.link = string(c.Label(p.src)), string(c.URL(p.src))
			out = append(out, r)
		case *ast.Image:
			r := style
			r.text = plain(p.text(c))
			out = append(out, r)
		case *east.TaskCheckBox:
			r := style
			r.text = "☐ "
			if c.IsChecked {
				r.text = "☑ "
			}
			out = append(out, r)
		case *ast.RawHTML:
			// Inline HTML (<br>, <b>…) is left out.
		default:
			out = append(out, p.inlines(c, style)...)
		}
	}
	return out
}

// unescape is Markdown text as the characters it stands for: backslash escapes and entity and numeric references
// resolved (CommonMark 2.4, 2.5), as goldmark's own renderer resolves them. Its writer leaves the result HTML-escaped,
// which html.UnescapeString undoes exactly.
func unescape(b []byte) string {
	var buf bytes.Buffer
	w := bufio.NewWriter(&buf)
	gmhtml.DefaultWriter.Write(w, b)
	_ = w.Flush() // a bytes.Buffer does not fail
	return html.UnescapeString(buf.String())
}

// codeLine is a piece of a code span as written, a line ending in it a space (CommonMark 6.1).
func codeLine(b []byte) string {
	s := string(b)
	if line, ok := strings.CutSuffix(s, "\n"); ok {
		return strings.TrimSuffix(line, "\r") + " "
	}
	return s
}

// destination is a link's or a picture's target with its escapes and references resolved, as goldmark does before it
// percent-encodes one; left unencoded, a relative file path or a non-ASCII URL stays as written.
func destination(b []byte) string {
	return string(util.ResolveEntityNames(util.ResolveNumericReferences(util.UnescapePunctuations(b))))
}

// plain is runs' words alone.
func plain(runs []run) string {
	var b strings.Builder
	for _, r := range runs {
		if r.br {
			b.WriteString(" ")
		}
		b.WriteString(r.text)
	}
	return strings.TrimSpace(b.String())
}
