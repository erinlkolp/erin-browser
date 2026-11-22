package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Config stores user preferences persisted to ~/.config/erin/config.json.
type Config struct {
	Monochrome   bool   `json:"monochrome"`
	ReaderMode   bool   `json:"reader_mode"`
	SearchEngine string `json:"search_engine"`
	UserAgent    string `json:"user_agent"`
	HomeURL      string `json:"home_url"`
}

// Bookmark represents a saved website.
type Bookmark struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

// DefaultConfig returns sensible defaults tuned for an OLPC XO-1.
func DefaultConfig() Config {
	return Config{
		Monochrome:   false,
		ReaderMode:   false,
		SearchEngine: "marginalia",
		UserAgent:    "firefox",
		HomeURL:      "about:home",
	}
}

// DefaultBookmarks returns a curated starter list of text-friendly informative sites.
func DefaultBookmarks() []Bookmark {
	return []Bookmark{
		{Title: "Marginalia Search (Small Web & Info)", URL: "https://old-search.marginalia.nu/"},
		{Title: "Wikipedia (English)", URL: "https://en.wikipedia.org/wiki/Main_Page"},
		{Title: "Mojeek (Independent Search)", URL: "https://www.mojeek.com/"},
		{Title: "FrogFind (Retro Search & Reader)", URL: "http://frogfind.com/"},
		{Title: "DuckDuckGo Lite (Web Search)", URL: "https://lite.duckduckgo.com/lite/"},
		{Title: "NPR Text-Only News", URL: "https://text.npr.org/"},
		{Title: "CNN Lite News", URL: "https://lite.cnn.com/"},
		{Title: "68k.news (Text News Aggregator)", URL: "http://68k.news/"},
		{Title: "Hacker News", URL: "https://news.ycombinator.com/"},
		{Title: "Wikibooks", URL: "https://en.wikibooks.org/wiki/Main_Page"},
		{Title: "Simple English Wikipedia", URL: "https://simple.wikipedia.org/wiki/Main_Page"},
	}
}

// Dir returns the configuration directory (~/.config/erin).
func Dir() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "erin")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ".erin"
	}
	return filepath.Join(home, ".config", "erin")
}

func configPath() string    { return filepath.Join(Dir(), "config.json") }
func bookmarksPath() string { return filepath.Join(Dir(), "bookmarks.json") }

// LoadConfig loads config from disk, returning defaults if missing.
func LoadConfig() Config {
	cfg := DefaultConfig()
	data, err := os.ReadFile(configPath())
	if err != nil {
		return cfg
	}
	_ = json.Unmarshal(data, &cfg)
	if cfg.SearchEngine == "" {
		cfg.SearchEngine = "marginalia"
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = "firefox"
	}
	if cfg.HomeURL == "" {
		cfg.HomeURL = "about:home"
	}
	return cfg
}

// SaveConfig persists config to disk.
func SaveConfig(cfg Config) error {
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath(), data, 0o644)
}

// LoadBookmarks loads bookmarks from disk, creating defaults on first run.
func LoadBookmarks() []Bookmark {
	data, err := os.ReadFile(bookmarksPath())
	if err != nil {
		defaults := DefaultBookmarks()
		_ = SaveBookmarks(defaults)
		return defaults
	}
	var bms []Bookmark
	if err := json.Unmarshal(data, &bms); err != nil {
		return DefaultBookmarks()
	}
	return bms
}

// SaveBookmarks persists bookmarks to disk.
func SaveBookmarks(bms []Bookmark) error {
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(bms, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(bookmarksPath(), data, 0o644)
}

// AddBookmark adds or updates a bookmark by URL.
func AddBookmark(bms []Bookmark, title, rawURL string) []Bookmark {
	title = strings.TrimSpace(title)
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return bms
	}
	if title == "" {
		title = rawURL
	}
	for i := range bms {
		if bms[i].URL == rawURL {
			bms[i].Title = title
			return bms
		}
	}
	return append(bms, Bookmark{Title: title, URL: rawURL})
}

// RemoveBookmark deletes the bookmark at index idx.
func RemoveBookmark(bms []Bookmark, idx int) []Bookmark {
	if idx < 0 || idx >= len(bms) {
		return bms
	}
	return append(bms[:idx], bms[idx+1:]...)
}
