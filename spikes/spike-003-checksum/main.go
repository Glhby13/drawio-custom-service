// DISPOSABLE SPIKE — NOT PRODUCTION CODE (SPIKE-002 §checksum).
// Canonical document checksum, Go side. The same canonical form is produced in the browser from the LIVE draw.io
// model (canon.js). Go only ever sees XML (snapshots / ingest), so it parses the draw.io XML into the same
// abstract model and then canonicalises. See canon.js header for the CANON-v0 specification.
//
//	canon xml    < [{"name":..,"xml":..}]   -> [{"name":..,"hash":..,"canon":..,"error":..}]
//	canon nums   < [floats as JSON]          -> [canonical number strings]
package main

import (
	"bytes"
	"compress/flate"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
)

// ---- abstract model ------------------------------------------------------------------------------------------

type pt struct{ X, Y float64 }

type geom struct {
	X, Y, W, H   float64
	Relative     bool
	Points       []pt
	SourcePoint  *pt
	TargetPoint  *pt
	Offset       *pt
	Alternate    *[4]float64
	Unrecognised []string
}

type cell struct {
	ID, Parent  string
	Vertex      bool
	Edge        bool
	Visible     bool
	Collapsed   bool
	Connectable bool
	Source      string
	Target      string
	// value: kind 'N' (null) 'S' (string) 'X' (xml node)
	ValKind byte
	ValStr  string
	ValTag  string
	ValAttr map[string]string
	Style   string
	Geo     *geom
	Kids    []*cell
}

type page struct {
	ID, Name string
	Root     *cell
}

// ---- number / string canonicalisation (must match canon.js) -----------------------------------------------------

func num(v float64) string {
	if v == 0 {
		return "0" // also folds -0
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func esc(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `|`, `\|`, "\n", `\n`, "\r", `\r`)
	return r.Replace(s)
}

func canonStyle(s string) string {
	var names []string
	kv := map[string]string{}
	for _, tok := range strings.Split(s, ";") {
		if tok == "" {
			continue
		}
		if i := strings.IndexByte(tok, '='); i >= 0 {
			kv[tok[:i]] = tok[i+1:]
		} else {
			names = append(names, tok)
		}
	}
	keys := make([]string, 0, len(kv))
	for k := range kv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := append([]string{}, names...)
	for _, k := range keys {
		parts = append(parts, k+"="+kv[k])
	}
	return strings.Join(parts, ";")
}

func canonPt(p *pt) string {
	if p == nil {
		return "-"
	}
	return num(p.X) + "," + num(p.Y)
}

func canonGeo(g *geom) string {
	if g == nil {
		return "-"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "x=%s;y=%s;w=%s;h=%s;rel=%d", num(g.X), num(g.Y), num(g.W), num(g.H), b2i(g.Relative))
	pts := make([]string, len(g.Points))
	for i, p := range g.Points {
		pts[i] = canonPt(&p)
	}
	fmt.Fprintf(&b, ";pts=[%s];sp=%s;tp=%s;off=%s", strings.Join(pts, " "), canonPt(g.SourcePoint), canonPt(g.TargetPoint), canonPt(g.Offset))
	if g.Alternate != nil {
		a := g.Alternate
		fmt.Fprintf(&b, ";alt=%s,%s,%s,%s", num(a[0]), num(a[1]), num(a[2]), num(a[3]))
	}
	if len(g.Unrecognised) > 0 {
		sort.Strings(g.Unrecognised)
		fmt.Fprintf(&b, ";UNRECOGNISED=%s", strings.Join(g.Unrecognised, ","))
	}
	return b.String()
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func canonValue(c *cell) string {
	switch c.ValKind {
	case 'S':
		return "S:" + esc(c.ValStr)
	case 'X':
		keys := make([]string, 0, len(c.ValAttr))
		for k := range c.ValAttr {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, esc(k)+"="+esc(c.ValAttr[k]))
		}
		return "X:" + esc(c.ValTag) + "{" + strings.Join(parts, ",") + "}"
	}
	return "N"
}

func canonCell(b *strings.Builder, c *cell) {
	kind := "-"
	if c.Vertex {
		kind = "v"
	} else if c.Edge {
		kind = "e"
	}
	fmt.Fprintf(b, "C|%s|%s|%s|%d%d%d|%s|%s|%s|%s|%s\n", esc(c.ID), esc(c.Parent), kind,
		b2i(c.Visible), b2i(c.Collapsed), b2i(c.Connectable), esc(c.Source), esc(c.Target),
		canonValue(c), esc(canonStyle(c.Style)), esc(canonGeo(c.Geo)))
	for _, k := range c.Kids {
		canonCell(b, k)
	}
}

func canonDoc(pages []page) (string, string) {
	var b strings.Builder
	for _, p := range pages {
		fmt.Fprintf(&b, "P|%s|%s\n", esc(p.ID), esc(p.Name))
		if p.Root != nil {
			canonCell(&b, p.Root)
		}
	}
	s := b.String()
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:]), s
}

// ---- draw.io XML -> abstract model ---------------------------------------------------------------------------------

type node struct {
	Name string
	Attr map[string]string
	Kids []*node
}

// normalizeAttrWS applies XML 1.0 §3.3.3 attribute-value normalisation to LITERAL tab/CR/LF inside tags
// (character references such as &#xa; are untouched). encoding/xml does not do this, browsers' DOMParser does.
// Also refuses constructs a platform parser must not accept anyway (DOCTYPE, CDATA, comments, PI).
func normalizeAttrWS(in []byte) ([]byte, error) {
	out := make([]byte, 0, len(in))
	inTag := false
	var q byte
	for i := 0; i < len(in); i++ {
		c := in[i]
		switch {
		case q != 0:
			if c == q {
				q = 0
			} else if c == '\n' || c == '\r' || c == '\t' {
				c = ' '
			}
		case inTag:
			if c == '"' || c == '\'' {
				q = c
			} else if c == '>' {
				inTag = false
			}
		default:
			if c == '<' {
				if bytes.HasPrefix(in[i:], []byte("<!")) || bytes.HasPrefix(in[i:], []byte("<?xml")) && false {
					return nil, fmt.Errorf("DOCTYPE/CDATA/comment not accepted")
				}
				inTag = true
			}
		}
		out = append(out, c)
	}
	return out, nil
}

func parseTree(data []byte) (*node, error) {
	data, err := normalizeAttrWS(data)
	if err != nil {
		return nil, err
	}
	d := xml.NewDecoder(bytes.NewReader(data))
	d.Strict = true
	var stack []*node
	var root *node
	for {
		t, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch e := t.(type) {
		case xml.StartElement:
			n := &node{Name: e.Name.Local, Attr: map[string]string{}}
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
		}
	}
	return root, nil
}

// badNums collects numeric attributes that are not plain decimals (JS parseFloat("5px") == 5, Go would fail):
// strict by design - a server must reject such input instead of guessing.
var badNums []string

func fl(s string) float64 {
	if s == "" {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		badNums = append(badNums, s)
		return 0
	}
	return f
}

func ptOf(n *node) *pt { return &pt{fl(n.Attr["x"]), fl(n.Attr["y"])} }

func geoOf(n *node) *geom {
	g := &geom{X: fl(n.Attr["x"]), Y: fl(n.Attr["y"]), W: fl(n.Attr["width"]), H: fl(n.Attr["height"]), Relative: n.Attr["relative"] == "1"}
	for _, k := range n.Kids {
		switch k.Attr["as"] {
		case "points":
			for _, p := range k.Kids {
				g.Points = append(g.Points, *ptOf(p))
			}
		case "sourcePoint":
			g.SourcePoint = ptOf(k)
		case "targetPoint":
			g.TargetPoint = ptOf(k)
		case "offset":
			g.Offset = ptOf(k)
		case "alternateBounds":
			g.Alternate = &[4]float64{fl(k.Attr["x"]), fl(k.Attr["y"]), fl(k.Attr["width"]), fl(k.Attr["height"])}
		default:
			g.Unrecognised = append(g.Unrecognised, k.Name+"/"+k.Attr["as"])
		}
	}
	return g
}

// cellFrom builds a cell from an <mxCell> node; wrapper is the <UserObject>/<object> parent, if any.
func cellFrom(n *node, wrapper *node) *cell {
	c := &cell{Visible: n.Attr["visible"] != "0", Collapsed: n.Attr["collapsed"] == "1", Connectable: n.Attr["connectable"] != "0",
		Vertex: n.Attr["vertex"] == "1", Edge: n.Attr["edge"] == "1", Parent: n.Attr["parent"], Source: n.Attr["source"], Target: n.Attr["target"],
		Style: n.Attr["style"]}
	if wrapper != nil {
		c.ID = wrapper.Attr["id"]
		c.ValKind, c.ValTag = 'X', wrapper.Name
		c.ValAttr = map[string]string{}
		for k, v := range wrapper.Attr {
			if k != "id" {
				c.ValAttr[k] = v
			}
		}
	} else {
		c.ID = n.Attr["id"]
		if v, ok := n.Attr["value"]; ok {
			c.ValKind, c.ValStr = 'S', v
		} else {
			c.ValKind = 'N'
		}
	}
	for _, k := range n.Kids {
		if k.Name == "mxGeometry" {
			c.Geo = geoOf(k)
		}
	}
	return c
}

func buildPage(model *node, id, name string) (page, error) {
	var rootNode *node
	for _, k := range model.Kids {
		if k.Name == "root" {
			rootNode = k
		}
	}
	p := page{ID: id, Name: name}
	if rootNode == nil {
		return p, nil
	}
	byID := map[string]*cell{}
	var order []*cell
	for _, k := range rootNode.Kids {
		var c *cell
		switch k.Name {
		case "mxCell":
			c = cellFrom(k, nil)
		case "UserObject", "object":
			for _, kk := range k.Kids {
				if kk.Name == "mxCell" {
					c = cellFrom(kk, k)
				}
			}
		}
		if c == nil {
			return p, fmt.Errorf("unrecognised root child <%s>", k.Name)
		}
		byID[c.ID] = c
		order = append(order, c)
	}
	// Children order = document order of cells sharing a parent (draw.io serialises in sibling order).
	for _, c := range order {
		if c.Parent == "" {
			p.Root = c
			continue
		}
		par, ok := byID[c.Parent]
		if !ok {
			return p, fmt.Errorf("cell %q has unknown parent %q", c.ID, c.Parent)
		}
		par.Kids = append(par.Kids, c)
	}
	return p, nil
}

func inflateDiagram(s string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return "", err
	}
	r := flate.NewReader(bytes.NewReader(raw))
	out, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	return url.PathUnescape(string(out))
}

func parseDoc(xmlStr string) ([]page, error) {
	root, err := parseTree([]byte(xmlStr))
	if err != nil {
		return nil, err
	}
	if root == nil {
		return nil, fmt.Errorf("empty document")
	}
	var diagrams []*node
	switch root.Name {
	case "mxfile":
		for _, k := range root.Kids {
			if k.Name == "diagram" {
				diagrams = append(diagrams, k)
			}
		}
	case "mxGraphModel":
		diagrams = []*node{{Name: "diagram", Attr: map[string]string{"id": "", "name": ""}, Kids: []*node{root}}}
	default:
		return nil, fmt.Errorf("unexpected root <%s>", root.Name)
	}
	badNums = nil
	var pages []page
	defer func() {}()
	for _, d := range diagrams {
		var model *node
		for _, k := range d.Kids {
			if k.Name == "mxGraphModel" {
				model = k
			}
		}
		if model == nil {
			// compressed payload is character data; encoding/xml tree builder drops it, so re-read via CharData
			return nil, fmt.Errorf("diagram %q has no inline mxGraphModel (compressed payload must be inflated by caller)", d.Attr["id"])
		}
		p, err := buildPage(model, d.Attr["id"], d.Attr["name"])
		if err != nil {
			return nil, err
		}
		pages = append(pages, p)
	}
	if len(badNums) > 0 {
		return nil, fmt.Errorf("non-canonical numeric attribute(s): %q", badNums)
	}
	return pages, nil
}

// decompressDiagrams inflates <diagram>base64</diagram> payloads in-place (text rewriting, spike-level).
func decompressDiagrams(x string) string {
	// Minimal: find every <diagram ...>TEXT</diagram> whose TEXT does not start with '<'.
	var out strings.Builder
	rest := x
	for {
		i := strings.Index(rest, "<diagram")
		if i < 0 {
			out.WriteString(rest)
			break
		}
		j := strings.Index(rest[i:], ">")
		if j < 0 {
			out.WriteString(rest)
			break
		}
		open := rest[:i+j+1]
		body := rest[i+j+1:]
		if strings.HasSuffix(open, "/>") {
			out.WriteString(open)
			rest = body
			continue
		}
		k := strings.Index(body, "</diagram>")
		if k < 0 {
			out.WriteString(rest)
			break
		}
		payload := body[:k]
		if t := strings.TrimSpace(payload); t != "" && !strings.HasPrefix(t, "<") {
			if inf, err := inflateDiagram(t); err == nil {
				payload = inf
			}
		}
		out.WriteString(open)
		out.WriteString(payload)
		out.WriteString("</diagram>")
		rest = body[k+len("</diagram>"):]
	}
	return out.String()
}

type item struct {
	Name string `json:"name"`
	XML  string `json:"xml"`
}

type result struct {
	Name  string `json:"name"`
	Hash  string `json:"hash,omitempty"`
	Canon string `json:"canon,omitempty"`
	Error string `json:"error,omitempty"`
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: canon xml|nums < input.json")
		os.Exit(2)
	}
	in, _ := io.ReadAll(os.Stdin)
	switch os.Args[1] {
	case "xml":
		var items []item
		if err := json.Unmarshal(in, &items); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		out := make([]result, 0, len(items))
		for _, it := range items {
			pages, err := parseDoc(decompressDiagrams(it.XML))
			if err != nil {
				out = append(out, result{Name: it.Name, Error: err.Error()})
				continue
			}
			h, c := canonDoc(pages)
			out = append(out, result{Name: it.Name, Hash: h, Canon: c})
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(out)
	case "nums":
		var nums []float64
		if err := json.Unmarshal(in, &nums); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		out := make([]string, len(nums))
		for i, v := range nums {
			out[i] = num(v)
		}
		_ = json.NewEncoder(os.Stdout).Encode(out)
	default:
		os.Exit(2)
	}
}
