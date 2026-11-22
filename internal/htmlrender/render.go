package htmlrender

import (
	"fmt"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"
)

// SpanStyle is a bitmask describing semantic/visual styling for a text span.
type SpanStyle uint16

const (
	StyleNormal SpanStyle = 0
	StyleBold   SpanStyle = 1 << iota
	StyleUnderline
	StyleDim
	StyleHeading
	StyleLink
	StyleCode
	StyleQuote
	StyleInput
	StyleButton
	StyleBadge
)

// Span is a styled run of text on a single rendered line.
// TargetID > 0 associates this span with Document.Targets[TargetID-1].
type Span struct {
	Text     string
	Style    SpanStyle
	TargetID int
}

// Line is a single wrapped display row.
type Line struct {
	Spans []Span
}

// PlainText returns the unstyled text of the line.
func (l Line) PlainText() string {
	var b strings.Builder
	for _, s := range l.Spans {
		b.WriteString(s.Text)
	}
	return b.String()
}

// TargetKind identifies interactive elements on a page.
type TargetKind int

const (
	TargetLink TargetKind = iota
	TargetInputText
	TargetInputPassword
	TargetInputCheckbox
	TargetInputRadio
	TargetSelect
	TargetSubmit
)

// SelectOption represents an <option> inside a <select>.
type SelectOption struct {
	Value string
	Label string
}

// Target represents a numbered interactive element (link or form control).
type Target struct {
	ID          int
	Kind        TargetKind
	Label       string
	URL         string
	FormID      int
	FieldName   string
	Value       string
	Checked     bool
	Options     []SelectOption
	SelectedIdx int
	LineIdx     int
}

// FormField represents a name/value pair (used for hidden fields).
type FormField struct {
	Name  string
	Value string
}

// Form represents an HTML <form> element.
type Form struct {
	ID           int
	Action       string
	Method       string
	HiddenFields []FormField
}

// Document is the fully laid-out representation of a web page for a given terminal width.
type Document struct {
	Title          string
	URL            string
	Lines          []Line
	Targets        []Target
	Forms          []Form
	ReaderMode     bool
	ReaderFallback bool
}

// TargetByID returns a pointer to the 1-based target, or nil.
func (d *Document) TargetByID(id int) *Target {
	if id < 1 || id > len(d.Targets) {
		return nil
	}
	return &d.Targets[id-1]
}

// FormByID returns a pointer to the 1-based form, or nil.
func (d *Document) FormByID(id int) *Form {
	if id < 1 || id > len(d.Forms) {
		return nil
	}
	return &d.Forms[id-1]
}

// FormVisibleInputs counts visible text/password inputs in the given form.
func (d *Document) FormVisibleInputs(formID int) int {
	count := 0
	for _, t := range d.Targets {
		if t.FormID == formID && (t.Kind == TargetInputText || t.Kind == TargetInputPassword) {
			count++
		}
	}
	return count
}

// BuildFormValues collects all hidden and interactive values for a form submission.
func (d *Document) BuildFormValues(formID int, submitTarget *Target) url.Values {
	vals := url.Values{}
	f := d.FormByID(formID)
	if f == nil {
		return vals
	}
	for _, hf := range f.HiddenFields {
		if hf.Name != "" {
			vals.Add(hf.Name, hf.Value)
		}
	}
	for _, t := range d.Targets {
		if t.FormID != formID || t.FieldName == "" {
			continue
		}
		switch t.Kind {
		case TargetInputText, TargetInputPassword:
			vals.Set(t.FieldName, t.Value)
		case TargetInputCheckbox, TargetInputRadio:
			if t.Checked {
				v := t.Value
				if v == "" {
					v = "on"
				}
				vals.Add(t.FieldName, v)
			}
		case TargetSelect:
			if t.SelectedIdx >= 0 && t.SelectedIdx < len(t.Options) {
				vals.Set(t.FieldName, t.Options[t.SelectedIdx].Value)
			}
		}
	}
	if submitTarget != nil && submitTarget.FieldName != "" {
		vals.Set(submitTarget.FieldName, submitTarget.Value)
	}
	return vals
}

// RefreshTargetDisplay updates the rendered text of an interactive form control in-place.
func (d *Document) RefreshTargetDisplay(targetID int) {
	t := d.TargetByID(targetID)
	if t == nil {
		return
	}
	newText := formatFormControlText(t)
	if newText == "" {
		return
	}
	if t.LineIdx >= 0 && t.LineIdx < len(d.Lines) {
		for i := range d.Lines[t.LineIdx].Spans {
			if d.Lines[t.LineIdx].Spans[i].TargetID == targetID {
				d.Lines[t.LineIdx].Spans[i].Text = newText
				return
			}
		}
	}
}

// RenderPlainText lays out plain text or JSON cleanly wrapped to width.
func RenderPlainText(text, title, rawURL string, width int) *Document {
	if width < 20 {
		width = 20
	}
	if title == "" {
		title = rawURL
	}
	doc := &Document{
		Title: title,
		URL:   rawURL,
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	for _, rawLine := range strings.Split(text, "\n") {
		wrapped := wrapPlainLine(rawLine, width)
		for _, w := range wrapped {
			doc.Lines = append(doc.Lines, Line{
				Spans: []Span{{Text: w, Style: StyleNormal}},
			})
		}
	}
	if len(doc.Lines) == 0 {
		doc.Lines = append(doc.Lines, Line{Spans: []Span{{Text: "(empty document)", Style: StyleDim}}})
	}
	return doc
}

// RenderHTML parses HTML5 and lays it out for a terminal of the given column width.
func RenderHTML(rawHTML, baseURL string, width int, readerMode bool) *Document {
	if width < 24 {
		width = 24
	}
	root, err := html.ParseWithOptions(strings.NewReader(rawHTML), html.ParseOptionEnableScripting(false))
	if err != nil {
		return RenderPlainText(rawHTML, baseURL, baseURL, width)
	}

	title := strings.TrimSpace(collapseSpaces(findTitle(root)))
	effectiveBase := baseURL
	if bHref := findBaseHref(root); bHref != "" {
		effectiveBase = resolveURL(baseURL, bHref)
	}
	parsedBase, _ := url.Parse(effectiveBase)

	body := findTag(root, "body")
	if body == nil {
		body = root
	}

	renderRoot := body
	readerFallback := false
	if readerMode {
		candidate := findReaderRoot(body)
		if candidate != nil && visibleTextLen(candidate) >= 120 {
			renderRoot = candidate
		} else {
			readerFallback = true
		}
	}

	doc := &Document{
		Title:          title,
		URL:            baseURL,
		ReaderMode:     readerMode,
		ReaderFallback: readerFallback,
	}
	if doc.Title == "" {
		doc.Title = baseURL
	}

	r := &renderer{
		doc:        doc,
		baseURL:    parsedBase,
		width:      width,
		readerMode: readerMode && !readerFallback,
	}

	if title != "" && r.readerMode {
		r.flushLine()
		r.addWrappedBlock(title, StyleBold|StyleHeading, "")
		r.addRule('=')
		r.blankLine()
	}

	r.walk(renderRoot, StyleNormal, 0, 0)
	r.flushLine()
	r.trimTrailingBlanks()

	if len(doc.Lines) == 0 {
		doc.Lines = append(doc.Lines, Line{
			Spans: []Span{{Text: "(no readable text content on this page)", Style: StyleDim}},
		})
	}

	return doc
}

// DumpPlain converts a Document to plain text for CLI --dump mode and tests.
func DumpPlain(doc *Document) string {
	var b strings.Builder
	if doc.Title != "" {
		b.WriteString("=== ")
		b.WriteString(doc.Title)
		b.WriteString(" ===\n")
	}
	if doc.URL != "" {
		b.WriteString(doc.URL)
		b.WriteString("\n\n")
	}
	for _, line := range doc.Lines {
		b.WriteString(line.PlainText())
		b.WriteByte('\n')
	}
	if len(doc.Targets) > 0 {
		b.WriteString("\n--- Links & Controls ---\n")
		for _, t := range doc.Targets {
			if t.Kind == TargetLink {
				fmt.Fprintf(&b, "[%d] %s -> %s\n", t.ID, t.Label, t.URL)
			} else {
				fmt.Fprintf(&b, "[%d] (form #%d field %q) %s\n", t.ID, t.FormID, t.FieldName, t.Label)
			}
		}
	}
	return b.String()
}

type renderer struct {
	doc        *Document
	baseURL    *url.URL
	width      int
	readerMode bool

	curSpans  []Span
	curCol    int
	indent    string
	quoteMask SpanStyle
	inPre     bool
	listDepth int
	listStack []listState
	needSpace bool
}

type listState struct {
	ordered bool
	index   int
}

func (r *renderer) walk(n *html.Node, style SpanStyle, activeFormID int, activeLinkID int) {
	if n == nil {
		return
	}

	switch n.Type {
	case html.TextNode:
		if r.inPre {
			r.writePreText(n.Data, style|StyleCode, activeLinkID)
			return
		}
		r.writeInlineText(n.Data, style, activeLinkID)
		return

	case html.DocumentNode:
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			r.walk(c, style, activeFormID, activeLinkID)
		}
		return

	case html.ElementNode:
		tag := strings.ToLower(n.Data)
		if shouldSkipNode(n, tag, r.readerMode) {
			return
		}

		switch tag {
		case "head", "script", "style", "svg", "canvas", "template", "iframe", "object", "embed", "audio", "video", "map":
			return

		case "br":
			r.flushLine()
			r.needSpace = false
			return

		case "hr":
			r.flushLine()
			r.addRule('-')
			return

		case "h1", "h2", "h3", "h4", "h5", "h6":
			r.blankLine()
			hStyle := style | StyleBold | StyleHeading
			prefix := ""
			switch tag {
			case "h1":
				prefix = ""
			case "h2":
				prefix = "## "
			case "h3":
				prefix = "### "
			default:
				prefix = "* "
			}
			if prefix != "" {
				r.appendRawSpan(prefix, hStyle, 0)
			}
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				r.walk(c, hStyle, activeFormID, activeLinkID)
			}
			r.flushLine()
			if tag == "h1" {
				r.addRule('=')
			} else if tag == "h2" {
				r.addRule('-')
			}
			r.blankLine()
			return

		case "p", "div", "section", "article", "main", "header", "footer", "nav", "aside", "figure", "figcaption", "address", "details", "summary":
			isPara := tag == "p" || tag == "article" || tag == "section"
			if isPara {
				r.blankLine()
			} else {
				r.flushLine()
			}
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				r.walk(c, style, activeFormID, activeLinkID)
			}
			if isPara {
				r.blankLine()
			} else {
				r.flushLine()
			}
			return

		case "blockquote":
			r.blankLine()
			oldIndent := r.indent
			oldQuote := r.quoteMask
			r.indent = oldIndent + "| "
			r.quoteMask = StyleQuote
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				r.walk(c, style|StyleQuote, activeFormID, activeLinkID)
			}
			r.flushLine()
			r.indent = oldIndent
			r.quoteMask = oldQuote
			r.blankLine()
			return

		case "pre":
			r.blankLine()
			oldPre := r.inPre
			oldIndent := r.indent
			r.inPre = true
			r.indent = oldIndent + "  "
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				r.walk(c, style|StyleCode, activeFormID, activeLinkID)
			}
			r.flushLine()
			r.inPre = oldPre
			r.indent = oldIndent
			r.blankLine()
			return

		case "code", "tt", "kbd", "samp":
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				r.walk(c, style|StyleCode, activeFormID, activeLinkID)
			}
			return

		case "strong", "b":
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				r.walk(c, style|StyleBold, activeFormID, activeLinkID)
			}
			return

		case "em", "i", "u", "cite":
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				r.walk(c, style|StyleUnderline, activeFormID, activeLinkID)
			}
			return

		case "small":
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				r.walk(c, style|StyleDim, activeFormID, activeLinkID)
			}
			return

		case "ul", "ol":
			r.blankLine()
			r.listDepth++
			r.listStack = append(r.listStack, listState{ordered: tag == "ol", index: 0})
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				r.walk(c, style, activeFormID, activeLinkID)
			}
			r.flushLine()
			r.listStack = r.listStack[:len(r.listStack)-1]
			r.listDepth--
			if r.listDepth == 0 {
				r.blankLine()
			}
			return

		case "li":
			r.flushLine()
			bullet := "* "
			if len(r.listStack) > 0 {
				top := &r.listStack[len(r.listStack)-1]
				if top.ordered {
					top.index++
					bullet = fmt.Sprintf("%d. ", top.index)
				}
			}
			oldIndent := r.indent
			baseIndent := strings.Repeat("  ", maxInt(0, r.listDepth-1))
			r.indent = oldIndent + baseIndent
			r.appendRawSpan(bullet, style|StyleBold, 0)
			r.indent = oldIndent + baseIndent + strings.Repeat(" ", len(bullet))
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				r.walk(c, style, activeFormID, activeLinkID)
			}
			r.flushLine()
			r.indent = oldIndent
			return

		case "dl":
			r.blankLine()
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				r.walk(c, style, activeFormID, activeLinkID)
			}
			r.blankLine()
			return

		case "dt":
			r.flushLine()
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				r.walk(c, style|StyleBold, activeFormID, activeLinkID)
			}
			r.flushLine()
			return

		case "dd":
			r.flushLine()
			oldIndent := r.indent
			r.indent = oldIndent + "    "
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				r.walk(c, style, activeFormID, activeLinkID)
			}
			r.flushLine()
			r.indent = oldIndent
			return

		case "table":
			r.blankLine()
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				r.walk(c, style, activeFormID, activeLinkID)
			}
			r.blankLine()
			return

		case "thead", "tbody", "tfoot":
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				r.walk(c, style, activeFormID, activeLinkID)
			}
			return

		case "tr":
			r.flushLine()
			cellIdx := 0
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				if c.Type == html.ElementNode {
					ctag := strings.ToLower(c.Data)
					if ctag == "td" || ctag == "th" {
						if cellIdx > 0 && r.curCol > len(r.indent) {
							r.appendRawSpan(" | ", StyleDim, 0)
							r.needSpace = false
						}
						cellStyle := style
						if ctag == "th" {
							cellStyle |= StyleBold
						}
						for gc := c.FirstChild; gc != nil; gc = gc.NextSibling {
							r.walk(gc, cellStyle, activeFormID, activeLinkID)
						}
						cellIdx++
					}
				}
			}
			r.flushLine()
			return

		case "a":
			href := strings.TrimSpace(getAttr(n, "href"))
			if href == "" || strings.HasPrefix(strings.ToLower(href), "javascript:") {
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					r.walk(c, style, activeFormID, activeLinkID)
				}
				return
			}
			resolved := r.resolve(href)
			label := strings.TrimSpace(collapseSpaces(collectTextWithImgAlt(n)))
			if label == "" {
				label = getAttr(n, "title")
			}
			if label == "" {
				label = getAttr(n, "aria-label")
			}
			if label == "" {
				// Skip invisible/empty anchor tags so we don't spam numbered blanks.
				return
			}

			tID := len(r.doc.Targets) + 1
			lineIdx := len(r.doc.Lines)
			r.doc.Targets = append(r.doc.Targets, Target{
				ID:      tID,
				Kind:    TargetLink,
				Label:   label,
				URL:     resolved,
				LineIdx: lineIdx,
			})

			linkStyle := style | StyleLink | StyleUnderline
			r.writeInlineText(label, linkStyle, tID)
			// Update target line index in case wrapping placed it on a fresh line.
			r.doc.Targets[tID-1].LineIdx = r.targetLineIdx(tID, lineIdx)
			badge := fmt.Sprintf("[%d]", tID)
			r.appendRawSpan(badge, StyleBadge|StyleDim, tID)
			r.needSpace = true
			return

		case "img":
			alt := strings.TrimSpace(getAttr(n, "alt"))
			if alt != "" {
				r.writeInlineText("[img: "+alt+"]", style|StyleDim, activeLinkID)
			}
			return

		case "form":
			r.flushLine()
			action := r.resolve(strings.TrimSpace(getAttr(n, "action")))
			if action == "" && r.baseURL != nil {
				action = r.baseURL.String()
			}
			method := strings.ToUpper(strings.TrimSpace(getAttr(n, "method")))
			if method != "POST" {
				method = "GET"
			}
			fID := len(r.doc.Forms) + 1
			r.doc.Forms = append(r.doc.Forms, Form{
				ID:     fID,
				Action: action,
				Method: method,
			})
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				r.walk(c, style, fID, activeLinkID)
			}
			r.flushLine()
			return

		case "input":
			r.renderInput(n, activeFormID)
			return

		case "textarea":
			name := getAttr(n, "name")
			val := strings.TrimSpace(collectText(n))
			placeholder := getAttr(n, "placeholder")
			if placeholder == "" {
				placeholder = name
			}
			tID := len(r.doc.Targets) + 1
			t := Target{
				ID:        tID,
				Kind:      TargetInputText,
				Label:     placeholder,
				FormID:    activeFormID,
				FieldName: name,
				Value:     val,
				LineIdx:   len(r.doc.Lines),
			}
			r.doc.Targets = append(r.doc.Targets, t)
			text := formatFormControlText(&r.doc.Targets[tID-1])
			r.appendControlSpan(text, StyleInput|StyleUnderline, tID)
			return

		case "select":
			name := getAttr(n, "name")
			opts, selIdx := collectSelectOptions(n)
			if len(opts) == 0 {
				return
			}
			tID := len(r.doc.Targets) + 1
			t := Target{
				ID:          tID,
				Kind:        TargetSelect,
				Label:       name,
				FormID:      activeFormID,
				FieldName:   name,
				Options:     opts,
				SelectedIdx: selIdx,
				LineIdx:     len(r.doc.Lines),
			}
			r.doc.Targets = append(r.doc.Targets, t)
			text := formatFormControlText(&r.doc.Targets[tID-1])
			r.appendControlSpan(text, StyleInput|StyleBold, tID)
			return

		case "button":
			btype := strings.ToLower(strings.TrimSpace(getAttr(n, "type")))
			if btype == "" || btype == "submit" {
				label := strings.TrimSpace(collapseSpaces(collectText(n)))
				if label == "" {
					label = getAttr(n, "value")
				}
				if label == "" {
					label = getAttr(n, "aria-label")
				}
				if label == "" {
					label = "Submit"
				}
				tID := len(r.doc.Targets) + 1
				t := Target{
					ID:        tID,
					Kind:      TargetSubmit,
					Label:     label,
					FormID:    activeFormID,
					FieldName: getAttr(n, "name"),
					Value:     getAttr(n, "value"),
					LineIdx:   len(r.doc.Lines),
				}
				r.doc.Targets = append(r.doc.Targets, t)
				text := formatFormControlText(&r.doc.Targets[tID-1])
				r.appendControlSpan(text, StyleButton|StyleBold, tID)
				return
			}
			// Non-submit JS buttons: just show label subtly.
			label := strings.TrimSpace(collapseSpaces(collectText(n)))
			if label != "" {
				r.writeInlineText("["+label+"]", StyleDim, 0)
			}
			return

		default:
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				r.walk(c, style, activeFormID, activeLinkID)
			}
		}
	}
}

func (r *renderer) renderInput(n *html.Node, formID int) {
	itype := strings.ToLower(strings.TrimSpace(getAttr(n, "type")))
	if itype == "" {
		itype = "text"
	}
	name := getAttr(n, "name")
	val := getAttr(n, "value")
	placeholder := getAttr(n, "placeholder")
	if placeholder == "" {
		placeholder = getAttr(n, "aria-label")
	}
	if placeholder == "" {
		placeholder = name
	}

	switch itype {
	case "hidden":
		if formID > 0 {
			f := r.doc.FormByID(formID)
			if f != nil {
				f.HiddenFields = append(f.HiddenFields, FormField{Name: name, Value: val})
			}
		}
		return

	case "submit", "image":
		label := val
		if label == "" {
			label = getAttr(n, "alt")
		}
		if label == "" {
			label = "Submit"
		}
		tID := len(r.doc.Targets) + 1
		t := Target{
			ID:        tID,
			Kind:      TargetSubmit,
			Label:     label,
			FormID:    formID,
			FieldName: name,
			Value:     val,
			LineIdx:   len(r.doc.Lines),
		}
		r.doc.Targets = append(r.doc.Targets, t)
		text := formatFormControlText(&r.doc.Targets[tID-1])
		r.appendControlSpan(text, StyleButton|StyleBold, tID)
		return

	case "checkbox", "radio":
		kind := TargetInputCheckbox
		if itype == "radio" {
			kind = TargetInputRadio
		}
		checked := hasAttr(n, "checked")
		tID := len(r.doc.Targets) + 1
		t := Target{
			ID:        tID,
			Kind:      kind,
			Label:     placeholder,
			FormID:    formID,
			FieldName: name,
			Value:     val,
			Checked:   checked,
			LineIdx:   len(r.doc.Lines),
		}
		r.doc.Targets = append(r.doc.Targets, t)
		text := formatFormControlText(&r.doc.Targets[tID-1])
		r.appendControlSpan(text, StyleInput|StyleBold, tID)
		return

	case "reset", "button", "file":
		return

	default:
		// text, search, email, url, tel, number, password, etc.
		kind := TargetInputText
		if itype == "password" {
			kind = TargetInputPassword
		}
		tID := len(r.doc.Targets) + 1
		t := Target{
			ID:        tID,
			Kind:      kind,
			Label:     placeholder,
			FormID:    formID,
			FieldName: name,
			Value:     val,
			LineIdx:   len(r.doc.Lines),
		}
		r.doc.Targets = append(r.doc.Targets, t)
		text := formatFormControlText(&r.doc.Targets[tID-1])
		r.appendControlSpan(text, StyleInput|StyleUnderline, tID)
		return
	}
}

func formatFormControlText(t *Target) string {
	switch t.Kind {
	case TargetInputText, TargetInputPassword:
		display := t.Value
		if t.Kind == TargetInputPassword && display != "" {
			display = strings.Repeat("*", utf8.RuneCountInString(display))
		}
		if display == "" {
			if t.Label != "" {
				display = t.Label
			} else {
				display = "________"
			}
		}
		if len([]rune(display)) > 26 {
			display = string([]rune(display)[:23]) + "..."
		} else if len([]rune(display)) < 14 {
			display = display + strings.Repeat("_", 14-len([]rune(display)))
		}
		return fmt.Sprintf("[%d: %s]", t.ID, display)

	case TargetSubmit:
		lbl := t.Label
		if lbl == "" {
			lbl = "Submit"
		}
		return fmt.Sprintf("[%d: %s ]", t.ID, lbl)

	case TargetInputCheckbox:
		mark := " "
		if t.Checked {
			mark = "x"
		}
		return fmt.Sprintf("[%d: (%s)]", t.ID, mark)

	case TargetInputRadio:
		mark := " "
		if t.Checked {
			mark = "*"
		}
		return fmt.Sprintf("[%d: (%s)]", t.ID, mark)

	case TargetSelect:
		lbl := ""
		if t.SelectedIdx >= 0 && t.SelectedIdx < len(t.Options) {
			lbl = t.Options[t.SelectedIdx].Label
		}
		if lbl == "" {
			lbl = "select"
		}
		return fmt.Sprintf("[%d: <%s v>]", t.ID, lbl)

	default:
		return ""
	}
}

func (r *renderer) appendControlSpan(text string, style SpanStyle, tID int) {
	w := utf8.RuneCountInString(text)
	if r.curCol > len(r.indent) && r.curCol+1+w > r.width {
		r.flushLine()
	}
	if r.needSpace && r.curCol > len(r.indent) {
		r.appendRawSpan(" ", StyleNormal, 0)
	}
	r.ensureIndent()
	r.curSpans = append(r.curSpans, Span{Text: text, Style: style, TargetID: tID})
	r.curCol += w
	r.doc.Targets[tID-1].LineIdx = len(r.doc.Lines)
	r.needSpace = true
}

func (r *renderer) targetLineIdx(tID, fallback int) int {
	for i := len(r.doc.Lines) - 1; i >= fallback && i >= 0; i-- {
		for _, sp := range r.doc.Lines[i].Spans {
			if sp.TargetID == tID {
				return i
			}
		}
	}
	for _, sp := range r.curSpans {
		if sp.TargetID == tID {
			return len(r.doc.Lines)
		}
	}
	return fallback
}

func (r *renderer) ensureIndent() {
	if r.curCol == 0 && len(r.indent) > 0 {
		r.curSpans = append(r.curSpans, Span{Text: r.indent, Style: r.quoteMask})
		r.curCol = utf8.RuneCountInString(r.indent)
	}
}

func (r *renderer) appendRawSpan(text string, style SpanStyle, targetID int) {
	if text == "" {
		return
	}
	r.ensureIndent()
	r.curSpans = append(r.curSpans, Span{Text: text, Style: style, TargetID: targetID})
	r.curCol += utf8.RuneCountInString(text)
}

func (r *renderer) writeInlineText(raw string, style SpanStyle, targetID int) {
	if raw == "" {
		return
	}
	leadingSpace := unicode.IsSpace(firstRune(raw))
	trailingSpace := unicode.IsSpace(lastRune(raw))
	words := strings.Fields(raw)
	if len(words) == 0 {
		if r.curCol > len(r.indent) {
			r.needSpace = true
		}
		return
	}

	for i, word := range words {
		needSep := (i > 0) || leadingSpace || r.needSpace
		wLen := utf8.RuneCountInString(word)
		maxContentWidth := r.width - utf8.RuneCountInString(r.indent)
		if maxContentWidth < 8 {
			maxContentWidth = 8
		}

		// Handle very long unbroken tokens (like long URLs) by chunking them cleanly.
		if wLen > maxContentWidth {
			if r.curCol > utf8.RuneCountInString(r.indent) {
				r.flushLine()
			}
			runes := []rune(word)
			for len(runes) > 0 {
				take := maxContentWidth
				if take > len(runes) {
					take = len(runes)
				}
				r.appendRawSpan(string(runes[:take]), style, targetID)
				runes = runes[take:]
				if len(runes) > 0 {
					r.flushLine()
				}
			}
			r.needSpace = false
			continue
		}

		sepLen := 0
		if needSep && r.curCol > utf8.RuneCountInString(r.indent) {
			sepLen = 1
		}
		if r.curCol > utf8.RuneCountInString(r.indent) && r.curCol+sepLen+wLen > r.width {
			r.flushLine()
			sepLen = 0
		}
		if sepLen == 1 {
			// Keep space inside link styling if we're between words of the same link.
			spStyle := StyleNormal
			spTarget := 0
			if targetID > 0 && i > 0 {
				spStyle = style
				spTarget = targetID
			}
			r.appendRawSpan(" ", spStyle, spTarget)
		}
		r.appendRawSpan(word, style, targetID)
		r.needSpace = false
	}

	if trailingSpace {
		r.needSpace = true
	}
}

func (r *renderer) writePreText(raw string, style SpanStyle, targetID int) {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "\n")
	raw = strings.ReplaceAll(raw, "\t", "    ")
	parts := strings.Split(raw, "\n")
	for i, part := range parts {
		if i > 0 {
			r.flushLine()
		}
		if part == "" {
			if i > 0 && i < len(parts)-1 {
				r.doc.Lines = append(r.doc.Lines, Line{})
			}
			continue
		}
		avail := r.width - utf8.RuneCountInString(r.indent)
		if avail < 8 {
			avail = 8
		}
		for _, chunk := range wrapPlainLine(part, avail) {
			r.appendRawSpan(chunk, style, targetID)
			if len(chunk) >= avail {
				r.flushLine()
			}
		}
	}
}

func (r *renderer) addWrappedBlock(text string, style SpanStyle, indent string) {
	old := r.indent
	r.indent = indent
	r.writeInlineText(text, style, 0)
	r.flushLine()
	r.indent = old
}

func (r *renderer) addRule(ch rune) {
	r.flushLine()
	ruleW := r.width - utf8.RuneCountInString(r.indent)
	if ruleW > 60 {
		ruleW = 60
	}
	if ruleW < 8 {
		ruleW = 8
	}
	r.ensureIndent()
	r.curSpans = append(r.curSpans, Span{Text: strings.Repeat(string(ch), ruleW), Style: StyleDim})
	r.curCol += ruleW
	r.flushLine()
}

func (r *renderer) flushLine() {
	if len(r.curSpans) == 0 {
		r.curCol = 0
		r.needSpace = false
		return
	}
	// Merge adjacent spans with identical Style & TargetID to keep rendering fast.
	merged := make([]Span, 0, len(r.curSpans))
	for _, sp := range r.curSpans {
		if len(merged) > 0 && merged[len(merged)-1].Style == sp.Style && merged[len(merged)-1].TargetID == sp.TargetID {
			merged[len(merged)-1].Text += sp.Text
		} else {
			merged = append(merged, sp)
		}
	}
	r.doc.Lines = append(r.doc.Lines, Line{Spans: merged})
	r.curSpans = nil
	r.curCol = 0
	r.needSpace = false
}

func (r *renderer) blankLine() {
	r.flushLine()
	if len(r.doc.Lines) == 0 {
		return
	}
	last := r.doc.Lines[len(r.doc.Lines)-1]
	if len(last.Spans) == 0 || strings.TrimSpace(last.PlainText()) == "" {
		return
	}
	r.doc.Lines = append(r.doc.Lines, Line{})
}

func (r *renderer) trimTrailingBlanks() {
	for len(r.doc.Lines) > 0 {
		last := r.doc.Lines[len(r.doc.Lines)-1]
		if len(last.Spans) == 0 || strings.TrimSpace(last.PlainText()) == "" {
			r.doc.Lines = r.doc.Lines[:len(r.doc.Lines)-1]
		} else {
			break
		}
	}
}

func (r *renderer) resolve(href string) string {
	if r.baseURL == nil {
		return href
	}
	return resolveURL(r.baseURL.String(), href)
}

func resolveURL(base, ref string) string {
	if ref == "" {
		return base
	}
	b, err := url.Parse(base)
	if err != nil {
		return ref
	}
	u, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	resolved := b.ResolveReference(u)
	// Unwrap DuckDuckGo Lite redirect wrappers (//duckduckgo.com/l/?uddg=...) directly
	// to save an extra TLS round-trip on the XO-1 and display the true destination URL.
	if strings.HasSuffix(strings.ToLower(resolved.Hostname()), "duckduckgo.com") && resolved.Path == "/l/" {
		if target := resolved.Query().Get("uddg"); strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
			return target
		}
	}
	return resolved.String()
}

func shouldSkipNode(n *html.Node, tag string, readerMode bool) bool {
	if hasAttr(n, "hidden") || hasAttr(n, "popover") {
		return true
	}
	if tag == "dialog" && !hasAttr(n, "open") {
		return true
	}
	if strings.EqualFold(getAttr(n, "aria-hidden"), "true") {
		return true
	}
	styleAttr := strings.ToLower(strings.ReplaceAll(getAttr(n, "style"), " ", ""))
	if strings.Contains(styleAttr, "display:none") || strings.Contains(styleAttr, "visibility:hidden") {
		return true
	}
	if readerMode {
		switch tag {
		case "nav", "aside", "footer":
			return true
		}
		classID := strings.ToLower(getAttr(n, "class") + " " + getAttr(n, "id") + " " + getAttr(n, "role"))
		noiseTokens := []string{
			"navbar", "nav-bar", "navigation", "sidebar", "side-bar",
			"cookie", "consent", "banner", "breadcrumb", "footer",
			"mw-jump-link", "vector-header", "vector-column-start", "vector-toc",
			"vector-page-toolbar", "p-lang-btn", "mw-indicators", "vector-dropdown",
			"navbox", "catlinks", "printfooter", "mw-editsection",
		}
		for _, tok := range noiseTokens {
			if strings.Contains(classID, tok) {
				return true
			}
		}
	}
	return false
}

// findReaderRoot chooses the best main content node for Reader Mode.
func findReaderRoot(body *html.Node) *html.Node {
	// 1. Prefer MediaWiki/Wikipedia article body directly if present.
	if mw := findByID(body, "mw-content-text"); mw != nil && visibleTextLen(mw) >= 200 {
		return mw
	}

	// 2. Prefer explicit semantic landmarks (<article>, <main>, role="main").
	var preferred *html.Node
	var walkLandmarks func(*html.Node)
	walkLandmarks = func(n *html.Node) {
		if n == nil || preferred != nil {
			return
		}
		if n.Type == html.ElementNode {
			tag := strings.ToLower(n.Data)
			role := strings.ToLower(getAttr(n, "role"))
			if tag == "article" || tag == "main" || role == "main" {
				if visibleTextLen(n) >= 200 {
					preferred = n
					return
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walkLandmarks(c)
		}
	}
	walkLandmarks(body)
	if preferred != nil {
		return preferred
	}

	// 2. Otherwise score <div>/<section> blocks by paragraph density minus link clutter.
	var bestNode *html.Node
	bestScore := 0
	var scoreWalk func(*html.Node)
	scoreWalk = func(n *html.Node) {
		if n == nil {
			return
		}
		if n.Type == html.ElementNode {
			tag := strings.ToLower(n.Data)
			if tag == "div" || tag == "section" {
				pText, aText := measureTextRatio(n)
				score := pText - aText*2
				if score > bestScore {
					bestScore = score
					bestNode = n
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			scoreWalk(c)
		}
	}
	scoreWalk(body)
	if bestScore >= 200 {
		return bestNode
	}
	return nil
}

func measureTextRatio(root *html.Node) (paraChars, linkChars int) {
	var walk func(*html.Node, bool, bool)
	walk = func(n *html.Node, inP, inA bool) {
		if n == nil {
			return
		}
		if n.Type == html.TextNode {
			l := len(strings.TrimSpace(n.Data))
			if inA {
				linkChars += l
			} else if inP {
				paraChars += l
			}
			return
		}
		if n.Type == html.ElementNode {
			tag := strings.ToLower(n.Data)
			if tag == "script" || tag == "style" || tag == "nav" || tag == "footer" || tag == "aside" {
				return
			}
			if tag == "p" || tag == "li" {
				inP = true
			}
			if tag == "a" {
				inA = true
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c, inP, inA)
		}
	}
	walk(root, false, false)
	return paraChars, linkChars
}

func visibleTextLen(root *html.Node) int {
	total := 0
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n == nil {
			return
		}
		if n.Type == html.TextNode {
			total += len(strings.TrimSpace(n.Data))
			return
		}
		if n.Type == html.ElementNode {
			tag := strings.ToLower(n.Data)
			if tag == "script" || tag == "style" || tag == "svg" {
				return
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	return total
}

func findTitle(n *html.Node) string {
	t := findTag(n, "title")
	if t == nil {
		return ""
	}
	return collectText(t)
}

func findBaseHref(n *html.Node) string {
	b := findTag(n, "base")
	if b == nil {
		return ""
	}
	return strings.TrimSpace(getAttr(b, "href"))
}

func findTag(n *html.Node, name string) *html.Node {
	if n == nil {
		return nil
	}
	if n.Type == html.ElementNode && strings.EqualFold(n.Data, name) {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := findTag(c, name); found != nil {
			return found
		}
	}
	return nil
}

func findByID(n *html.Node, id string) *html.Node {
	if n == nil {
		return nil
	}
	if n.Type == html.ElementNode && strings.EqualFold(getAttr(n, "id"), id) {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := findByID(c, id); found != nil {
			return found
		}
	}
	return nil
}

func collectText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(cur *html.Node) {
		if cur == nil {
			return
		}
		if cur.Type == html.TextNode {
			b.WriteString(cur.Data)
		} else if cur.Type == html.ElementNode {
			tag := strings.ToLower(cur.Data)
			if tag == "script" || tag == "style" {
				return
			}
		}
		for c := cur.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

func collectTextWithImgAlt(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(cur *html.Node) {
		if cur == nil {
			return
		}
		if cur.Type == html.TextNode {
			b.WriteString(cur.Data)
		} else if cur.Type == html.ElementNode {
			tag := strings.ToLower(cur.Data)
			if tag == "script" || tag == "style" {
				return
			}
			if tag == "img" {
				if alt := strings.TrimSpace(getAttr(cur, "alt")); alt != "" {
					b.WriteString(" ")
					b.WriteString(alt)
					b.WriteString(" ")
				}
				return
			}
		}
		for c := cur.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

func collectSelectOptions(n *html.Node) ([]SelectOption, int) {
	var opts []SelectOption
	selected := 0
	var walk func(*html.Node)
	walk = func(cur *html.Node) {
		if cur == nil {
			return
		}
		if cur.Type == html.ElementNode && strings.EqualFold(cur.Data, "option") {
			label := strings.TrimSpace(collapseSpaces(collectText(cur)))
			val := getAttr(cur, "value")
			if !hasAttr(cur, "value") {
				val = label
			}
			if hasAttr(cur, "selected") {
				selected = len(opts)
			}
			opts = append(opts, SelectOption{Value: val, Label: label})
			return
		}
		for c := cur.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return opts, selected
}

func getAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val
		}
	}
	return ""
}

func hasAttr(n *html.Node, key string) bool {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return true
		}
	}
	return false
}

func collapseSpaces(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func firstRune(s string) rune {
	r, _ := utf8.DecodeRuneInString(s)
	return r
}

func lastRune(s string) rune {
	r, _ := utf8.DecodeLastRuneInString(s)
	return r
}

func wrapPlainLine(s string, width int) []string {
	runes := []rune(s)
	if len(runes) == 0 {
		return []string{""}
	}
	var out []string
	for len(runes) > width {
		out = append(out, string(runes[:width]))
		runes = runes[width:]
	}
	out = append(out, string(runes))
	return out
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
