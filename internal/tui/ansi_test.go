package tui

import (
	"bytes"
	"regexp"
	"testing"

	"erin-browser/internal/htmlrender"
)

var ansiColorRe = regexp.MustCompile(`\x1b\[[0-9;]*([349][0-7]|10[0-7])m`)

func TestMonochromeEmitsZeroColorCodes(t *testing.T) {
	styles := []htmlrender.SpanStyle{
		htmlrender.StyleNormal,
		htmlrender.StyleBold,
		htmlrender.StyleUnderline,
		htmlrender.StyleDim,
		htmlrender.StyleHeading,
		htmlrender.StyleLink,
		htmlrender.StyleCode,
		htmlrender.StyleQuote,
		htmlrender.StyleInput,
		htmlrender.StyleButton,
		htmlrender.StyleBadge,
	}
	for _, st := range styles {
		for _, focused := range []bool{false, true} {
			seq := styleSequence(st, focused, true)
			if ansiColorRe.MatchString(seq) {
				t.Fatalf("monochrome mode emitted ANSI color sequence %q for style=%d focused=%v", seq, st, focused)
			}
		}
	}

	var b bytes.Buffer
	writeBar(&b, 1, 80, "Erin Browser", "[HTTPS|MONO]", true, true)
	writeBar(&b, 24, 80, "Status", "50%", true, false)
	writeDocumentLine(&b, 2, 80, htmlrender.Line{
		Spans: []htmlrender.Span{
			{Text: "Hello ", Style: htmlrender.StyleHeading},
			{Text: "Wikipedia[1]", Style: htmlrender.StyleLink, TargetID: 1},
		},
	}, 1, "wiki", true)

	if ansiColorRe.Match(b.Bytes()) {
		t.Fatalf("monochrome rendering emitted ANSI color code: %q", b.String())
	}
}
