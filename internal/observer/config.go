// Package observer analyzes run ledger evidence and emits grounded Draft PR proposals.
package observer

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

const defaultHTTPTimeout = 30 * time.Second

var (
	// ErrInvalidConfig indicates missing or malformed Twelve-Factor LLM configuration.
	ErrInvalidConfig = errors.New("invalid observer config")
	// ErrNoEvidence indicates the run ledger report contains no slice_id evidence to ground a proposal.
	ErrNoEvidence = errors.New("no run ledger evidence")
	// ErrUngroundedProposal indicates the LLM output failed to cite an exact slice_id from the report.
	ErrUngroundedProposal = errors.New("ungrounded proposal")
	// ErrProtectedPath indicates a proposal attempted to touch claims/, .intent/, or .intentgate.yml.
	ErrProtectedPath = errors.New("proposal targets protected path")
)

// Config holds Twelve-Factor settings for the OpenAI-compatible LLM endpoint.
type Config struct {
	BaseURL     string
	APIKey      string
	Model       string
	HTTPTimeout time.Duration
}

// LoadConfigFromEnv loads and validates LLM configuration from environment variables.
func LoadConfigFromEnv() (Config, error) {
	cfg := Config{
		BaseURL:     strings.TrimSpace(os.Getenv("MILL_LLM_BASE_URL")),
		APIKey:      strings.TrimSpace(os.Getenv("MILL_LLM_API_KEY")),
		Model:       strings.TrimSpace(os.Getenv("MILL_LLM_MODEL")),
		HTTPTimeout: defaultHTTPTimeout,
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	return cfg, nil
}

// Validate checks that required Twelve-Factor configuration fields are present and well-formed.
func (c Config) Validate() error {
	if strings.TrimSpace(c.BaseURL) == "" {
		return fmt.Errorf("%w: MILL_LLM_BASE_URL is required", ErrInvalidConfig)
	}
	u, err := url.Parse(strings.TrimSpace(c.BaseURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%w: MILL_LLM_BASE_URL must be an http(s) URL", ErrInvalidConfig)
	}
	if strings.TrimSpace(c.APIKey) == "" {
		return fmt.Errorf("%w: MILL_LLM_API_KEY is required", ErrInvalidConfig)
	}
	if strings.TrimSpace(c.Model) == "" {
		return fmt.Errorf("%w: MILL_LLM_MODEL is required", ErrInvalidConfig)
	}
	return nil
}
