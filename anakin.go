package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

const anakinBase = "https://api.anakin.io/v1"

type pageResult struct {
	URL        string `json:"url"`
	Status     string `json:"status"`
	Markdown   string `json:"markdown"`
	Error      string `json:"error,omitempty"`
	DurationMs int64  `json:"durationMs,omitempty"`
}

type submitResp struct {
	JobID  string `json:"jobId"`
	Status string `json:"status"`
}

type pollResp struct {
	ID             string       `json:"id"`
	Status         string       `json:"status"`
	URL            string       `json:"url"`
	TotalPages     int          `json:"totalPages"`
	CompletedPages int          `json:"completedPages"`
	Results        []pageResult `json:"results"`
	Error          string       `json:"error,omitempty"`
}

func submitCrawl(ctx context.Context, hc *http.Client, apiKey, target string, maxPages int) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"url":      target,
		"maxPages": maxPages,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, anakinBase+"/crawl", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build submit request: %w", err)
	}
	req.Header.Set("X-API-Key", apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("POST %s/crawl: %w", anakinBase, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("anakin submit failed (%d): %s", resp.StatusCode, strTrunc(string(respBody), 400))
	}
	var sr submitResp
	if err := json.Unmarshal(respBody, &sr); err != nil {
		return "", fmt.Errorf("decode submit response: %w; body=%s", err, strTrunc(string(respBody), 400))
	}
	if sr.JobID == "" {
		return "", fmt.Errorf("anakin returned empty jobId; body=%s", strTrunc(string(respBody), 400))
	}
	return sr.JobID, nil
}

func pollCrawl(ctx context.Context, hc *http.Client, apiKey, jobID string) (*pollResp, error) {
	url := anakinBase + "/crawl/" + jobID
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build poll request: %w", err)
	}
	req.Header.Set("X-API-Key", apiKey)
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("anakin poll %s failed (%d): %s", jobID, resp.StatusCode, strTrunc(string(respBody), 400))
	}
	var pr pollResp
	if err := json.Unmarshal(respBody, &pr); err != nil {
		return nil, fmt.Errorf("decode poll response: %w; body=%s", err, strTrunc(string(respBody), 400))
	}
	return &pr, nil
}

type crawlOutcome struct {
	JobID          string
	Pages          []pageResult
	TotalPages     int
	CompletedPages int
}

func crawl(ctx context.Context, apiKey, target string, maxPages int) (*crawlOutcome, error) {
	hc := &http.Client{Timeout: 60 * time.Second}
	jobID, err := submitCrawl(ctx, hc, apiKey, target, maxPages)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(os.Stderr, "frag: submitted crawl job %s for %s (maxPages=%d)\n", jobID, target, maxPages)

	deadline := time.Now().Add(5 * time.Minute)
	delay := 2 * time.Second
	const maxDelay = 10 * time.Second

	for {
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("anakin crawl job %s did not complete within 5m; rerun frag sync to retry", jobID)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
		pr, err := pollCrawl(ctx, hc, apiKey, jobID)
		if err != nil {
			return nil, err
		}
		switch pr.Status {
		case "completed":
			fmt.Fprintf(os.Stderr, "frag: crawl %s complete: %d/%d pages\n", jobID, pr.CompletedPages, pr.TotalPages)
			return &crawlOutcome{JobID: jobID, Pages: pr.Results, TotalPages: pr.TotalPages, CompletedPages: pr.CompletedPages}, nil
		case "failed":
			return nil, fmt.Errorf("anakin crawl job %s failed: %s", jobID, pr.Error)
		case "pending", "processing":
			fmt.Fprintf(os.Stderr, "frag: %s ... (%d/%d done)\n", pr.Status, pr.CompletedPages, pr.TotalPages)
		default:
			return nil, fmt.Errorf("anakin returned unknown status %q for job %s", pr.Status, jobID)
		}
		if delay < maxDelay {
			delay *= 2
			if delay > maxDelay {
				delay = maxDelay
			}
		}
	}
}

func strTrunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(truncated)"
}
