package tui

import (
	"bytes"
	"fmt"
	"html"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/term"

	"erin-browser/internal/config"
	"erin-browser/internal/htmlrender"
	"erin-browser/internal/netclient"
)

type pageEntry struct {
	URL         string
	Title       string
	RawBody     string
	IsHTML      bool
	IsTLS       bool
	TLSVersion  string
	Status      string
	ScrollY     int
	FocusID     int
	IsBookmarks bool
	IsHome      bool
	IsHelp      bool
}

type promptKind int

const (
	promptNone promptKind = iota
	promptURL
	promptFind
	promptBookmarkTitle
	promptFormInput
)

// App holds the full runtime state of the interactive terminal browser.
type App struct {
	client    *netclient.Client
	cfg       config.Config
	bookmarks []config.Bookmark

	width  int
	height int

	history []pageEntry
	histIdx int
	doc     *htmlrender.Document

	scrollY     int
	focusID     int
	jumpBuf     string
	findQuery   string
	statusMsg   string
	statusIsErr bool

	// Active inline bottom prompt state
	promptMode     promptKind
	promptLabel    string
	promptInput    []rune
	promptCursor   int
	promptMask     bool
	promptTargetID int
}

// NewApp creates an interactive browser instance.
func NewApp(client *netclient.Client, cfg config.Config, bookmarks []config.Bookmark) *App {
	return &App{
		client:    client,
		cfg:       cfg,
		bookmarks: bookmarks,
		histIdx:   -1,
	}
}

// Run enters raw terminal mode, loads initialURL (or about:home), and runs the UI loop.
func (a *App) Run(initialURL string) error {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return fmt.Errorf("stdin is not a terminal (use --dump for non-interactive output)")
	}

	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("enter raw terminal mode: %w", err)
	}
	defer func() {
		_, _ = os.Stdout.WriteString(ansiReset + ansiShowCur + ansiLeaveAlt)
		_ = term.Restore(fd, oldState)
	}()

	_, _ = os.Stdout.WriteString(ansiEnterAlt + ansiHideCur + ansiClearAll)
	a.updateSize()

	// Listen for terminal window resize events.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	defer signal.Stop(sigCh)
	go func() {
		for range sigCh {
			a.updateSize()
			a.relayoutCurrent()
			a.draw()
		}
	}()

	start := initialURL
	if start == "" {
		start = a.cfg.HomeURL
	}
	a.navigate(start, true)
	a.draw()

	reader := NewKeyReader(os.Stdin)
	for {
		ev, err := reader.ReadKey()
		if err != nil {
			return nil
		}
		if ev.Type == KeyCtrlC {
			return nil
		}
		if a.promptMode != promptNone {
			a.handlePromptKey(ev)
			a.draw()
			continue
		}
		if quit := a.handleBrowseKey(ev); quit {
			return nil
		}
		a.draw()
	}
}

func (a *App) updateSize() {
	w, h, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || w <= 0 || h <= 0 {
		w, h = 80, 24
	}
	if w < 30 {
		w = 30
	}
	if h < 8 {
		h = 8
	}
	a.width = w
	a.height = h
}

func (a *App) viewHeight() int {
	h := a.height - 2 // top header bar + bottom status/prompt bar
	if h < 1 {
		return 1
	}
	return h
}

func (a *App) currentPage() *pageEntry {
	if a.histIdx < 0 || a.histIdx >= len(a.history) {
		return nil
	}
	return &a.history[a.histIdx]
}

func (a *App) setStatus(msg string, isErr bool) {
	a.statusMsg = msg
	a.statusIsErr = isErr
}

func (a *App) saveCurrentPosition() {
	if p := a.currentPage(); p != nil {
		p.ScrollY = a.scrollY
		p.FocusID = a.focusID
	}
}

func (a *App) pushPage(p pageEntry) {
	a.saveCurrentPosition()
	if a.histIdx < len(a.history)-1 {
		a.history = a.history[:a.histIdx+1]
	}
	a.history = append(a.history, p)
	// Bound history to 30 pages so memory stays tiny on 256 MB RAM XO-1.
	const maxHistory = 30
	if len(a.history) > maxHistory {
		a.history = a.history[len(a.history)-maxHistory:]
	}
	a.histIdx = len(a.history) - 1
	a.scrollY = 0
	a.focusID = 0
	a.jumpBuf = ""
	a.relayoutCurrent()
	a.focusFirstVisibleTarget()
}

func (a *App) relayoutCurrent() {
	p := a.currentPage()
	if p == nil {
		return
	}
	switch {
	case p.IsHome:
		p.RawBody = a.buildHomeHTML()
		a.doc = htmlrender.RenderHTML(p.RawBody, "about:home", a.width, false)
	case p.IsBookmarks:
		p.RawBody = a.buildBookmarksHTML()
		a.doc = htmlrender.RenderHTML(p.RawBody, "about:bookmarks", a.width, false)
	case p.IsHelp:
		p.RawBody = a.buildHelpHTML()
		a.doc = htmlrender.RenderHTML(p.RawBody, "about:help", a.width, false)
	case p.IsHTML:
		a.doc = htmlrender.RenderHTML(p.RawBody, p.URL, a.width, a.cfg.ReaderMode)
	default:
		a.doc = htmlrender.RenderPlainText(p.RawBody, p.Title, p.URL, a.width)
	}
	if p.Title == "" && a.doc != nil {
		p.Title = a.doc.Title
	}
	a.clampScroll()
}

func (a *App) navigate(rawInput string, resolve bool) {
	target := strings.TrimSpace(rawInput)
	if target == "" {
		return
	}
	if resolve {
		target = a.client.ResolveInput(target)
	}

	switch strings.ToLower(target) {
	case "about:home":
		a.pushPage(pageEntry{
			URL:    "about:home",
			Title:  "Erin Home",
			IsHTML: true,
			IsHome: true,
		})
		a.setStatus("Home — press 'g' to search or enter URL, '?' for help", false)
		return

	case "about:bookmarks":
		a.pushPage(pageEntry{
			URL:         "about:bookmarks",
			Title:       "Bookmarks",
			IsHTML:      true,
			IsBookmarks: true,
		})
		a.setStatus("Bookmarks — Enter/number to open, 'd' to delete selected, Backspace to go back", false)
		return

	case "about:help":
		a.pushPage(pageEntry{
			URL:    "about:help",
			Title:  "Erin Help",
			IsHTML: true,
			IsHelp: true,
		})
		a.setStatus("Help — press Backspace or Left arrow to return", false)
		return
	}

	// Show immediate loading indicator before network I/O.
	a.setStatus("Loading "+target+" ...", false)
	a.draw()

	referer := ""
	if p := a.currentPage(); p != nil {
		referer = p.URL
	}

	resp, err := a.client.Get(target, referer)
	if err != nil {
		a.setStatus("Error: "+err.Error(), true)
		return
	}
	a.applyResponse(resp)
}

func (a *App) submitForm(formID int, submitTarget *htmlrender.Target) {
	if a.doc == nil {
		return
	}
	f := a.doc.FormByID(formID)
	if f == nil {
		a.setStatus("No form associated with this control", true)
		return
	}
	vals := a.doc.BuildFormValues(formID, submitTarget)
	action := f.Action
	if action == "" {
		if p := a.currentPage(); p != nil {
			action = p.URL
		}
	}

	referer := ""
	if p := a.currentPage(); p != nil {
		referer = p.URL
	}

	if f.Method == "POST" {
		a.setStatus("Submitting POST "+action+" ...", false)
		a.draw()
		resp, err := a.client.PostForm(action, vals, referer)
		if err != nil {
			a.setStatus("Submit error: "+err.Error(), true)
			return
		}
		a.applyResponse(resp)
		return
	}

	// GET submission appends encoded query parameters to action URL.
	u, err := url.Parse(action)
	if err != nil {
		a.setStatus("Invalid form action: "+err.Error(), true)
		return
	}
	u.RawQuery = vals.Encode()
	a.navigate(u.String(), false)
}

func (a *App) applyResponse(resp *netclient.Response) {
	entry := pageEntry{
		URL:        resp.FinalURL,
		RawBody:    resp.Body,
		IsHTML:     resp.IsHTML,
		IsTLS:      resp.IsTLS,
		TLSVersion: resp.TLSVersion,
		Status:     resp.Status,
	}
	a.pushPage(entry)
	if p := a.currentPage(); p != nil && a.doc != nil {
		p.Title = a.doc.Title
	}

	var notes []string
	if resp.TLSVersion != "" {
		notes = append(notes, resp.TLSVersion)
	}
	if resp.StatusCode >= 400 {
		notes = append(notes, resp.Status)
	}
	if resp.Truncated {
		notes = append(notes, "truncated at 6MB")
	}
	if a.doc != nil && a.doc.ReaderMode && a.doc.ReaderFallback {
		notes = append(notes, "reader fallback: full page")
	}
	if len(notes) > 0 {
		a.setStatus("Loaded ("+strings.Join(notes, ", ")+")", resp.StatusCode >= 400)
	} else {
		a.setStatus(fmt.Sprintf("Loaded %d lines, %d links/controls", len(a.doc.Lines), len(a.doc.Targets)), false)
	}
}

func (a *App) handleBrowseKey(ev KeyEvent) bool {
	// Digit keys build a link/control jump number!
	if ev.Type == KeyRune && ev.Rune >= '0' && ev.Rune <= '9' {
		if len(a.jumpBuf) < 5 {
			a.jumpBuf += string(ev.Rune)
			a.setStatus("Jump to #"+a.jumpBuf+" (Enter to open, Esc to cancel)", false)
		}
		return false
	}

	if a.jumpBuf != "" {
		switch ev.Type {
		case KeyEsc:
			a.jumpBuf = ""
			a.setStatus("Cancelled jump", false)
			return false
		case KeyBackspace:
			a.jumpBuf = a.jumpBuf[:len(a.jumpBuf)-1]
			if a.jumpBuf == "" {
				a.setStatus("", false)
			} else {
				a.setStatus("Jump to #"+a.jumpBuf+" (Enter to open, Esc to cancel)", false)
			}
			return false
		case KeyEnter:
			num, err := strconv.Atoi(a.jumpBuf)
			a.jumpBuf = ""
			if err == nil && a.doc != nil && num >= 1 && num <= len(a.doc.Targets) {
				a.focusTarget(num)
				a.activateFocusedTarget()
			} else {
				a.setStatus(fmt.Sprintf("No link/control #%d on this page", num), true)
			}
			return false
		default:
			// Any other command key cancels jump buffer and executes normally.
			a.jumpBuf = ""
		}
	}

	switch ev.Type {
	case KeyCtrlL:
		a.updateSize()
		a.relayoutCurrent()
		return false

	case KeyDown:
		a.scrollBy(1)
		return false
	case KeyUp:
		a.scrollBy(-1)
		return false
	case KeyPgDn:
		a.scrollBy(a.viewHeight() - 1)
		return false
	case KeyPgUp:
		a.scrollBy(-(a.viewHeight() - 1))
		return false
	case KeyHome:
		a.scrollY = 0
		a.focusFirstVisibleTarget()
		return false
	case KeyEnd:
		a.scrollY = a.maxScroll()
		a.focusFirstVisibleTarget()
		return false
	case KeyTab:
		a.stepTarget(1)
		return false
	case KeyShiftTab:
		a.stepTarget(-1)
		return false
	case KeyEnter:
		a.activateFocusedTarget()
		return false
	case KeyBackspace, KeyLeft:
		a.goBack()
		return false
	case KeyRight:
		a.goForward()
		return false
	}

	if ev.Type != KeyRune {
		return false
	}

	switch ev.Rune {
	case 'q', 'Q':
		return true
	case 'j':
		a.scrollBy(1)
	case 'k':
		a.scrollBy(-1)
	case ' ', 'f':
		a.scrollBy(a.viewHeight() - 1)
	case 'u':
		a.scrollBy(-(a.viewHeight() - 1))
	case 'n':
		if a.findQuery != "" {
			a.findNext(1)
		} else {
			a.stepTarget(1)
		}
	case 'N':
		if a.findQuery != "" {
			a.findNext(-1)
		} else {
			a.stepTarget(-1)
		}
	case 'p':
		a.stepTarget(-1)
	case 'H':
		a.goBack()
	case 'L':
		a.goForward()
	case 'g':
		a.openPrompt(promptURL, "Go / Search ("+a.client.SearchEngine().Name+"): ", "", false, 0)
	case 'G':
		cur := ""
		if p := a.currentPage(); p != nil && !strings.HasPrefix(p.URL, "about:") {
			cur = p.URL
		}
		a.openPrompt(promptURL, "Edit URL: ", cur, false, 0)
	case '/':
		a.openPrompt(promptFind, "Find: ", a.findQuery, false, 0)
	case 'm', 'M':
		a.cfg.Monochrome = !a.cfg.Monochrome
		_ = config.SaveConfig(a.cfg)
		if a.cfg.Monochrome {
			a.setStatus("Monochrome mode ON (XO-1 sunlight display)", false)
		} else {
			a.setStatus("Color mode ON", false)
		}
	case 'r':
		a.cfg.ReaderMode = !a.cfg.ReaderMode
		_ = config.SaveConfig(a.cfg)
		a.relayoutCurrent()
		if a.cfg.ReaderMode {
			a.setStatus("Reader Mode ON", false)
		} else {
			a.setStatus("Reader Mode OFF (full page)", false)
		}
	case 'R':
		if p := a.currentPage(); p != nil {
			a.navigate(p.URL, false)
		}
	case 'b', 'B':
		a.navigate("about:bookmarks", false)
	case 'a', 'A':
		p := a.currentPage()
		if p == nil || strings.HasPrefix(p.URL, "about:") {
			a.setStatus("Cannot bookmark internal page", true)
		} else {
			a.openPrompt(promptBookmarkTitle, "Bookmark title: ", p.Title, false, 0)
		}
	case 'd':
		// On bookmarks page, 'd' deletes the focused bookmark; elsewhere scrolls half-page down.
		if p := a.currentPage(); p != nil && p.IsBookmarks {
			a.deleteFocusedBookmark()
		} else {
			a.scrollBy(a.viewHeight() / 2)
		}
	case 'e', 'E':
		eng := a.client.CycleSearchEngine()
		a.cfg.SearchEngine = eng.ID
		_ = config.SaveConfig(a.cfg)
		a.setStatus("Search engine: "+eng.Name+" (prefix "+eng.Prefix+")", false)
		if p := a.currentPage(); p != nil && p.IsHome {
			a.relayoutCurrent()
		}
	case 'U':
		ua := a.client.CycleUserAgent()
		a.cfg.UserAgent = ua.ID
		_ = config.SaveConfig(a.cfg)
		a.setStatus("User-Agent: "+ua.Label, false)
	case 'o':
		a.navigate("about:home", false)
	case 'h', '?':
		a.navigate("about:help", false)
	}
	return false
}

func (a *App) openPrompt(mode promptKind, label, initial string, mask bool, targetID int) {
	a.promptMode = mode
	a.promptLabel = label
	a.promptInput = []rune(initial)
	a.promptCursor = len(a.promptInput)
	a.promptMask = mask
	a.promptTargetID = targetID
}

func (a *App) handlePromptKey(ev KeyEvent) {
	switch ev.Type {
	case KeyEsc:
		a.promptMode = promptNone
		a.setStatus("Cancelled", false)
		return
	case KeyCtrlU:
		a.promptInput = nil
		a.promptCursor = 0
		return
	case KeyCtrlW:
		for a.promptCursor > 0 && a.promptInput[a.promptCursor-1] == ' ' {
			a.promptInput = append(a.promptInput[:a.promptCursor-1], a.promptInput[a.promptCursor:]...)
			a.promptCursor--
		}
		for a.promptCursor > 0 && a.promptInput[a.promptCursor-1] != ' ' {
			a.promptInput = append(a.promptInput[:a.promptCursor-1], a.promptInput[a.promptCursor:]...)
			a.promptCursor--
		}
		return
	case KeyLeft:
		if a.promptCursor > 0 {
			a.promptCursor--
		}
		return
	case KeyRight:
		if a.promptCursor < len(a.promptInput) {
			a.promptCursor++
		}
		return
	case KeyHome:
		a.promptCursor = 0
		return
	case KeyEnd:
		a.promptCursor = len(a.promptInput)
		return
	case KeyBackspace:
		if a.promptCursor > 0 {
			a.promptInput = append(a.promptInput[:a.promptCursor-1], a.promptInput[a.promptCursor:]...)
			a.promptCursor--
		}
		return
	case KeyDelete:
		if a.promptCursor < len(a.promptInput) {
			a.promptInput = append(a.promptInput[:a.promptCursor], a.promptInput[a.promptCursor+1:]...)
		}
		return
	case KeyRune:
		r := ev.Rune
		if r >= 32 {
			a.promptInput = append(a.promptInput[:a.promptCursor], append([]rune{r}, a.promptInput[a.promptCursor:]...)...)
			a.promptCursor++
		}
		return
	case KeyEnter:
		val := string(a.promptInput)
		mode := a.promptMode
		targetID := a.promptTargetID
		a.promptMode = promptNone

		switch mode {
		case promptURL:
			if strings.TrimSpace(val) != "" {
				a.navigate(val, true)
			}
		case promptFind:
			a.findQuery = strings.TrimSpace(val)
			if a.findQuery == "" {
				a.setStatus("Search highlight cleared", false)
			} else {
				a.findNext(1)
			}
		case promptBookmarkTitle:
			if p := a.currentPage(); p != nil {
				a.bookmarks = config.AddBookmark(a.bookmarks, val, p.URL)
				if err := config.SaveBookmarks(a.bookmarks); err != nil {
					a.setStatus("Failed to save bookmark: "+err.Error(), true)
				} else {
					a.setStatus("Bookmarked: "+val, false)
				}
			}
		case promptFormInput:
			if a.doc == nil {
				return
			}
			t := a.doc.TargetByID(targetID)
			if t == nil {
				return
			}
			t.Value = val
			a.doc.RefreshTargetDisplay(targetID)
			// Convenience: if this form has only 1 visible text field (like search boxes),
			// submit immediately on Enter!
			if t.FormID > 0 && a.doc.FormVisibleInputs(t.FormID) == 1 {
				a.submitForm(t.FormID, nil)
			} else {
				a.setStatus("Updated field. Tab to next field or activate Submit button.", false)
			}
		}
	}
}

func (a *App) activateFocusedTarget() {
	if a.doc == nil || a.focusID <= 0 {
		return
	}
	t := a.doc.TargetByID(a.focusID)
	if t == nil {
		return
	}
	switch t.Kind {
	case htmlrender.TargetLink:
		a.navigate(t.URL, false)
	case htmlrender.TargetInputText, htmlrender.TargetInputPassword:
		lbl := t.Label
		if lbl == "" {
			lbl = t.FieldName
		}
		if lbl == "" {
			lbl = "Input"
		}
		a.openPrompt(promptFormInput, lbl+": ", t.Value, t.Kind == htmlrender.TargetInputPassword, t.ID)
	case htmlrender.TargetInputCheckbox:
		t.Checked = !t.Checked
		a.doc.RefreshTargetDisplay(t.ID)
	case htmlrender.TargetInputRadio:
		for i := range a.doc.Targets {
			ot := &a.doc.Targets[i]
			if ot.FormID == t.FormID && ot.Kind == htmlrender.TargetInputRadio && ot.FieldName == t.FieldName {
				ot.Checked = (ot.ID == t.ID)
				a.doc.RefreshTargetDisplay(ot.ID)
			}
		}
	case htmlrender.TargetSelect:
		if len(t.Options) > 0 {
			t.SelectedIdx = (t.SelectedIdx + 1) % len(t.Options)
			a.doc.RefreshTargetDisplay(t.ID)
			a.setStatus("Selected: "+t.Options[t.SelectedIdx].Label, false)
		}
	case htmlrender.TargetSubmit:
		a.submitForm(t.FormID, t)
	}
}

func (a *App) deleteFocusedBookmark() {
	if a.doc == nil || a.focusID <= 0 {
		a.setStatus("Select a bookmark first with Tab, then press 'd'", true)
		return
	}
	t := a.doc.TargetByID(a.focusID)
	if t == nil || t.Kind != htmlrender.TargetLink {
		return
	}
	for i, bm := range a.bookmarks {
		if bm.URL == t.URL {
			title := bm.Title
			a.bookmarks = config.RemoveBookmark(a.bookmarks, i)
			_ = config.SaveBookmarks(a.bookmarks)
			a.relayoutCurrent()
			a.focusFirstVisibleTarget()
			a.setStatus("Deleted bookmark: "+title, false)
			return
		}
	}
}

func (a *App) goBack() {
	if a.histIdx <= 0 {
		a.setStatus("Already at oldest page in history", false)
		return
	}
	a.saveCurrentPosition()
	a.histIdx--
	p := a.currentPage()
	a.scrollY = p.ScrollY
	a.focusID = p.FocusID
	a.relayoutCurrent()
	a.setStatus("Back: "+p.Title, false)
}

func (a *App) goForward() {
	if a.histIdx >= len(a.history)-1 {
		a.setStatus("Already at newest page in history", false)
		return
	}
	a.saveCurrentPosition()
	a.histIdx++
	p := a.currentPage()
	a.scrollY = p.ScrollY
	a.focusID = p.FocusID
	a.relayoutCurrent()
	a.setStatus("Forward: "+p.Title, false)
}

func (a *App) maxScroll() int {
	if a.doc == nil {
		return 0
	}
	m := len(a.doc.Lines) - a.viewHeight()
	if m < 0 {
		return 0
	}
	return m
}

func (a *App) clampScroll() {
	if a.scrollY < 0 {
		a.scrollY = 0
	}
	if m := a.maxScroll(); a.scrollY > m {
		a.scrollY = m
	}
}

func (a *App) scrollBy(delta int) {
	a.scrollY += delta
	a.clampScroll()
	// Keep focused target visible if it scrolled out of view.
	if a.doc != nil && a.focusID > 0 {
		t := a.doc.TargetByID(a.focusID)
		if t == nil || t.LineIdx < a.scrollY || t.LineIdx >= a.scrollY+a.viewHeight() {
			a.focusFirstVisibleTarget()
		}
	}
}

func (a *App) focusFirstVisibleTarget() {
	if a.doc == nil || len(a.doc.Targets) == 0 {
		a.focusID = 0
		return
	}
	vh := a.viewHeight()
	for _, t := range a.doc.Targets {
		if t.LineIdx >= a.scrollY && t.LineIdx < a.scrollY+vh {
			a.focusID = t.ID
			return
		}
	}
	a.focusID = 0
}

func (a *App) stepTarget(dir int) {
	if a.doc == nil || len(a.doc.Targets) == 0 {
		a.setStatus("No links or form fields on this page", false)
		return
	}
	total := len(a.doc.Targets)
	next := a.focusID + dir
	if a.focusID == 0 {
		if dir > 0 {
			next = 1
		} else {
			next = total
		}
	} else {
		if next > total {
			next = 1
		} else if next < 1 {
			next = total
		}
	}
	a.focusTarget(next)
}

func (a *App) focusTarget(id int) {
	if a.doc == nil {
		return
	}
	t := a.doc.TargetByID(id)
	if t == nil {
		return
	}
	a.focusID = id
	vh := a.viewHeight()
	if t.LineIdx < a.scrollY {
		a.scrollY = t.LineIdx
	} else if t.LineIdx >= a.scrollY+vh {
		a.scrollY = t.LineIdx - vh + 2
	}
	a.clampScroll()
	if t.Kind == htmlrender.TargetLink {
		a.setStatus(fmt.Sprintf("[%d] %s", t.ID, t.URL), false)
	} else {
		a.setStatus(fmt.Sprintf("[%d] Form control (%s) — press Enter to activate", t.ID, t.Label), false)
	}
}

func (a *App) findNext(dir int) {
	if a.doc == nil || a.findQuery == "" {
		return
	}
	lq := strings.ToLower(a.findQuery)
	total := len(a.doc.Lines)
	if total == 0 {
		return
	}
	start := a.scrollY + dir
	for i := 0; i < total; i++ {
		idx := (start + i*dir + total*2) % total
		if strings.Contains(strings.ToLower(a.doc.Lines[idx].PlainText()), lq) {
			a.scrollY = idx
			a.clampScroll()
			a.setStatus(fmt.Sprintf("Match %q on line %d (n/N next/prev)", a.findQuery, idx+1), false)
			return
		}
	}
	a.setStatus(fmt.Sprintf("Pattern not found: %q", a.findQuery), true)
}

func (a *App) draw() {
	var b bytes.Buffer
	b.Grow(a.width * a.height * 2)
	b.WriteString(ansiHideCur)

	// 1. Top Header Bar
	title := "Erin"
	urlStr := ""
	flags := []string{}
	if p := a.currentPage(); p != nil {
		if p.Title != "" {
			title = p.Title
		}
		urlStr = p.URL
		if p.IsTLS {
			flags = append(flags, "HTTPS")
		}
	}
	if a.cfg.ReaderMode {
		flags = append(flags, "READER")
	}
	if a.cfg.Monochrome {
		flags = append(flags, "MONO")
	}
	headerLeft := " " + title
	if urlStr != "" && urlStr != title {
		headerLeft += " - " + urlStr
	}
	headerRight := ""
	if len(flags) > 0 {
		headerRight = "[" + strings.Join(flags, "|") + "] "
	}
	writeBar(&b, 1, a.width, headerLeft, headerRight, a.cfg.Monochrome, true)

	// 2. Document Viewport
	vh := a.viewHeight()
	for row := 0; row < vh; row++ {
		lineIdx := a.scrollY + row
		if a.doc != nil && lineIdx >= 0 && lineIdx < len(a.doc.Lines) {
			writeDocumentLine(&b, row+2, a.width, a.doc.Lines[lineIdx], a.focusID, a.findQuery, a.cfg.Monochrome)
		} else {
			moveTo(&b, row+2, 1)
			b.WriteString(ansiReset)
			b.WriteString(ansiClearLine)
		}
	}

	// 3. Bottom Bar (Prompt or Status + Position)
	if a.promptMode != promptNone {
		displayVal := string(a.promptInput)
		if a.promptMask {
			displayVal = strings.Repeat("*", len(a.promptInput))
		}
		promptText := a.promptLabel + displayVal
		writeBar(&b, a.height, a.width, promptText, " [Enter/Esc]", a.cfg.Monochrome, false)
		curCol := len([]rune(a.promptLabel)) + a.promptCursor + 1
		if curCol > a.width {
			curCol = a.width
		}
		moveTo(&b, a.height, curCol)
		b.WriteString(ansiShowCur)
	} else {
		left := " " + a.statusMsg
		if a.statusMsg == "" {
			left = " g:Go/Search  b:Bookmarks  a:Add  r:Reader  m:Mono  Tab:Next  ?:Help  q:Quit"
		}
		pct := "100%"
		if a.doc != nil && len(a.doc.Lines) > vh {
			maxS := a.maxScroll()
			if maxS > 0 {
				pct = fmt.Sprintf("%d%%", (a.scrollY*100)/maxS)
			}
		}
		right := fmt.Sprintf(" %s ", pct)
		writeBar(&b, a.height, a.width, left, right, a.cfg.Monochrome, false)
	}

	_, _ = os.Stdout.Write(b.Bytes())
}

func (a *App) buildHomeHTML() string {
	var b strings.Builder
	eng := a.client.SearchEngine()
	b.WriteString("<h1>Erin Text Browser (OLPC XO-1 Edition)</h1>")
	b.WriteString("<p>Modern TLS 1.2/1.3 &amp; embedded root certificates — zero Fedora 18 SSL errors.</p>")

	// Direct search form on Home screen!
	b.WriteString("<h2>Quick Search</h2>")
	b.WriteString(`<form method="GET" action="https://lite.duckduckgo.com/lite/">`)
	b.WriteString(`<p>DuckDuckGo Lite: <input type="text" name="q" placeholder="search the web"> <input type="submit" value="Search"></p>`)
	b.WriteString(`</form>`)
	b.WriteString(`<form method="GET" action="https://en.wikipedia.org/w/index.php">`)
	b.WriteString(`<p>Wikipedia: <input type="text" name="search" placeholder="encyclopedia"> <input type="submit" value="Lookup"></p>`)
	b.WriteString(`</form>`)

	fmt.Fprintf(&b, "<p>Default <b>g</b> search engine: <b>%s</b> (press <b>e</b> to cycle, or type <code>w: topic</code>, <code>d: query</code>, <code>m: query</code>, <code>f: query</code>).</p>", html.EscapeString(eng.Name))
	b.WriteString("<p><i>Note on Google Search:</i> Google began requiring JavaScript on google.com/search in 2025, which blocks non-JS text browsers. DuckDuckGo Lite, Wikipedia, Mojeek, and FrogFind work natively here.</p>")

	b.WriteString("<h2>Your Bookmarks</h2><ul>")
	for _, bm := range a.bookmarks {
		fmt.Fprintf(&b, `<li><a href="%s">%s</a></li>`, html.EscapeString(bm.URL), html.EscapeString(bm.Title))
	}
	b.WriteString("</ul>")

	b.WriteString("<h2>Essential Keys</h2><ul>")
	b.WriteString("<li><b>g</b>: Open URL or search &nbsp; | &nbsp; <b>G</b>: Edit current URL</li>")
	b.WriteString("<li><b>Tab / Shift+Tab</b>: Next / previous link or form box &nbsp; | &nbsp; <b>0-9 + Enter</b>: Jump directly to link #</li>")
	b.WriteString("<li><b>m</b>: Toggle Monochrome mode (great for XO-1 reflective sunlight screen!)</li>")
	b.WriteString("<li><b>r</b>: Toggle Reader mode (strips page nav clutter)</li>")
	b.WriteString("<li><b>b</b>: Manage Bookmarks &nbsp; | &nbsp; <b>a</b>: Add current page to Bookmarks</li>")
	b.WriteString("<li><b>?</b>: Full keyboard help &nbsp; | &nbsp; <b>q</b>: Quit</li>")
	b.WriteString("</ul>")
	return b.String()
}

func (a *App) buildBookmarksHTML() string {
	var b strings.Builder
	b.WriteString("<h1>Bookmarks</h1>")
	b.WriteString("<p>Press <b>Tab</b> or type a link number + <b>Enter</b> to open. Press <b>d</b> to delete the highlighted bookmark. Press <b>Backspace</b> to go back.</p>")
	if len(a.bookmarks) == 0 {
		b.WriteString("<p><i>No bookmarks saved yet. Press <b>a</b> on any web page to bookmark it.</i></p>")
		return b.String()
	}
	b.WriteString("<ol>")
	for _, bm := range a.bookmarks {
		fmt.Fprintf(&b, `<li><a href="%s">%s</a> <small>(%s)</small></li>`,
			html.EscapeString(bm.URL),
			html.EscapeString(bm.Title),
			html.EscapeString(bm.URL),
		)
	}
	b.WriteString("</ol>")
	return b.String()
}

func (a *App) buildHelpHTML() string {
	return `<h1>Erin Browser — Keyboard Reference</h1>
<h2>Navigation</h2>
<ul>
<li><b>g</b> — Go to URL or search (supports prefixes <code>d:</code> DuckDuckGo, <code>w:</code> Wikipedia, <code>m:</code> Mojeek, <code>f:</code> FrogFind, <code>g:</code> Google)</li>
<li><b>G</b> — Edit current URL</li>
<li><b>Tab / Shift+Tab</b> (or <b>p</b>) — Focus next / previous link or form input</li>
<li><b>0-9 then Enter</b> — Jump straight to numbered link or form field</li>
<li><b>Enter</b> — Open focused link, edit form input, toggle checkbox, or submit button</li>
<li><b>Backspace / Left / H</b> — Back in history</li>
<li><b>Right / L</b> — Forward in history</li>
<li><b>R</b> — Reload current page</li>
<li><b>o</b> — Go to Home page (<code>about:home</code>)</li>
</ul>
<h2>Scrolling &amp; Searching</h2>
<ul>
<li><b>Up / Down</b> or <b>k / j</b> — Scroll up / down one line</li>
<li><b>PgUp / PgDn</b> or <b>u / Space</b> — Page up / page down</li>
<li><b>Home / End</b> — Top / bottom of page</li>
<li><b>/</b> — Find text in current page (<b>n</b> next match, <b>N</b> previous match)</li>
</ul>
<h2>XO-1 Display, Reader &amp; Bookmarks</h2>
<ul>
<li><b>m</b> — Toggle Monochrome mode (pure bold/underline/reverse, zero ANSI colors — ideal for XO-1 reflective sunlight screen; saved automatically)</li>
<li><b>r</b> — Toggle Reader Mode (extracts main article text and removes navbar clutter)</li>
<li><b>b</b> — Open Bookmarks page (press <b>d</b> on a highlighted bookmark to delete it)</li>
<li><b>a</b> — Add current page to Bookmarks</li>
<li><b>e</b> — Cycle default search engine</li>
<li><b>U</b> — Cycle User-Agent (Firefox Linux / Mobile Web / Erin XO-1)</li>
<li><b>Ctrl+L</b> — Redraw screen</li>
<li><b>q</b> — Quit</li>
</ul>`
}
