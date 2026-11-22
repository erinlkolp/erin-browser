package netclient

import (
	"testing"
)

func TestResolveInput(t *testing.T) {
	c, err := New("ddg", "firefox")
	if err != nil {
		t.Fatalf("New client error: %v", err)
	}

	cases := []struct {
		in   string
		want string
	}{
		{"https://en.wikipedia.org/wiki/OLPC_XO", "https://en.wikipedia.org/wiki/OLPC_XO"},
		{"en.wikipedia.org", "https://en.wikipedia.org"},
		{"localhost:8080/test", "http://localhost:8080/test"},
		{"about:bookmarks", "about:bookmarks"},
		{"olpc xo-1 specs", "https://lite.duckduckgo.com/lite/?q=olpc+xo-1+specs"},
		{"w: AMD Geode", "https://en.wikipedia.org/w/index.php?search=AMD+Geode"},
		{"m: fedora 18", "https://www.mojeek.com/search?q=fedora+18"},
		{"f: retro web", "http://frogfind.com/?q=retro+web"},
	}

	for _, tc := range cases {
		got := c.ResolveInput(tc.in)
		if got != tc.want {
			t.Errorf("ResolveInput(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestExtractMetaRefreshURL(t *testing.T) {
	// Actual DuckDuckGo redirect HTML pattern (meta refresh inside <noscript>)
	ddgHTML := `<html><head><meta name='referrer' content='origin'></head><body><script language='JavaScript'>window.parent.location.replace("https://en.wikipedia.org/wiki/OLPC_XO");</script><noscript><META http-equiv='refresh' content="0;URL=https://en.wikipedia.org/wiki/OLPC_XO"></noscript></body></html>`
	got := extractMetaRefreshURL(ddgHTML, "https://duckduckgo.com/l/?uddg=foo")
	want := "https://en.wikipedia.org/wiki/OLPC_XO"
	if got != want {
		t.Fatalf("extractMetaRefreshURL(ddg) = %q, want %q", got, want)
	}

	// Relative meta refresh
	relHTML := `<html><head><meta http-equiv="refresh" content="1; url='/docs/intro.html'"></head></html>`
	got = extractMetaRefreshURL(relHTML, "https://example.com/old")
	want = "https://example.com/docs/intro.html"
	if got != want {
		t.Fatalf("extractMetaRefreshURL(rel) = %q, want %q", got, want)
	}

	// Long auto-reload timer (> 3s) should NOT be auto-followed
	reloadHTML := `<html><head><meta http-equiv="refresh" content="300; url=https://example.com/live"></head></html>`
	if got := extractMetaRefreshURL(reloadHTML, "https://example.com/live"); got != "" {
		t.Fatalf("expected long refresh timer to be ignored, got %q", got)
	}
}
