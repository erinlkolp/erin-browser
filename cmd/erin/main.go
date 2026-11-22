package main

import (
	"flag"
	"fmt"
	"os"

	"erin-browser/internal/config"
	"erin-browser/internal/htmlrender"
	"erin-browser/internal/netclient"
	"erin-browser/internal/tui"
)

const version = "1.0.0"

func main() {
	var (
		monoFlag   = flag.Bool("mono", false, "Start in monochrome mode (ideal for OLPC XO-1 sunlight display)")
		colorFlag  = flag.Bool("color", false, "Start in color mode")
		readerFlag = flag.Bool("reader", false, "Enable Reader Mode (main-content extraction)")
		engineFlag = flag.String("engine", "", "Default search engine: marginalia, ddg, wiki, mojeek, frogfind, google")
		uaFlag     = flag.String("ua", "", "User-Agent profile: firefox, mobile, classic")
		dumpFlag   = flag.Bool("dump", false, "Fetch and print rendered text to stdout, then exit")
		widthFlag  = flag.Int("width", 80, "Column width used with --dump")
		verFlag    = flag.Bool("version", false, "Print version and exit")
	)
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Erin %s — lightweight modern text browser for OLPC XO-1 / Fedora 18\n\n", version)
		fmt.Fprintf(os.Stderr, "Usage:\n  erin [options] [url-or-search]\n\nOptions:\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if *verFlag {
		fmt.Printf("erin %s (static Go TLS 1.2/1.3 + embedded CA bundle)\n", version)
		return
	}

	cfg := config.LoadConfig()
	if *monoFlag {
		cfg.Monochrome = true
	}
	if *colorFlag {
		cfg.Monochrome = false
	}
	if *readerFlag {
		cfg.ReaderMode = true
	}
	if *engineFlag != "" {
		cfg.SearchEngine = *engineFlag
	}
	if *uaFlag != "" {
		cfg.UserAgent = *uaFlag
	}

	client, err := netclient.New(cfg.SearchEngine, cfg.UserAgent)
	if err != nil {
		fmt.Fprintf(os.Stderr, "erin: init error: %v\n", err)
		os.Exit(1)
	}

	initialTarget := ""
	if flag.NArg() > 0 {
		initialTarget = flag.Arg(0)
	}

	if *dumpFlag {
		if initialTarget == "" {
			fmt.Fprintln(os.Stderr, "erin: --dump requires a URL or search query")
			os.Exit(1)
		}
		resolved := client.ResolveInput(initialTarget)
		resp, err := client.Get(resolved, "")
		if err != nil {
			fmt.Fprintf(os.Stderr, "erin: fetch error: %v\n", err)
			os.Exit(1)
		}
		var doc *htmlrender.Document
		if resp.IsHTML {
			doc = htmlrender.RenderHTML(resp.Body, resp.FinalURL, *widthFlag, cfg.ReaderMode)
		} else {
			doc = htmlrender.RenderPlainText(resp.Body, resp.FinalURL, resp.FinalURL, *widthFlag)
		}
		fmt.Print(htmlrender.DumpPlain(doc))
		return
	}

	bookmarks := config.LoadBookmarks()
	app := tui.NewApp(client, cfg, bookmarks)
	if err := app.Run(initialTarget); err != nil {
		fmt.Fprintf(os.Stderr, "erin: %v\n", err)
		os.Exit(1)
	}
}
