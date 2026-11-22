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
