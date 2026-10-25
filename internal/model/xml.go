package model

import (
	"bytes"
	"compress/flate"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// BlankXML is the document every new diagram starts from (one page, root + default layer).
const BlankXML = `<mxfile><diagram id="p1" name="Page-1"><mxGraphModel><root><mxCell id="0"/><mxCell id="1" parent="0"/></root></mxGraphModel></diagram></mxfile>`

type xnode struct {
	Name string
	Attr map[string]string
	Kids []*xnode
	Text string
}

// normalizeAttrWS applies XML 1.0 §3.3.3 attribute-value normalisation (literal TAB/CR/LF inside tags become a space;
// character references are untouched) and refuses DOCTYPE/CDATA/comments, which draw.io never writes.
func normalizeAttrWS(in []byte) ([]byte, error) {
	out := make([]byte, 0, len(in))
	inTag := false
	var q byte
	for i := 0; i < len(in); i++ {
		c := in[i]
		switch {
		case q != 0:
			switch c {
			case q:
				q = 0
			case '\n', '\r', '\t':
				c = ' '
			}
		case inTag:
			switch c {
			case '"', '\'':
				q = c
			case '>':
				inTag = false
			}
		default:
			if c == '<' {
				if bytes.HasPrefix(in[i:], []byte("<!")) {
					return nil, errors.New("DOCTYPE, CDATA and comments are not accepted")
				}
				inTag = true
			}
		}
		out = append(out, c)
	}
	return out, nil
}

func parseTree(data []byte) (*xnode, error) {
	data, err := normalizeAttrWS(data)
	if err != nil {
		return nil, err
	}
	d := xml.NewDecoder(bytes.NewReader(data))
	d.Strict = true
	var stack []*xnode
	var root *xnode
	for {
		t, err := d.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		switch e := t.(type) {
		case xml.StartElement:
			n := &xnode{Name: e.Name.Local, Attr: map[string]string{}}
			for _, a := range e.Attr {
				n.Attr[a.Name.Local] = a.Value
			}
			if len(stack) > 0 {
				p := stack[len(stack)-1]
				p.Kids = append(p.Kids, n)
			} else {
				root = n
			}
			stack = append(stack, n)
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].Text += string(e)
			}
		}
	}
	if root == nil {
		return nil, errors.New("empty document")
	}
	return root, nil
}

func inflate(s string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return "", err
	}
	out, err := io.ReadAll(io.LimitReader(flate.NewReader(bytes.NewReader(raw)), 64<<20))
	if err != nil {
		return "", err
	}
	return url.PathUnescape(string(out))
}

func strictNum(s string) (string, error) {
	if s == "" {
		return "0", nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return "", fmt.Errorf("non-canonical number %q", s)
	}
	return Num(f), nil
}

// ImportXML parses draw.io XML (plain or with compressed <diagram> payloads) into a fresh document at seq 0.
func ImportXML(xmlStr string) (*Doc, error) {
	root, err := parseTree([]byte(xmlStr))
	if err != nil {
		return nil, err
	}
	var diagrams []*xnode
	switch root.Name {
	case "mxfile":
		for _, k := range root.Kids {
			if k.Name == "diagram" {
				diagrams = append(diagrams, k)
			}
		}
	case "mxGraphModel":
		diagrams = []*xnode{{Name: "diagram", Attr: map[string]string{"id": "p1", "name": "Page-1"}, Kids: []*xnode{root}}}
	default:
		return nil, fmt.Errorf("unexpected root <%s>", root.Name)
	}
	if len(diagrams) == 0 {
		return nil, errors.New("no diagram pages")
	}
	d := NewDoc()
	keys := Spread(len(diagrams))
	for i, dg := range diagrams {
		model := firstKid(dg, "mxGraphModel")
		if model == nil {
			if t := strings.TrimSpace(dg.Text); t != "" {
				inf, err := inflate(t)
				if err != nil {
					return nil, fmt.Errorf("diagram %q: %w", dg.Attr["id"], err)
				}
				tree, err := parseTree([]byte(inf))
				if err != nil {
					return nil, err
				}
				model = tree
			}
		}
		if model == nil || model.Name != "mxGraphModel" {
			return nil, fmt.Errorf("diagram %q has no mxGraphModel", dg.Attr["id"])
		}
		id := dg.Attr["id"]
		if id == "" {
			id = "p" + strconv.Itoa(i+1)
		}
		if !idRe.MatchString(id) {
			return nil, fmt.Errorf("bad page id %q", id)
		}
		if _, dup := d.Pages[id]; dup {
			return nil, fmt.Errorf("duplicate page id %q", id)
		}
		attrs, _ := json.Marshal(model.Attr) // map keys are sorted by encoding/json
		if len(model.Attr) == 0 {
			attrs = nil
		}
		pg := &Page{ID: id, Name: Reg{V: dg.Attr["name"]}, Order: Reg{V: keys[i]}, Attrs: Reg{V: string(attrs)}, Cells: map[string]*Cell{}, CreatedBy: "import"}
		d.Pages[id] = pg
		if err := importCells(pg, firstKid(model, "root")); err != nil {
			return nil, fmt.Errorf("page %q: %w", id, err)
		}
	}
	return d, nil
}

func firstKid(n *xnode, name string) *xnode {
	for _, k := range n.Kids {
		if k.Name == name {
			return k
		}
	}
	return nil
}

func importCells(pg *Page, root *xnode) error {
	if root == nil {
		return errors.New("no <root>")
	}
	type item struct {
		c      *Cell
		parent string
	}
	var items []item
	for _, k := range root.Kids {
		var mx, wrap *xnode
		switch k.Name {
		case "mxCell":
			mx = k
		case "UserObject", "object":
			wrap, mx = k, firstKid(k, "mxCell")
		default:
			return fmt.Errorf("unrecognised <%s> in root", k.Name)
		}
		if mx == nil {
			return errors.New("object without mxCell")
		}
		id := mx.Attr["id"]
		if wrap != nil {
			id = wrap.Attr["id"]
		}
		if !idRe.MatchString(id) {
			return fmt.Errorf("bad cell id %q", id)
		}
		if _, dup := pg.Cells[id]; dup {
			return fmt.Errorf("duplicate cell id %q", id)
		}
		c := &Cell{ID: id, Kind: "-", Props: map[string]Reg{}, CreatedBy: "import"}
		switch {
		case mx.Attr["vertex"] == "1":
			c.Kind = "v"
		case mx.Attr["edge"] == "1":
			c.Kind = "e"
		}
		set := func(k, v string) { c.Props[k] = Reg{V: v} }
		if wrap != nil {
			set("vt", wrap.Name)
			for k, v := range wrap.Attr {
				if k == "id" {
					continue
				}
				if !attrName.MatchString(k) {
					return fmt.Errorf("unsupported attribute name %q", k)
				}
				set("va:"+k, v)
			}
		} else if v, ok := mx.Attr["value"]; ok {
			set("v", v)
		}
		importStyle(c, mx.Attr["style"])
		if mx.Attr["visible"] == "0" {
			set("fvis", "0")
		}
		if mx.Attr["collapsed"] == "1" {
			set("fcol", "1")
		}
		if mx.Attr["connectable"] == "0" {
			set("fcon", "0")
		}
		if v := mx.Attr["source"]; v != "" {
			set("src", v)
		}
		if v := mx.Attr["target"]; v != "" {
			set("tgt", v)
		}
		if g := firstKid(mx, "mxGeometry"); g != nil {
			if err := importGeometry(c, g); err != nil {
				return fmt.Errorf("cell %q: %w", id, err)
			}
		}
		pg.Cells[id] = c
		items = append(items, item{c, mx.Attr["parent"]})
	}
	// sibling order = document order within each parent, spread over well-spaced keys
	byParent := map[string][]*Cell{}
	for _, it := range items {
		if it.parent == "" {
			if it.c.ID != "0" {
				return fmt.Errorf("cell %q has no parent", it.c.ID)
			}
			continue
		}
		if _, ok := pg.Cells[it.parent]; !ok {
			return fmt.Errorf("cell %q has unknown parent %q", it.c.ID, it.parent)
		}
		it.c.Parent = Reg{V: it.parent}
		byParent[it.parent] = append(byParent[it.parent], it.c)
	}
	for _, list := range byParent {
		ks := Spread(len(list))
		for i, c := range list {
			c.Order = Reg{V: ks[i]}
		}
	}
	if pg.Cells["0"] == nil {
		return errors.New("page has no root cell 0")
	}
	return nil
}

func importStyle(c *Cell, style string) {
	var names []string
	for _, tok := range strings.Split(style, ";") {
		if tok == "" {
			continue
		}
		if i := strings.IndexByte(tok, '='); i > 0 && styleKey.MatchString(tok[:i]) {
			c.Props["st:"+tok[:i]] = Reg{V: tok[i+1:]}
		} else {
			names = append(names, tok)
		}
	}
	if len(names) > 0 {
		c.Props["sn"] = Reg{V: strings.Join(names, ";")}
	}
}

func importGeometry(c *Cell, g *xnode) error {
	set := func(k, v string) { c.Props[k] = Reg{V: v} }
	for _, f := range [][2]string{{"gx", "x"}, {"gy", "y"}, {"gw", "width"}, {"gh", "height"}} {
		v, err := strictNum(g.Attr[f[1]])
		if err != nil {
			return err
		}
		set(f[0], v)
	}
	if g.Attr["relative"] == "1" {
		set("grel", "1")
	}
	ex := map[string]any{}
	pt := func(n *xnode) ([]json.RawMessage, error) {
		x, err := strictNum(n.Attr["x"])
		if err != nil {
			return nil, err
		}
		y, err := strictNum(n.Attr["y"])
		if err != nil {
			return nil, err
		}
		return []json.RawMessage{json.RawMessage(x), json.RawMessage(y)}, nil
	}
	for _, k := range g.Kids {
		switch k.Attr["as"] {
		case "points":
			var pts [][]json.RawMessage
			for _, p := range k.Kids {
				v, err := pt(p)
				if err != nil {
					return err
				}
				pts = append(pts, v)
			}
			ex["p"] = pts
		case "sourcePoint", "targetPoint", "offset":
			v, err := pt(k)
			if err != nil {
				return err
			}
			ex[map[string]string{"sourcePoint": "sp", "targetPoint": "tp", "offset": "off"}[k.Attr["as"]]] = v
		case "alternateBounds":
			var a []json.RawMessage
			for _, f := range []string{"x", "y", "width", "height"} {
				v, err := strictNum(k.Attr[f])
				if err != nil {
					return err
				}
				a = append(a, json.RawMessage(v))
			}
			ex["alt"] = a
		default:
			return fmt.Errorf("unsupported geometry child <%s as=%q>", k.Name, k.Attr["as"])
		}
	}
	if len(ex) > 0 {
		b, _ := json.Marshal(ex) // sorted keys: alt, off, p, sp, tp (web/model.js builds the same order)
		set("gex", string(b))
	}
	return nil
}

// ---- export -----------------------------------------------------------------------------------------------

var xmlAttrRepl = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;", "\n", "&#xa;", "\r", "&#xd;", "\t", "&#x9;")

func xa(s string) string { return xmlAttrRepl.Replace(s) }

func (c *Cell) styleString() string {
	var parts []string
	if sn, ok := c.Prop("sn"); ok && sn != "" {
		parts = append(parts, sn)
	}
	var keys []string
	for k, r := range c.Props {
		if strings.HasPrefix(k, "st:") && !r.Del {
			keys = append(keys, k[3:])
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		parts = append(parts, k+"="+c.Props["st:"+k].V)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, ";") + ";"
}

// ExportXML writes the document as uncompressed draw.io XML (any draw.io opens it).
func (d *Doc) ExportXML() string {
	var b strings.Builder
	b.WriteString("<mxfile>")
	for _, p := range d.SortedPages() {
		b.WriteString(`<diagram id="` + xa(p.ID) + `" name="` + xa(p.Name.V) + `"><mxGraphModel`)
		if p.Attrs.V != "" {
			var m map[string]string
			if json.Unmarshal([]byte(p.Attrs.V), &m) == nil {
				ks := make([]string, 0, len(m))
				for k := range m {
					ks = append(ks, k)
				}
				sort.Strings(ks)
				for _, k := range ks {
					if attrName.MatchString(k) {
						b.WriteString(" " + k + `="` + xa(m[k]) + `"`)
					}
				}
			}
		}
		b.WriteString("><root>")
		idx := p.childIndex()
		var walk func(c *Cell, parent string)
		walk = func(c *Cell, parent string) {
			writeCell(&b, c, parent)
			for _, k := range idx[c.ID] {
				walk(k, c.ID)
			}
		}
		if r := p.Cells["0"]; r != nil && r.Alive() {
			walk(r, "")
		}
		b.WriteString("</root></mxGraphModel></diagram>")
	}
	b.WriteString("</mxfile>")
	return b.String()
}

func writeCell(b *strings.Builder, c *Cell, parent string) {
	vt, wrapped := c.Prop("vt")
	if wrapped {
		b.WriteString("<" + vt)
		var ks []string
		for k, r := range c.Props {
			if strings.HasPrefix(k, "va:") && !r.Del {
				ks = append(ks, k[3:])
			}
		}
		sort.Strings(ks)
		for _, k := range ks {
			b.WriteString(" " + k + `="` + xa(c.Props["va:"+k].V) + `"`)
		}
		b.WriteString(` id="` + xa(c.ID) + `">`)
	}
	b.WriteString("<mxCell")
	if !wrapped {
		b.WriteString(` id="` + xa(c.ID) + `"`)
		if v, ok := c.Prop("v"); ok {
			b.WriteString(` value="` + xa(v) + `"`)
		}
	}
	if s := c.styleString(); s != "" {
		b.WriteString(` style="` + xa(s) + `"`)
	}
	switch c.Kind {
	case "v":
		b.WriteString(` vertex="1"`)
	case "e":
		b.WriteString(` edge="1"`)
	}
	if v, ok := c.Prop("fvis"); ok && v == "0" {
		b.WriteString(` visible="0"`)
	}
	if v, ok := c.Prop("fcol"); ok && v == "1" {
		b.WriteString(` collapsed="1"`)
	}
	if v, ok := c.Prop("fcon"); ok && v == "0" {
		b.WriteString(` connectable="0"`)
	}
	if parent != "" {
		b.WriteString(` parent="` + xa(parent) + `"`)
	}
	if v, ok := c.Prop("src"); ok {
		b.WriteString(` source="` + xa(v) + `"`)
	}
	if v, ok := c.Prop("tgt"); ok {
		b.WriteString(` target="` + xa(v) + `"`)
	}
	if gw, ok := c.Prop("gw"); ok {
		b.WriteString("><mxGeometry")
		for _, f := range [][2]string{{"gx", "x"}, {"gy", "y"}} {
			if v, _ := c.Prop(f[0]); v != "" && v != "0" {
				b.WriteString(" " + f[1] + `="` + v + `"`)
			}
		}
		gh, _ := c.Prop("gh")
		if gw != "0" {
			b.WriteString(` width="` + gw + `"`)
		}
		if gh != "0" && gh != "" {
			b.WriteString(` height="` + gh + `"`)
		}
		if v, _ := c.Prop("grel"); v == "1" {
			b.WriteString(` relative="1"`)
		}
		b.WriteString(` as="geometry"`)
		if raw, ok := c.Prop("gex"); ok {
			b.WriteString(">")
			writeExtras(b, raw)
			b.WriteString("</mxGeometry></mxCell>")
		} else {
			b.WriteString("/></mxCell>")
		}
	} else {
		b.WriteString("/>")
	}
	if wrapped {
		b.WriteString("</" + vt + ">")
	}
}

func writeExtras(b *strings.Builder, raw string) {
	var ex map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &ex) != nil {
		return
	}
	pt := func(name string, m json.RawMessage) {
		var v []json.Number
		if json.Unmarshal(m, &v) == nil && len(v) == 2 {
			b.WriteString(`<mxPoint x="` + v[0].String() + `" y="` + v[1].String() + `" as="` + name + `"/>`)
		}
	}
	if m, ok := ex["sp"]; ok {
		pt("sourcePoint", m)
	}
	if m, ok := ex["tp"]; ok {
		pt("targetPoint", m)
	}
	if m, ok := ex["p"]; ok {
		var pts [][]json.Number
		if json.Unmarshal(m, &pts) == nil {
			b.WriteString(`<Array as="points">`)
			for _, p := range pts {
				if len(p) == 2 {
					b.WriteString(`<mxPoint x="` + p[0].String() + `" y="` + p[1].String() + `"/>`)
				}
			}
			b.WriteString("</Array>")
		}
	}
	if m, ok := ex["off"]; ok {
		pt("offset", m)
	}
	if m, ok := ex["alt"]; ok {
		var v []json.Number
		if json.Unmarshal(m, &v) == nil && len(v) == 4 {
			b.WriteString(`<mxRectangle x="` + v[0].String() + `" y="` + v[1].String() + `" width="` + v[2].String() + `" height="` + v[3].String() + `" as="alternateBounds"/>`)
		}
	}
}
