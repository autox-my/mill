// Package httpclient implements the mill store interface over the bearer-authenticated HTTP API.
package httpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/autox-my/mill/internal/domain"
)

const defaultTimeout = 15 * time.Second

// Client calls a remote `mill serve` HTTP API with Bearer authentication.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// New constructs a validated HTTP store client.
func New(baseURL, token string) (*Client, error) {
	trimmedURL := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if trimmedURL == "" {
		return nil, fmt.Errorf("%w: MILL_URL is required", domain.ErrInvalidInput)
	}
	u, err := url.Parse(trimmedURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("%w: MILL_URL must be an http(s) URL", domain.ErrInvalidInput)
	}
	trimmedToken := strings.TrimSpace(token)
	if trimmedToken == "" {
		return nil, fmt.Errorf("%w: MILL_API_TOKEN is required when MILL_URL is set", domain.ErrInvalidInput)
	}
	return &Client{
		baseURL:    trimmedURL,
		token:      trimmedToken,
		httpClient: &http.Client{Timeout: defaultTimeout},
	}, nil
}

// Close satisfies the closeable store contract (HTTP client holds no persistent handle).
func (c *Client) Close() error {
	return nil
}

// CreateSlice creates a new slice on the remote board.
func (c *Client) CreateSlice(ctx context.Context, sl domain.Slice) error {
	var created domain.Slice
	return c.doJSON(ctx, http.MethodPost, "/slices", sl, &created)
}

// GetSlice retrieves a slice by ID from the remote board.
func (c *Client) GetSlice(ctx context.Context, id string) (domain.Slice, error) {
	var out domain.Slice
	err := c.doJSON(ctx, http.MethodGet, "/slices/"+url.PathEscape(id), nil, &out)
	return out, err
}

// ListSlices lists slices on the remote board, optionally filtered by state.
func (c *Client) ListSlices(ctx context.Context, stateFilter string) ([]domain.Slice, error) {
	p := "/slices"
	if strings.TrimSpace(stateFilter) != "" {
		p += "?state=" + url.QueryEscape(stateFilter)
	}
	var out []domain.Slice
	err := c.doJSON(ctx, http.MethodGet, p, nil, &out)
	return out, err
}

// ClaimSlice atomically acquires a lease on the remote board.
func (c *Client) ClaimSlice(ctx context.Context, id, owner string, ttl time.Duration, _ time.Time) (domain.Slice, error) {
	return c.postLease(ctx, id, "claim", owner, ttl)
}

// RenewLease extends an active lease on the remote board.
func (c *Client) RenewLease(ctx context.Context, id, owner string, ttl time.Duration, _ time.Time) (domain.Slice, error) {
	return c.postLease(ctx, id, "renew", owner, ttl)
}

func (c *Client) postLease(ctx context.Context, id, action, owner string, ttl time.Duration) (domain.Slice, error) {
	payload := map[string]string{
		"owner": owner,
		"ttl":   ttl.String(),
	}
	var out domain.Slice
	err := c.doJSON(ctx, http.MethodPost, "/slices/"+url.PathEscape(id)+"/"+action, payload, &out)
	return out, err
}

// UpdateSliceState transitions a slice state and optional verdict/risk on the remote board.
func (c *Client) UpdateSliceState(ctx context.Context, id string, next domain.SliceState, verdict domain.Verdict, risk domain.RiskLevel, _ time.Time) (domain.Slice, error) {
	payload := map[string]string{
		"state":   string(next),
		"verdict": string(verdict),
		"risk":    string(risk),
	}
	var out domain.Slice
	err := c.doJSON(ctx, http.MethodPost, "/slices/"+url.PathEscape(id)+"/update", payload, &out)
	return out, err
}

// RecordRun appends a run event on the remote ledger.
func (c *Client) RecordRun(ctx context.Context, ev domain.RunEvent) (domain.RunEvent, error) {
	var out domain.RunEvent
	err := c.doJSON(ctx, http.MethodPost, "/runs", ev, &out)
	return out, err
}

// ListRuns retrieves run events from the remote ledger.
func (c *Client) ListRuns(ctx context.Context, sliceID string, limit int) ([]domain.RunEvent, error) {
	q := url.Values{}
	if strings.TrimSpace(sliceID) != "" {
		q.Set("slice_id", sliceID)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	p := "/runs"
	if encoded := q.Encode(); encoded != "" {
		p += "?" + encoded
	}
	var out []domain.RunEvent
	err := c.doJSON(ctx, http.MethodGet, p, nil, &out)
	return out, err
}

// Stats retrieves aggregated board and run ledger metrics.
func (c *Client) Stats(ctx context.Context) (domain.StatsSummary, error) {
	var out domain.StatsSummary
	err := c.doJSON(ctx, http.MethodGet, "/stats", nil, &out)
	return out, err
}

func (c *Client) doJSON(ctx context.Context, method, path string, in, out any) error {
	var bodyReader io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("call %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return mapHTTPError(resp.StatusCode, respBody)
	}
	if out != nil && len(bytes.TrimSpace(respBody)) > 0 {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

func mapHTTPError(status int, body []byte) error {
	var errPayload struct {
		Error string `json:"error"`
	}
	msg := strings.TrimSpace(string(body))
	if json.Unmarshal(body, &errPayload) == nil && strings.TrimSpace(errPayload.Error) != "" {
		msg = errPayload.Error
	}
	switch status {
	case http.StatusNotFound:
		return fmt.Errorf("%w: %s", domain.ErrNotFound, msg)
	case http.StatusForbidden:
		return fmt.Errorf("%w: %s", domain.ErrNotLeaseOwner, msg)
	case http.StatusBadRequest:
		return fmt.Errorf("%w: %s", domain.ErrInvalidInput, msg)
	case http.StatusConflict:
		return mapConflictError(msg)
	default:
		return fmt.Errorf("http %d: %s", status, msg)
	}
}

func mapConflictError(msg string) error {
	switch {
	case strings.Contains(msg, domain.ErrAlreadyExists.Error()):
		return fmt.Errorf("%w: %s", domain.ErrAlreadyExists, msg)
	case strings.Contains(msg, domain.ErrAlreadyClaimed.Error()):
		return fmt.Errorf("%w: %s", domain.ErrAlreadyClaimed, msg)
	case strings.Contains(msg, domain.ErrInvalidTransition.Error()):
		return fmt.Errorf("%w: %s", domain.ErrInvalidTransition, msg)
	default:
		return fmt.Errorf("conflict: %s", msg)
	}
}
