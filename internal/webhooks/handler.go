package webhooks

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/autox-my/mill/internal/domain"
	"github.com/autox-my/mill/internal/ingest"
)

const (
	maxBodyBytes     = 1 << 20 // 1 MiB
	sourceForgejo    = "forgejo"
	sourcePulse      = "pulse"
	defaultPulseRepo = "homelab/pulse"
	envForgejoSecret = "MILL_WEBHOOK_FORGEJO_SECRET"
	envPulseSecret   = "MILL_WEBHOOK_PULSE_SECRET"
	pulseIDPrefix    = "PULSE-"
	pulseIDHexLen    = 12
)

var (
	signatureHeaders = []string{
		"X-Forgejo-Signature",
		"X-Hub-Signature-256",
		"X-Pulse-Signature",
		"X-Mill-Signature",
	}
	deliveryHeaders = []string{
		"X-Forgejo-Delivery",
		"X-GitHub-Delivery",
		"X-Pulse-Delivery",
		"X-Delivery-ID",
	}
)

// SliceStore defines the slice persistence operations required by webhook handlers.
type SliceStore interface {
	CreateSlice(ctx context.Context, sl domain.Slice) error
	GetSlice(ctx context.Context, id string) (domain.Slice, error)
	UpdateSliceState(ctx context.Context, id string, next domain.SliceState, verdict domain.Verdict, risk domain.RiskLevel, now time.Time) (domain.Slice, error)
}

// Config configures the webhook HTTP handler.
type Config struct {
	Store         SliceStore
	Deliveries    DeliveryStore
	ForgejoSecret string
	PulseSecret   string
}

// LoadConfigFromEnv loads webhook secrets from environment variables (Twelve-Factor config).
func LoadConfigFromEnv(st SliceStore, deliveries DeliveryStore) Config {
	return Config{
		Store:         st,
		Deliveries:    deliveries,
		ForgejoSecret: strings.TrimSpace(os.Getenv(envForgejoSecret)),
		PulseSecret:   strings.TrimSpace(os.Getenv(envPulseSecret)),
	}
}

type server struct {
	store         SliceStore
	deliveries    DeliveryStore
	forgejoSecret string
	pulseSecret   string
}

// NewHandler constructs an HTTP handler exposing POST /webhooks/{name} for forgejo and pulse.
func NewHandler(cfg Config) http.Handler {
	s := &server{
		store:         cfg.Store,
		deliveries:    cfg.Deliveries,
		forgejoSecret: cfg.ForgejoSecret,
		pulseSecret:   cfg.PulseSecret,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhooks/{name}", s.handleWebhook)
	return mux
}

func (s *server) secretForSource(source string) (string, bool) {
	switch source {
	case sourceForgejo:
		return s.forgejoSecret, true
	case sourcePulse:
		return s.pulseSecret, true
	default:
		return "", false
	}
}

func (s *server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	source := strings.ToLower(strings.TrimSpace(r.PathValue("name")))
	secret, known := s.secretForSource(source)
	if !known {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown webhook source"})
		return
	}
	if s.store == nil || s.deliveries == nil || secret == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "webhook endpoint is not configured"})
		return
	}

	rawBody, ok := readLimitedBody(w, r)
	if !ok {
		return
	}
	if !verifyRequestAuth(r, secret, rawBody) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	var obj map[string]any
	if err := json.Unmarshal(rawBody, &obj); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json payload"})
		return
	}

	now := time.Now().UTC()
	deliveryID := extractDeliveryID(r, source, rawBody)
	eventType := extractEventType(r, source, obj)
	env := WrapUntrusted(source, deliveryID, eventType, rawBody, now)

	inserted, err := s.deliveries.RecordDelivery(r.Context(), DeliveryRecord{
		DeliveryID: env.DeliveryID,
		Source:     env.Source,
		EventType:  env.EventType,
		Status:     "received",
		ReceivedAt: env.ReceivedAt,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "record delivery failed"})
		return
	}
	if !inserted {
		writeJSON(w, http.StatusOK, map[string]string{
			"status":      "duplicate",
			"delivery_id": env.DeliveryID,
		})
		return
	}

	if source == sourceForgejo {
		s.handleForgejoEvent(w, r, env)
		return
	}
	s.handlePulseEvent(w, r, env)
}

func readLimitedBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "payload exceeds 1 MiB limit"})
			return nil, false
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "failed to read request body"})
		return nil, false
	}
	return body, true
}

func verifyRequestAuth(r *http.Request, secret string, rawBody []byte) bool {
	if tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && tok != "" {
		if subtle.ConstantTimeCompare([]byte(tok), []byte(secret)) == 1 {
			return true
		}
	}
	for _, hdr := range signatureHeaders {
		sig := strings.TrimSpace(r.Header.Get(hdr))
		if sig != "" && verifyHMACSignature(secret, rawBody, sig) {
			return true
		}
	}
	return false
}

func verifyHMACSignature(secret string, rawBody []byte, headerVal string) bool {
	sigHex := strings.TrimSpace(headerVal)
	if len(sigHex) >= 7 && strings.EqualFold(sigHex[:7], "sha256=") {
		sigHex = sigHex[7:]
	}
	gotMAC, err := hex.DecodeString(sigHex)
	if err != nil || len(gotMAC) != sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(rawBody)
	expectedMAC := mac.Sum(nil)
	return subtle.ConstantTimeCompare(gotMAC, expectedMAC) == 1
}

func extractDeliveryID(r *http.Request, source string, rawBody []byte) string {
	for _, hdr := range deliveryHeaders {
		if v := strings.TrimSpace(r.Header.Get(hdr)); v != "" {
			return v
		}
	}
	sum := sha256.Sum256(append([]byte(source+":"), rawBody...))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func extractEventType(r *http.Request, source string, payload map[string]any) string {
	if source == sourceForgejo {
		for _, hdr := range []string{"X-Forgejo-Event", "X-GitHub-Event"} {
			if v := strings.TrimSpace(r.Header.Get(hdr)); v != "" {
				return strings.ToLower(v)
			}
		}
	}
	if source == sourcePulse {
		if v := strings.TrimSpace(r.Header.Get("X-Pulse-Event")); v != "" {
			return strings.ToLower(v)
		}
	}
	for _, key := range []string{"event", "event_type", "type"} {
		if v, ok := payload[key].(string); ok && strings.TrimSpace(v) != "" {
			return strings.ToLower(strings.TrimSpace(v))
		}
	}
	return "unknown"
}

type forgejoPRPayload struct {
	Action      string `json:"action"`
	PullRequest struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Merged bool   `json:"merged"`
		Head   struct {
			Ref   string `json:"ref"`
			Label string `json:"label"`
		} `json:"head"`
	} `json:"pull_request"`
	Repository struct {
		FullName string `json:"full_name"`
		Name     string `json:"name"`
	} `json:"repository"`
}

func (s *server) handleForgejoEvent(w http.ResponseWriter, r *http.Request, env UntrustedEnvelope) {
	if env.EventType != "pull_request" {
		writeJSON(w, http.StatusOK, map[string]string{
			"status":      "ignored",
			"delivery_id": env.DeliveryID,
		})
		return
	}
	var payload forgejoPRPayload
	if err := json.Unmarshal(env.RawPayload, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid pull_request payload"})
		return
	}

	action := strings.ToLower(strings.TrimSpace(payload.Action))
	ref := strings.TrimSpace(payload.PullRequest.Head.Ref)
	if ref == "" {
		ref = strings.TrimSpace(payload.PullRequest.Head.Label)
	}
	sliceID := ingest.ResolveSliceID("", ref)

	if action == "closed" && payload.PullRequest.Merged {
		s.handleForgejoMerged(w, r, env, sliceID)
		return
	}
	if action == "opened" || action == "reopened" {
		s.handleForgejoOpened(w, r, env, payload, ref, sliceID)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"status":      "ignored",
		"delivery_id": env.DeliveryID,
	})
}

func (s *server) handleForgejoMerged(w http.ResponseWriter, r *http.Request, env UntrustedEnvelope, sliceID string) {
	if sliceID == "" {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ignored", "delivery_id": env.DeliveryID})
		return
	}
	_, err := s.store.UpdateSliceState(r.Context(), sliceID, domain.StateMerged, domain.VerdictGreen, "", env.ReceivedAt)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrInvalidTransition) {
			writeJSON(w, http.StatusOK, map[string]string{
				"status":      "ignored",
				"delivery_id": env.DeliveryID,
				"slice_id":    sliceID,
				"reason":      err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "update slice state failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"status":      "merged",
		"delivery_id": env.DeliveryID,
		"slice_id":    sliceID,
	})
}

func (s *server) handleForgejoOpened(w http.ResponseWriter, r *http.Request, env UntrustedEnvelope, payload forgejoPRPayload, ref, sliceID string) {
	if sliceID == "" {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ignored", "delivery_id": env.DeliveryID})
		return
	}
	if _, err := s.store.GetSlice(r.Context(), sliceID); err == nil {
		writeJSON(w, http.StatusOK, map[string]string{
			"status":      "ignored",
			"delivery_id": env.DeliveryID,
			"slice_id":    sliceID,
		})
		return
	} else if !errors.Is(err, domain.ErrNotFound) {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lookup slice failed"})
		return
	}

	title := SanitizeTitle(payload.PullRequest.Title)
	if title == "" {
		title = fmt.Sprintf("PR #%d", payload.PullRequest.Number)
	}
	repo := SanitizeString(payload.Repository.FullName, 160)
	if repo == "" {
		repo = SanitizeString(payload.Repository.Name, 160)
	}
	if repo == "" {
		repo = "unknown/repo"
	}

	err := s.store.CreateSlice(r.Context(), domain.Slice{
		ID:     sliceID,
		Repo:   repo,
		Title:  title,
		Branch: SanitizeString(ref, 160),
		State:  domain.StateSpec,
		Risk:   domain.RiskLow,
	})
	if err != nil {
		if errors.Is(err, domain.ErrAlreadyExists) {
			writeJSON(w, http.StatusOK, map[string]string{
				"status":      "ignored",
				"delivery_id": env.DeliveryID,
				"slice_id":    sliceID,
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "create slice failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"status":      "created",
		"delivery_id": env.DeliveryID,
		"slice_id":    sliceID,
	})
}

type pulsePayload struct {
	Status      string `json:"status"`
	State       string `json:"state"`
	AlertID     string `json:"alert_id"`
	ID          string `json:"id"`
	Fingerprint string `json:"fingerprint"`
	Key         string `json:"key"`
	Alert       string `json:"alert"`
	Title       string `json:"title"`
	Message     string `json:"message"`
	Severity    string `json:"severity"`
	Repo        string `json:"repo"`
}

func (s *server) handlePulseEvent(w http.ResponseWriter, r *http.Request, env UntrustedEnvelope) {
	var payload pulsePayload
	if err := json.Unmarshal(env.RawPayload, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid pulse payload"})
		return
	}
	if !isPulseFiring(payload) {
		writeJSON(w, http.StatusOK, map[string]string{
			"status":      "ignored",
			"delivery_id": env.DeliveryID,
		})
		return
	}

	sliceID := pulseAlertSliceID(payload, env.DeliveryID)
	rawTitle := firstNonEmpty(payload.Title, payload.Alert, payload.Message, "Pulse alert")
	title := SanitizeTitle("[alert] " + SanitizeString(rawTitle, maxTitleRunes))
	repo := SanitizeString(payload.Repo, 160)
	if repo == "" {
		repo = defaultPulseRepo
	}

	err := s.store.CreateSlice(r.Context(), domain.Slice{
		ID:    sliceID,
		Repo:  repo,
		Title: title,
		State: domain.StateSpec,
		Risk:  mapPulseRisk(payload.Severity),
	})
	if err != nil {
		if errors.Is(err, domain.ErrAlreadyExists) {
			writeJSON(w, http.StatusOK, map[string]string{
				"status":      "ignored",
				"delivery_id": env.DeliveryID,
				"slice_id":    sliceID,
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "create alert slice failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"status":      "created",
		"delivery_id": env.DeliveryID,
		"slice_id":    sliceID,
	})
}

func isPulseFiring(p pulsePayload) bool {
	state := strings.ToLower(firstNonEmpty(p.Status, p.State))
	switch state {
	case "resolved", "ok", "normal", "recovered", "closed":
		return false
	case "firing", "alerting":
		return true
	case "":
		return strings.TrimSpace(firstNonEmpty(p.Alert, p.Message, p.Title)) != ""
	default:
		return false
	}
}

func pulseAlertSliceID(p pulsePayload, deliveryID string) string {
	key := firstNonEmpty(p.AlertID, p.ID, p.Fingerprint, p.Key, deliveryID)
	sum := sha256.Sum256([]byte(key))
	return pulseIDPrefix + hex.EncodeToString(sum[:])[:pulseIDHexLen]
}

func mapPulseRisk(severity string) domain.RiskLevel {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case "critical":
		return domain.RiskHigh
	case "warning":
		return domain.RiskMedium
	default:
		return domain.RiskLow
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
