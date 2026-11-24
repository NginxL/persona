package office

import (
	"encoding/xml"
	"fmt"
	"testing"
)

// rect is a shape's place on the slide in EMU.
type rect struct{ x, y, cx, cy int64 }

func (r rect) overlaps(o rect) bool {
	return r.x < o.x+o.cx && o.x < r.x+r.cx && r.y < o.y+o.cy && o.y < r.y+r.cy
}

func (r rect) String() string { return fmt.Sprintf("%d,%d %dx%d", r.x, r.y, r.cx, r.cy) }

// xfrmXML is a:xfrm or p:xfrm (DrawingML 20.1.7.6): an offset and an extent.
type xfrmXML struct {
	Off struct {
		X int64 `xml:"x,attr"`
		Y int64 `xml:"y,attr"`
	} `xml:"off"`
	Ext struct {
		Cx int64 `xml:"cx,attr"`
		Cy int64 `xml:"cy,attr"`
	} `xml:"ext"`
}

func (x *xfrmXML) rect() rect { return rect{x.Off.X, x.Off.Y, x.Ext.Cx, x.Ext.Cy} }

// slideXML is the shapes of a slide's p:spTree: p:sp (PresentationML 19.3.1.43), p:pic (19.3.1.37) and
// p:graphicFrame (19.3.1.21).
type slideXML struct {
	Shapes []struct {
		Ph *struct {
			Type string `xml:"type,attr"`
			Idx  string `xml:"idx,attr"`
		} `xml:"nvSpPr>nvPr>ph"`
		Xfrm *xfrmXML `xml:"spPr>xfrm"`
	} `xml:"cSld>spTree>sp"`
	Pictures []struct {
		Xfrm *xfrmXML `xml:"spPr>xfrm"`
	} `xml:"cSld>spTree>pic"`
	Frames []struct {
		Xfrm *xfrmXML `xml:"xfrm"`
	} `xml:"cSld>spTree>graphicFrame"`
}

// slideRects are where a content slide's title, words, picture and table are. A placeholder without its own xfrm
// takes the rect of its layout, which here is the master's.
func slideRects(t *testing.T, slideXMLText string) map[string]rect {
	t.Helper()
	var s slideXML
	if err := xml.Unmarshal([]byte(slideXMLText), &s); err != nil {
		t.Fatal(err)
	}
	out := map[string]rect{}
	put := func(name string, r rect) {
		if _, dup := out[name]; dup {
			t.Fatalf("two %s shapes in %s", name, slideXMLText)
		}
		out[name] = r
	}
	for _, sp := range s.Shapes {
		var name string
		var inherited rect
		switch {
		case sp.Ph != nil && sp.Ph.Type == "title":
			name, inherited = "title", rect{838200, 365125, 10515600, 1325563}
		case sp.Ph != nil && sp.Ph.Type == "" && sp.Ph.Idx == "1":
			name, inherited = "words", rect{bodyX, bodyY, bodyW, bodyH}
		default:
			t.Fatalf("unexpected shape %+v in %s", sp, slideXMLText)
		}
		if sp.Xfrm != nil {
			inherited = sp.Xfrm.rect()
		}
		put(name, inherited)
	}
	for _, p := range s.Pictures {
		if p.Xfrm == nil {
			t.Fatalf("picture without xfrm in %s", slideXMLText)
		}
		put("picture", p.Xfrm.rect())
	}
	for _, f := range s.Frames {
		if f.Xfrm == nil {
			t.Fatalf("table without xfrm in %s", slideXMLText)
		}
		put("table", f.Xfrm.rect())
	}
	return out
}

// TestSlideShapesDoNotOverlap puts words, a picture and a table on a slide in every combination: no shape may cover
// another or leave the slide, and the layouts that never overlapped keep their exact places.
func TestSlideShapesDoNotOverlap(t *testing.T) {
	dir := t.TempDir()
	writePNG(t, dir, "photo.png", 800, 600)
	const (
		words   = "- First point\n- Second point\n\n"
		picture = "![A photo](photo.png)\n\n"
		table   = "| Item | Count |\n| --- | --- |\n| One | 1 |\n\n"
	)
	title := rect{838200, 365125, 10515600, 1325563}
	body := rect{bodyX, bodyY, bodyW, bodyH}
	cases := []struct {
		name, md string
		want     map[string]rect
	}{
		// These five never overlapped: their places stay exactly as they were.
		{"words", words, map[string]rect{"title": title, "words": body}},
		{"picture", picture, map[string]rect{"title": title, "picture": {3195108, 1825625, 5801784, 4351338}}},
		{"table", table, map[string]rect{"title": title, "table": {838200, 1825625, 10515600, 741680}}},
		{"words+picture", words + picture, map[string]rect{
			"title": title, "words": {838200, 1825625, 5783580, 4351338}, "picture": {6937248, 2345087, 4416552, 3312414}}},
		{"words+table", words + table, map[string]rect{
			"title": title, "words": {838200, 1825625, 10515600, 1522968}, "table": {838200, 3479133, 10515600, 741680}}},
		// With a picture and a table, the picture takes the right and the table the left column, under any words.
		{"picture+table", picture + table, map[string]rect{
			"title": title, "picture": {6937248, 2345087, 4416552, 3312414}, "table": {838200, 1825625, 5783580, 741680}}},
		{"words+picture+table", words + picture + table, map[string]rect{
			"title": title, "words": {838200, 1825625, 5783580, 1522968}, "picture": {6937248, 2345087, 4416552, 3312414},
			"table": {838200, 3479133, 5783580, 741680}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			data, err := Pptx([]byte("# Slide\n\n"+c.md), dir, "")
			if err != nil {
				t.Fatal(err)
			}
			got := slideRects(t, parts(t, data)["ppt/slides/slide1.xml"])
			names := []string{"title", "words", "picture", "table"}
			for i, a := range names {
				ra, ok := got[a]
				if !ok {
					continue
				}
				if ra.cx <= 0 || ra.cy <= 0 || ra.x < 0 || ra.y < 0 || ra.x+ra.cx > slideW || ra.y+ra.cy > slideH {
					t.Errorf("%s at %v is not inside the %dx%d slide", a, ra, slideW, slideH)
				}
				for _, b := range names[i+1:] {
					if rb, ok := got[b]; ok && ra.overlaps(rb) {
						t.Errorf("%s at %v overlaps %s at %v", a, ra, b, rb)
					}
				}
			}
			if fmt.Sprint(got) != fmt.Sprint(c.want) {
				t.Errorf("shapes\n got %v\nwant %v", got, c.want)
			}
		})
	}
}
