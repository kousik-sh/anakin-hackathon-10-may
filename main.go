package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const usage = `usage:
  frag sync <url>     crawl URL via Anakin, diff chunks against last sync
  frag export         write changed_chunks.jsonl from .frag-changes.json
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
		if len(os.Args) != 3 {
			fmt.Fprint(os.Stderr, usage)
			os.Exit(2)
		}
		if err := runSync(ctx, os.Args[2]); err != nil {
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

func runSync(ctx context.Context, target string) error {
	apiKey := os.Getenv("ANAKIN_API_KEY")
	if apiKey == "" {
		return fmt.Errorf("ANAKIN_API_KEY not set; export it before running sync (e.g. `export $(grep -v '^#' env | xargs)`)")
	}
	pages, err := crawl(ctx, apiKey, target, 20)
	if err != nil {
		return err
	}

	var current []StateChunk
	usedPages := 0
	for _, p := range pages {
		if p.Status != "completed" || p.Markdown == "" {
			fmt.Fprintf(os.Stderr, "frag: skipping %s (status=%s, error=%s)\n", p.URL, p.Status, p.Error)
			continue
		}
		usedPages++
		current = append(current, chunksForPage(p.URL, p.Markdown)...)
	}
	if usedPages == 0 {
		return fmt.Errorf("anakin returned no usable pages for %s", target)
	}

	prev, err := loadState(stateFile)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	changes := diff(prev, current, target, now)

	newState := State{URL: target, SnapshotAt: now, Chunks: current}
	if err := saveJSON(stateFile, &newState); err != nil {
		return err
	}
	if err := saveJSON(changesFile, &changes); err != nil {
		return err
	}

	fmt.Printf("%d added, %d modified, %d removed, %d unchanged\n",
		len(changes.Added), len(changes.Modified), len(changes.Removed), changes.UnchangedCount)
	return nil
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
