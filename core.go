package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	stateFile   = ".frag-state.json"
	changesFile = ".frag-changes.json"
	jsonlFile   = "changed_chunks.jsonl"
	chunkWords  = 500
)

type StateChunk struct {
	ID       string `json:"id"`
	URL      string `json:"url"`
	Index    int    `json:"index"`
	Text     string `json:"text"`
	TextHash string `json:"text_hash"`
}

type State struct {
	URL        string       `json:"url"`
	SnapshotAt time.Time    `json:"snapshot_at"`
	Chunks     []StateChunk `json:"chunks"`
}

type AddedEntry struct {
	ChunkID string `json:"chunk_id"`
	URL     string `json:"url"`
	Text    string `json:"text"`
}

type ModifiedEntry struct {
	ChunkID  string `json:"chunk_id"`
	URL      string `json:"url"`
	Text     string `json:"text"`
	PrevText string `json:"prev_text"`
}

type RemovedEntry struct {
	ChunkID  string `json:"chunk_id"`
	URL      string `json:"url"`
	PrevText string `json:"prev_text"`
}

type Changes struct {
	URL            string          `json:"url"`
	SnapshotAt     time.Time       `json:"snapshot_at"`
	Added          []AddedEntry    `json:"added"`
	Modified       []ModifiedEntry `json:"modified"`
	Removed        []RemovedEntry  `json:"removed"`
	UnchangedCount int             `json:"unchanged_count"`
}

var blankRunRE = regexp.MustCompile(`\n{3,}`)

func normalizeMarkdown(md string) string {
	return blankRunRE.ReplaceAllString(strings.TrimSpace(md), "\n\n")
}

var (
	htmlCommentRE = regexp.MustCompile(`(?s)<!--.*?-->`)
	dropLineREs   = []*regexp.Regexp{
		regexp.MustCompile(`(?i)^\s*last updated[:\s]`),
		regexp.MustCompile(`(?i)^\s*updated[:\s]+\d{4}-\d{2}-\d{2}`),
		regexp.MustCompile(`(?i)^\s*updated\s+(on\s+)?(jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)`),
		regexp.MustCompile(`^\s*©\s*\d{4}`),
		regexp.MustCompile(`(?i)^\s*copyright\s+\d{4}`),
		regexp.MustCompile(`(?i)^\s*generated\s+(on|at)\s+`),
		regexp.MustCompile(`^\s*\d{4}-\d{2}-\d{2}T\d{2}:\d{2}`),
	}
)

func normalize(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = htmlCommentRE.ReplaceAllString(s, "")
	lines := strings.Split(s, "\n")
	kept := make([]string, 0, len(lines))
lineLoop:
	for _, line := range lines {
		line = strings.TrimRight(line, " \t")
		for _, re := range dropLineREs {
			if re.MatchString(line) {
				continue lineLoop
			}
		}
		kept = append(kept, line)
	}
	s = strings.Join(kept, "\n")
	s = blankRunRE.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

func chunkText(md string, size int) []string {
	words := strings.Fields(md)
	if len(words) == 0 {
		return nil
	}
	var out []string
	for i := 0; i < len(words); i += size {
		end := i + size
		if end > len(words) {
			end = len(words)
		}
		out = append(out, strings.Join(words[i:end], " "))
	}
	return out
}

func chunkID(url string, idx int) string {
	sum := sha256.Sum256([]byte(url + ":" + strconv.Itoa(idx)))
	return hex.EncodeToString(sum[:])[:12]
}

func textHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func chunksForPage(url, markdown string, useNormalize bool) []StateChunk {
	pieces := chunkText(normalizeMarkdown(markdown), chunkWords)
	out := make([]StateChunk, 0, len(pieces))
	for i, p := range pieces {
		hashIn := p
		if useNormalize {
			hashIn = normalize(p)
		}
		out = append(out, StateChunk{
			ID:       chunkID(url, i),
			URL:      url,
			Index:    i,
			Text:     p,
			TextHash: textHash(hashIn),
		})
	}
	return out
}

func loadState(path string) (*State, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("parse %s: %w (delete the file to reset)", path, err)
	}
	return &s, nil
}

func saveJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".frag-tmp-*")
	if err != nil {
		return fmt.Errorf("create temp in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("write %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("rename to %s: %w", path, err)
	}
	return nil
}

func diff(prev *State, curr []StateChunk, srcURL string, snapshotAt time.Time) Changes {
	prevByID := map[string]StateChunk{}
	if prev != nil {
		for _, c := range prev.Chunks {
			prevByID[c.ID] = c
		}
	}
	ch := Changes{URL: srcURL, SnapshotAt: snapshotAt}
	for _, c := range curr {
		if p, ok := prevByID[c.ID]; ok {
			if p.TextHash == c.TextHash {
				ch.UnchangedCount++
			} else {
				ch.Modified = append(ch.Modified, ModifiedEntry{
					ChunkID: c.ID, URL: c.URL, Text: c.Text, PrevText: p.Text,
				})
			}
			delete(prevByID, c.ID)
		} else {
			ch.Added = append(ch.Added, AddedEntry{
				ChunkID: c.ID, URL: c.URL, Text: c.Text,
			})
		}
	}
	for _, p := range prevByID {
		ch.Removed = append(ch.Removed, RemovedEntry{
			ChunkID: p.ID, URL: p.URL, PrevText: p.Text,
		})
	}
	return ch
}
