package model

import (
	"math"
	"strings"
	"unicode/utf8"
)

// Text measurement is an estimate (no font metrics on the server): a Helvetica/Arial-like average advance of ~0.56 em, wider for
// bold, and a full em for East-Asian wide characters. It is deliberately a little pessimistic so a label that "fits" here fits in
// the editor too. Both the diagram builder (sizing) and the linter (overflow) use the same numbers so they agree with each other.

func runeWidth(r rune, fs float64, bold bool) float64 {
	k := 0.56
	switch {
	case r >= 0x2E80: // CJK and wide forms
		k = 1.0
	case r == ' ' || r == 'i' || r == 'l' || r == 'j' || r == 't' || r == 'f' || r == '.' || r == ',' || r == ':' || r == ';' || r == '|' || r == '!' || r == '\'':
		k = 0.32
	case r >= 'A' && r <= 'Z' || r == 'm' || r == 'w' || r == 'M' || r == 'W':
		k = 0.68
	}
	if bold {
		k *= 1.08
	}
	return k * fs
}

// TextWidth is the estimated width of one line.
func TextWidth(s string, fs float64, bold bool) float64 {
	w := 0.0
	for _, r := range s {
		w += runeWidth(r, fs, bold)
	}
	return w
}

// WrapText breaks text into lines no wider than maxW (greedy, on spaces; an over-long word stays whole). Existing newlines are kept.
func WrapText(s string, maxW, fs float64, bold bool) []string {
	var out []string
	for _, para := range strings.Split(strings.ReplaceAll(s, "\r", ""), "\n") {
		words := strings.Fields(para)
		if len(words) == 0 {
			out = append(out, "")
			continue
		}
		cur := words[0]
		for _, w := range words[1:] {
			if TextWidth(cur+" "+w, fs, bold) <= maxW {
				cur += " " + w
			} else {
				out = append(out, cur)
				cur = w
			}
		}
		out = append(out, cur)
	}
	return out
}

// TextBlock is the estimated size of a label wrapped to maxW.
func TextBlock(s string, maxW, fs float64, bold bool) (w, h float64, lines int) {
	ls := WrapText(s, maxW, fs, bold)
	for _, l := range ls {
		if lw := TextWidth(l, fs, bold); lw > w {
			w = lw
		}
	}
	return w, float64(len(ls)) * fs * 1.3, len(ls)
}

// ceil10 rounds a size up to the 10 px grid draw.io snaps to, so shapes align.
func ceil10(f float64) float64 { return math.Ceil(f/10) * 10 }

// labelText turns a stored label (HTML when the shape has html=1) into plain text for measuring.
func labelText(v string) string {
	v = strings.ReplaceAll(v, "<br>", "\n")
	v = strings.ReplaceAll(v, "<br/>", "\n")
	v = tagRe.ReplaceAllString(v, "")
	v = strings.NewReplacer("&lt;", "<", "&gt;", ">", "&amp;", "&", "&quot;", `"`, "&#39;", "'", "&nbsp;", " ").Replace(v)
	return v
}

// htmlLabel makes a caller-supplied label safe for a shape with html=1: markup is shown literally, never interpreted.
func htmlLabel(s string) string {
	s = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(strings.ReplaceAll(s, "\r", ""))
	return strings.ReplaceAll(s, "\n", "<br>")
}

func runeLen(s string) int { return utf8.RuneCountInString(s) }

// HTMLLabel makes a caller-supplied label safe for a shape with html=1 (markup is shown literally, never interpreted).
func HTMLLabel(s string) string { return htmlLabel(s) }
