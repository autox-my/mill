package observer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"slices"
	"strings"
)

const systemPrompt = `You are the mill observer. Analyze the provided run ledger report and propose a single Draft PR patch as JSON with fields: title, summary, cited_slice_ids, target_files, diff.
Rules:
1. Every cited_slice_ids entry MUST be an exact slice_id present in the report.
2. NEVER target or modify claims/, .intent/, or .intentgate.yml.`

// Proposal is a validated Draft PR patch proposal grounded in run ledger slice IDs.
type Proposal struct {
	Title         string   `json:"title"`
	Summary       string   `json:"summary"`
	CitedSliceIDs []string `json:"cited_slice_ids"`
	TargetFiles   []string `json:"target_files"`
	Diff          string   `json:"diff"`
}

// Client calls an OpenAI-compatible chat completions endpoint to emit grounded proposals.
type Client struct {
	cfg        Config
	httpClient *http.Client
}

// NewClient constructs an observer LLM client after validating Twelve-Factor config.
func NewClient(cfg Config) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	timeout := cfg.HTTPTimeout
	if timeout <= 0 {
		timeout = defaultHTTPTimeout
	}
	return &Client{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: timeout},
	}, nil
}

// Propose sends a grounded Report to the OpenAI-compatible /chat/completions endpoint and validates the returned Proposal.
func (c *Client) Propose(ctx context.Context, report Report) (Proposal, error) {
	if c == nil {
		return Proposal{}, fmt.Errorf("%w: nil client", ErrInvalidConfig)
	}
	if err := c.cfg.Validate(); err != nil {
		return Proposal{}, err
	}
	if !report.HasEvidence() {
		return Proposal{}, ErrNoEvidence
	}

	content, err := c.requestCompletion(ctx, report)
	if err != nil {
		return Proposal{}, err
	}

	var prop Proposal
	if err := json.Unmarshal([]byte(stripCodeFence(content)), &prop); err != nil {
		return Proposal{}, fmt.Errorf("decode proposal json: %w", err)
	}
	if err := validateProposal(prop, report); err != nil {
		return Proposal{}, err
	}
	return prop, nil
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
}

func (c *Client) requestCompletion(ctx context.Context, report Report) (string, error) {
	reportBytes, err := json.Marshal(report)
	if err != nil {
		return "", fmt.Errorf("marshal report: %w", err)
	}

	reqBody, err := json.Marshal(chatRequest{
		Model: c.cfg.Model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: string(reportBytes)},
		},
	})
	if err != nil {
		return "", fmt.Errorf("marshal chat request: %w", err)
	}

	endpoint := c.cfg.BaseURL + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("call chat completions: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("chat completions returned status %d", resp.StatusCode)
	}

	var parsed chatResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("decode chat response: %w", err)
	}
	if len(parsed.Choices) == 0 || strings.TrimSpace(parsed.Choices[0].Message.Content) == "" {
		return "", fmt.Errorf("chat completions returned empty choices")
	}
	return parsed.Choices[0].Message.Content, nil
}

func stripCodeFence(s string) string {
	trimmed := strings.TrimSpace(s)
	if !strings.HasPrefix(trimmed, "```") {
		return trimmed
	}
	lines := strings.Split(trimmed, "\n")
	if len(lines) < 3 {
		return trimmed
	}
	if strings.HasPrefix(strings.TrimSpace(lines[len(lines)-1]), "```") {
		return strings.TrimSpace(strings.Join(lines[1:len(lines)-1], "\n"))
	}
	return trimmed
}

func validateProposal(p Proposal, report Report) error {
	if strings.TrimSpace(p.Title) == "" || strings.TrimSpace(p.Diff) == "" {
		return fmt.Errorf("%w: proposal title and diff are required", ErrUngroundedProposal)
	}
	if err := validateCitations(p.CitedSliceIDs, report.SliceIDs); err != nil {
		return err
	}
	return validateNoProtectedPaths(p)
}

func validateCitations(cited []string, allowed []string) error {
	if len(cited) == 0 {
		return fmt.Errorf("%w: must cite at least one exact slice_id", ErrUngroundedProposal)
	}
	for _, id := range cited {
		if !slices.Contains(allowed, id) {
			return fmt.Errorf("%w: cited slice_id %q not in ledger evidence", ErrUngroundedProposal, id)
		}
	}
	return nil
}

// validateNoProtectedPaths enforces defense-in-depth so observer never mutates claims/ or .intent/.
func validateNoProtectedPaths(p Proposal) error {
	for _, tf := range p.TargetFiles {
		if isProtectedPath(tf) {
			return fmt.Errorf("%w: target_files contains %q", ErrProtectedPath, tf)
		}
	}
	diffPaths := extractDiffPaths(p.Diff)
	if len(diffPaths) == 0 {
		return fmt.Errorf("%w: diff contains no valid file headers", ErrProtectedPath)
	}
	for _, dp := range diffPaths {
		if isProtectedPath(dp) {
			return fmt.Errorf("%w: diff touches %q", ErrProtectedPath, dp)
		}
	}
	return nil
}

func extractDiffPaths(diff string) []string {
	var paths []string
	for _, line := range strings.Split(diff, "\n") {
		if p, ok := parseDiffHeaderLine(line); ok {
			paths = append(paths, p)
		}
	}
	return paths
}

func parseDiffHeaderLine(line string) (string, bool) {
	switch {
	case strings.HasPrefix(line, "--- "):
		return strings.TrimSpace(strings.TrimPrefix(line, "--- ")), true
	case strings.HasPrefix(line, "+++ "):
		return strings.TrimSpace(strings.TrimPrefix(line, "+++ ")), true
	case strings.HasPrefix(line, "diff --git "):
		fields := strings.Fields(line)
		if len(fields) >= 4 {
			return fields[3], true
		}
	case strings.HasPrefix(line, "rename from "):
		return strings.TrimSpace(strings.TrimPrefix(line, "rename from ")), true
	case strings.HasPrefix(line, "rename to "):
		return strings.TrimSpace(strings.TrimPrefix(line, "rename to ")), true
	}
	return "", false
}

func isProtectedPath(raw string) bool {
	p := strings.ReplaceAll(strings.TrimSpace(raw), "\\", "/")
	if p == "" || p == "/dev/null" {
		return false
	}
	p = strings.TrimPrefix(p, "a/")
	p = strings.TrimPrefix(p, "b/")
	p = strings.TrimLeft(p, "/")
	cleaned := path.Clean(p)
	if cleaned == "." || strings.HasPrefix(cleaned, "..") {
		return true
	}
	return cleaned == "claims" ||
		strings.HasPrefix(cleaned, "claims/") ||
		cleaned == ".intent" ||
		strings.HasPrefix(cleaned, ".intent/") ||
		cleaned == ".intentgate.yml"
}
