package htmlrender

import (
	"strings"
	"testing"
)

func TestRenderBasicHTMLAndLinks(t *testing.T) {
	sample := `<!doctype html>
<html>
<head><title>OLPC Test</title></head>
<body>
  <h1>Welcome XO-1</h1>
  <p>Read <a href="/wiki/OLPC_XO">OLPC XO</a> and <a href="javascript:void(0)">ignored js</a>.</p>
  <ul>
    <li>First item</li>
    <li>Second item</li>
  </ul>
</body>
</html>`

	doc := RenderHTML(sample, "https://en.wikipedia.org/wiki/Main", 60, false)
	if doc.Title != "OLPC Test" {
		t.Fatalf("expected title 'OLPC Test', got %q", doc.Title)
	}
	if len(doc.Targets) != 1 {
		t.Fatalf("expected 1 link target (ignoring javascript:), got %d", len(doc.Targets))
	}
	if doc.Targets[0].URL != "https://en.wikipedia.org/wiki/OLPC_XO" {
		t.Fatalf("unexpected resolved URL: %q", doc.Targets[0].URL)
	}
	plain := DumpPlain(doc)
	if !strings.Contains(plain, "Welcome XO-1") || !strings.Contains(plain, "OLPC XO[1]") {
		t.Fatalf("unexpected rendered output:\n%s", plain)
	}
}

func TestRenderFormSubmissionValues(t *testing.T) {
	sample := `<html><body>
<form action="/search" method="GET">
  <input type="hidden" name="kl" value="us-en">
  <input type="text" name="q" placeholder="Search">
  <input type="checkbox" name="safe" value="1" checked>
  <input type="submit" value="Go">
</form>
</body></html>`

	doc := RenderHTML(sample, "https://lite.duckduckgo.com/", 80, false)
	if len(doc.Forms) != 1 {
		t.Fatalf("expected 1 form, got %d", len(doc.Forms))
	}
	if doc.Forms[0].Action != "https://lite.duckduckgo.com/search" {
		t.Fatalf("unexpected form action: %q", doc.Forms[0].Action)
	}
	if doc.FormVisibleInputs(1) != 1 {
		t.Fatalf("expected 1 visible text input, got %d", doc.FormVisibleInputs(1))
	}
	// Simulate user typing into input target #1
	doc.Targets[0].Value = "olpc fedora 18"
	doc.RefreshTargetDisplay(1)
	vals := doc.BuildFormValues(1, nil)
	if vals.Get("q") != "olpc fedora 18" {
		t.Fatalf("expected q='olpc fedora 18', got %q", vals.Get("q"))
	}
	if vals.Get("kl") != "us-en" {
		t.Fatalf("expected hidden kl='us-en', got %q", vals.Get("kl"))
	}
	if vals.Get("safe") != "1" {
		t.Fatalf("expected checked safe='1', got %q", vals.Get("safe"))
	}
}

func TestReaderModeStripsNavClutter(t *testing.T) {
	sample := `<html><head><title>Article</title></head><body>
  <nav><a href="/home">Home</a> <a href="/login">Login</a> <a href="/about">About</a></nav>
  <article>
    <h1>Deep Dive on Geode LX</h1>
    <p>The AMD Geode LX 700 is a 32-bit x86 processor clocked at 433 MHz used in the OLPC XO-1 laptop. It supports MMX and 3DNow! instructions, has very low power consumption, and pairs with a sunlight-readable dual-mode display.</p>
    <p>Because it lacks SSE2 instructions, binaries compiled for modern i686 distributions must use softfloat or i586 instruction scheduling to avoid illegal instruction crashes.</p>
  </article>
  <footer>Copyright 2026 Clutter Footer</footer>
</body></html>`

	doc := RenderHTML(sample, "https://example.com/geode", 72, true)
	if doc.ReaderFallback {
		t.Fatalf("expected reader mode to succeed without fallback")
	}
	plain := DumpPlain(doc)
	if strings.Contains(plain, "Login") || strings.Contains(plain, "Clutter Footer") {
		t.Fatalf("reader mode should have stripped nav/footer, got:\n%s", plain)
	}
	if !strings.Contains(plain, "AMD Geode LX 700") {
		t.Fatalf("reader mode missing main article body:\n%s", plain)
	}
}

func TestClosedDialogIsSkipped(t *testing.T) {
	sample := `<html><body>
  <dialog><p>Hidden Filter Modal</p></dialog>
  <dialog open><p>Visible Open Dialog</p></dialog>
  <p>Main Search Result</p>
</body></html>`
	doc := RenderHTML(sample, "https://marginalia-search.com/", 80, false)
	plain := DumpPlain(doc)
	if strings.Contains(plain, "Hidden Filter Modal") {
		t.Fatalf("closed <dialog> should be skipped, got:\n%s", plain)
	}
	if !strings.Contains(plain, "Visible Open Dialog") || !strings.Contains(plain, "Main Search Result") {
		t.Fatalf("expected open dialog and main result, got:\n%s", plain)
	}
}
