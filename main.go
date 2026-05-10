package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

const usage = `usage:
  frag sync [--no-normalize] <url>   crawl URL via Anakin, diff chunks against last sync
  frag export                        write changed_chunks.jsonl from .frag-changes.json
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	switch os.Args[1] {
	case "sync":
		fs := flag.NewFlagSet("sync", flag.ExitOnError)
		noNormalize := fs.Bool("no-normalize", false, "bypass cosmetic-noise normalization before hashing (debug)")
		if err := fs.Parse(os.Args[2:]); err != nil {
			os.Exit(2)
		}
		if fs.NArg() != 1 {
			fmt.Fprint(os.Stderr, usage)
			os.Exit(2)
		}
		if err := runSync(ctx, fs.Arg(0), !*noNormalize); err != nil {
			fmt.Fprintf(os.Stderr, "frag: %v\n", err)
			os.Exit(1)
		}
	case "export":
		if len(os.Args) != 2 {
			fmt.Fprint(os.Stderr, usage)
			os.Exit(2)
		}
		if err := runExport(); err != nil {
			fmt.Fprintf(os.Stderr, "frag: %v\n", err)
			os.Exit(1)
		}
	case "-h", "--help", "help":
		fmt.Fprint(os.Stdout, usage)
	default:
		fmt.Fprintf(os.Stderr, "frag: unknown command %q\n%s", os.Args[1], usage)
		os.Exit(2)
	}
}

func runSync(ctx context.Context, target string, useNormalize bool) error {
	apiKey := os.Getenv("ANAKIN_API_KEY")
	if apiKey == "" {
		return fmt.Errorf("ANAKIN_API_KEY not set; export it before running sync (e.g. `export $(grep -v '^#' env | xargs)`)")
	}
	start := time.Now()
	outcome, err := crawl(ctx, apiKey, target, 20)
	duration := time.Since(start)
	if err != nil {
		return err
	}

	var current []StateChunk
	usedPages := 0
	for _, p := range outcome.Pages {
		if p.Status != "completed" || p.Markdown == "" {
			fmt.Fprintf(os.Stderr, "frag: skipping %s (status=%s, error=%s)\n", p.URL, p.Status, p.Error)
			continue
		}
		usedPages++
		current = append(current, chunksForPage(p.URL, p.Markdown, useNormalize)...)
	}
	if usedPages == 0 {
		return fmt.Errorf("anakin returned no usable pages for %s", target)
	}

	prev, err := loadState(stateFile)
	if err != nil {
		return err
	}
	isFirst := prev == nil

	now := time.Now().UTC()
	changes := diff(prev, current, target, now)

	newState := State{URL: target, SnapshotAt: now, Chunks: current}
	if err := saveJSON(stateFile, &newState); err != nil {
		return err
	}
	if err := saveJSON(changesFile, &changes); err != nil {
		return err
	}

	printSummary(os.Stdout, target, outcome, duration, changes, isFirst)
	fmt.Printf("%d added, %d modified, %d removed, %d unchanged\n",
		len(changes.Added), len(changes.Modified), len(changes.Removed), changes.UnchangedCount)
	return nil
}

const boxInner = 51

func visibleLen(s string) int {
	n := 0
	inEsc := false
	for _, r := range s {
		if inEsc {
			if r == 'm' {
				inEsc = false
			}
			continue
		}
		if r == '\x1b' {
			inEsc = true
			continue
		}
		n++
	}
	return n
}

func boxLine(content string) string {
	pad := boxInner - visibleLen(content)
	if pad < 0 {
		pad = 0
	}
	return "│" + content + strings.Repeat(" ", pad) + "│"
}

func boxTop(title string) string {
	titleSeg := "─ " + title + " "
	pad := boxInner - utf8.RuneCountInString(titleSeg)
	if pad < 0 {
		pad = 0
	}
	return "┌" + titleSeg + strings.Repeat("─", pad) + "┐"
}

func boxBottom() string {
	return "└" + strings.Repeat("─", boxInner) + "┘"
}

func truncateURL(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	if max <= 1 {
		return "…"
	}
	runes := []rune(s)
	return string(runes[:max-1]) + "…"
}

func dot(isTTY bool, color string) string {
	if !isTTY {
		return "●"
	}
	return "\x1b[" + color + "m●\x1b[0m"
}

func printSummary(w io.Writer, target string, outcome *crawlOutcome, dur time.Duration, c Changes, isFirst bool) {
	isTTY := false
	if f, ok := w.(*os.File); ok {
		if fi, err := f.Stat(); err == nil {
			isTTY = (fi.Mode() & os.ModeCharDevice) != 0
		}
	}

	urlLabel := " Source:  "
	urlBudget := boxInner - utf8.RuneCountInString(urlLabel)
	srcLine := urlLabel + truncateURL(target, urlBudget)

	shortJob := outcome.JobID
	if len(shortJob) > 8 {
		shortJob = shortJob[:8]
	}
	pageTotal := outcome.TotalPages
	if pageTotal == 0 {
		pageTotal = len(outcome.Pages)
	}
	pageLine := fmt.Sprintf(" Pages:   %d/%d  (job %s, %s)", outcome.CompletedPages, pageTotal, shortJob, dur.Round(time.Second))

	addedDot := dot(isTTY, "32")
	modDot := dot(isTTY, "33")
	remDot := dot(isTTY, "31")
	unchDot := "○"
	if isTTY {
		unchDot = "\x1b[90m○\x1b[0m"
	}

	chunkTotal := len(c.Added) + len(c.Modified) + c.UnchangedCount

	var headline string
	switch {
	case isFirst:
		headline = fmt.Sprintf(" Baseline established: %d chunks tracked", chunkTotal)
	case chunkTotal == 0:
		headline = " Re-embed avoided: —"
	default:
		pct := 100.0 * float64(c.UnchangedCount) / float64(chunkTotal)
		headline = fmt.Sprintf(" Re-embed avoided: %.1f%%  (%d of %d chunks)", pct, c.UnchangedCount, chunkTotal)
	}

	fmt.Fprintln(w, boxTop("frag sync"))
	fmt.Fprintln(w, boxLine(srcLine))
	fmt.Fprintln(w, boxLine(pageLine))
	fmt.Fprintln(w, boxLine(""))
	fmt.Fprintln(w, boxLine(fmt.Sprintf("   %s %-9s %3d", addedDot, "added", len(c.Added))))
	fmt.Fprintln(w, boxLine(fmt.Sprintf("   %s %-9s %3d", modDot, "modified", len(c.Modified))))
	fmt.Fprintln(w, boxLine(fmt.Sprintf("   %s %-9s %3d", remDot, "removed", len(c.Removed))))
	fmt.Fprintln(w, boxLine(fmt.Sprintf("   %s %-9s %3d", unchDot, "unchanged", c.UnchangedCount)))
	fmt.Fprintln(w, boxLine(""))
	fmt.Fprintln(w, boxLine(headline))
	fmt.Fprintln(w, boxBottom())
}

type jsonlLine struct {
	ChunkID    string `json:"chunk_id"`
	ChangeType string `json:"change_type"`
	URL        string `json:"url"`
	Text       string `json:"text"`
	PrevText   string `json:"prev_text"`
}

func runExport() error {
	b, err := os.ReadFile(changesFile)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no %s found; run `frag sync <url>` first", changesFile)
		}
		return fmt.Errorf("read %s: %w", changesFile, err)
	}
	var ch Changes
	if err := json.Unmarshal(b, &ch); err != nil {
		return fmt.Errorf("parse %s: %w", changesFile, err)
	}

	out, err := os.Create(jsonlFile)
	if err != nil {
		return fmt.Errorf("create %s: %w", jsonlFile, err)
	}
	defer out.Close()

	enc := json.NewEncoder(out)
	count := 0
	for _, e := range ch.Added {
		if err := enc.Encode(jsonlLine{ChunkID: e.ChunkID, ChangeType: "added", URL: e.URL, Text: e.Text, PrevText: ""}); err != nil {
			return fmt.Errorf("write %s: %w", jsonlFile, err)
		}
		count++
	}
	for _, e := range ch.Modified {
		if err := enc.Encode(jsonlLine{ChunkID: e.ChunkID, ChangeType: "modified", URL: e.URL, Text: e.Text, PrevText: e.PrevText}); err != nil {
			return fmt.Errorf("write %s: %w", jsonlFile, err)
		}
		count++
	}
	for _, e := range ch.Removed {
		if err := enc.Encode(jsonlLine{ChunkID: e.ChunkID, ChangeType: "removed", URL: e.URL, Text: "", PrevText: e.PrevText}); err != nil {
			return fmt.Errorf("write %s: %w", jsonlFile, err)
		}
		count++
	}
	fmt.Printf("wrote %d chunk(s) to %s\n", count, jsonlFile)
	return nil
}
