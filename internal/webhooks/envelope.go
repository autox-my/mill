package webhooks

import (
	"encoding/json"
	"strings"
	"time"
)

const maxTitleRunes = 160

// UntrustedEnvelope wraps raw external webhook payloads with provenance metadata
// before any handler inspects or sanitizes fields.
type UntrustedEnvelope struct {
	Source     string          `json:"source"`
	DeliveryID string          `json:"delivery_id"`
	EventType  string          `json:"event_type"`
	ReceivedAt time.Time       `json:"received_at"`
	Untrusted  bool            `json:"untrusted"`
	RawPayload json.RawMessage `json:"raw_payload"`
}

// WrapUntrusted constructs an UntrustedEnvelope for a verified webhook payload.
func WrapUntrusted(source, deliveryID, eventType string, raw []byte, receivedAt time.Time) UntrustedEnvelope {
	if receivedAt.IsZero() {
		receivedAt = time.Now().UTC()
	} else {
		receivedAt = receivedAt.UTC()
	}
	cp := make([]byte, len(raw))
	copy(cp, raw)
	return UntrustedEnvelope{
		Source:     source,
		DeliveryID: deliveryID,
		EventType:  eventType,
		ReceivedAt: receivedAt,
		Untrusted:  true,
		RawPayload: json.RawMessage(cp),
	}
}

// SanitizeString strips ASCII control characters (< 0x20 and 0x7f), collapses
// whitespace into single spaces, and truncates to maxRunes when maxRunes > 0.
func SanitizeString(s string, maxRunes int) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteByte(' ')
		case r < 0x20 || r == 0x7f:
			continue
		default:
			b.WriteRune(r)
		}
	}
	collapsed := strings.Join(strings.Fields(b.String()), " ")
	if maxRunes <= 0 {
		return collapsed
	}
	runes := []rune(collapsed)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes])
	}
	return collapsed
}

// SanitizeTitle sanitizes an untrusted string and caps it at 160 runes.
func SanitizeTitle(s string) string {
	return SanitizeString(s, maxTitleRunes)
}
