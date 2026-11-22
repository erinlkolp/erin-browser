package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigAndBookmarksPersistence(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	cfg := DefaultConfig()
	cfg.Monochrome = true
	cfg.ReaderMode = true
	cfg.SearchEngine = "wiki"
	if err := SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig failed: %v", err)
	}
	loaded := LoadConfig()
	if !loaded.Monochrome || !loaded.ReaderMode || loaded.SearchEngine != "wiki" {
		t.Fatalf("loaded config mismatch: %+v", loaded)
	}

	bms := LoadBookmarks()
	if len(bms) == 0 {
		t.Fatalf("expected default bookmarks on first run")
	}
	if _, err := os.Stat(filepath.Join(tmp, "erin", "bookmarks.json")); err != nil {
		t.Fatalf("expected bookmarks.json to be created: %v", err)
	}

	origLen := len(bms)
	bms = AddBookmark(bms, "OLPC Wiki", "https://wiki.laptop.org/go/XO-1")
	if len(bms) != origLen+1 {
		t.Fatalf("expected %d bookmarks after add, got %d", origLen+1, len(bms))
	}
	// Adding duplicate URL updates title rather than duplicating.
	bms = AddBookmark(bms, "OLPC XO-1 Wiki", "https://wiki.laptop.org/go/XO-1")
	if len(bms) != origLen+1 {
		t.Fatalf("duplicate URL should update in place, got %d", len(bms))
	}
	if bms[len(bms)-1].Title != "OLPC XO-1 Wiki" {
		t.Fatalf("expected updated title, got %q", bms[len(bms)-1].Title)
	}

	bms = RemoveBookmark(bms, len(bms)-1)
	if len(bms) != origLen {
		t.Fatalf("expected %d bookmarks after remove, got %d", origLen, len(bms))
	}
	if err := SaveBookmarks(bms); err != nil {
		t.Fatalf("SaveBookmarks failed: %v", err)
	}
}
