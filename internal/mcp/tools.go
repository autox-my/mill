package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/autox-my/mill/internal/domain"
)

type toolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	call        func(ctx context.Context, raw json.RawMessage) (any, error)
}

type callToolParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type textContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type callToolResult struct {
	Content []textContent `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

func (s *Server) buildTools() []toolDef {
	return []toolDef{
		{
			Name:        "list_slices",
			Description: "List work slices on the mill board, optionally filtered by state.",
			InputSchema: objectSchema(map[string]any{
				"state": stringProp("Optional slice state filter"),
			}, nil),
			call: s.callListSlices,
		},
		{
			Name:        "claim_slice",
			Description: "Atomically acquire or renew a lease on a slice.",
			InputSchema: objectSchema(map[string]any{
				"slice_id": stringProp("Slice identifier"),
				"owner":    stringProp("Lease owner identifier"),
				"ttl_sec":  intProp("Lease TTL in seconds (defaults to 900)"),
			}, []string{"slice_id", "owner"}),
			call: s.callClaimSlice,
		},
		{
			Name:        "renew_lease",
			Description: "Extend an active lease held by owner.",
			InputSchema: objectSchema(map[string]any{
				"slice_id": stringProp("Slice identifier"),
				"owner":    stringProp("Lease owner identifier"),
				"ttl_sec":  intProp("Lease TTL in seconds (defaults to 900)"),
			}, []string{"slice_id", "owner"}),
			call: s.callRenewLease,
		},
		{
			Name:        "update_slice",
			Description: "Transition a slice to a new lifecycle state and optionally record a verdict.",
			InputSchema: objectSchema(map[string]any{
				"slice_id": stringProp("Slice identifier"),
				"state":    stringProp("Target slice state"),
				"verdict":  stringProp("Optional verdict (pending, GREEN, RED, UNDECIDABLE)"),
			}, []string{"slice_id", "state"}),
			call: s.callUpdateSlice,
		},
		{
			Name:        "record_run",
			Description: "Append a run ledger event for an existing slice.",
			InputSchema: objectSchema(map[string]any{
				"slice_id":           stringProp("Slice identifier"),
				"harness":            stringProp("Harness name"),
				"model":              stringProp("Optional model identifier"),
				"phase":              stringProp("Execution phase"),
				"verdict":            stringProp("Run verdict (pending, GREEN, RED, UNDECIDABLE)"),
				"duration_sec":       intProp("Run duration in seconds"),
				"amendments":         intProp("Number of amendments"),
				"footprint_findings": intProp("Number of footprint findings"),
				"req_diff_direction": stringProp("Optional req diff direction"),
				"notes":              stringProp("Optional run notes"),
			}, []string{"slice_id", "harness", "phase", "verdict"}),
			call: s.callRecordRun,
		},
	}
}

func (s *Server) handleToolsCall(ctx context.Context, raw json.RawMessage) (any, *rpcError) {
	var params callToolParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: "invalid tools/call params: " + err.Error()}
	}
	if strings.TrimSpace(params.Name) == "" {
		return nil, &rpcError{Code: codeInvalidParams, Message: "tool name is required"}
	}
	def, ok := s.toolMap[params.Name]
	if !ok {
		return nil, &rpcError{Code: codeInvalidParams, Message: fmt.Sprintf("unknown tool: %s", params.Name)}
	}
	out, err := def.call(ctx, params.Arguments)
	if err != nil {
		return mapToolError(err)
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return nil, &rpcError{Code: codeInternalError, Message: "encode tool output: " + err.Error()}
	}
	return callToolResult{
		Content: []textContent{{Type: "text", Text: string(encoded)}},
	}, nil
}

func mapToolError(err error) (any, *rpcError) {
	var rErr *rpcError
	if errors.As(err, &rErr) {
		return nil, rErr
	}
	if isDomainError(err) {
		return callToolResult{
			Content: []textContent{{Type: "text", Text: err.Error()}},
			IsError: true,
		}, nil
	}
	return nil, &rpcError{Code: codeInternalError, Message: err.Error()}
}

func isDomainError(err error) bool {
	return errors.Is(err, domain.ErrNotFound) ||
		errors.Is(err, domain.ErrAlreadyExists) ||
		errors.Is(err, domain.ErrAlreadyClaimed) ||
		errors.Is(err, domain.ErrNotLeaseOwner) ||
		errors.Is(err, domain.ErrInvalidTransition) ||
		errors.Is(err, domain.ErrInvalidInput)
}

func (s *Server) callListSlices(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		State string `json:"state"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	slices, err := s.store.ListSlices(ctx, args.State)
	if err != nil {
		return nil, err
	}
	if slices == nil {
		slices = []domain.Slice{}
	}
	return slices, nil
}

func (s *Server) callClaimSlice(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		SliceID string `json:"slice_id"`
		Owner   string `json:"owner"`
		TTLSec  *int   `json:"ttl_sec"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	if err := requireNonEmpty("slice_id", args.SliceID); err != nil {
		return nil, err
	}
	if err := requireNonEmpty("owner", args.Owner); err != nil {
		return nil, err
	}
	return s.store.ClaimSlice(ctx, args.SliceID, args.Owner, resolveTTL(args.TTLSec), time.Now().UTC())
}

func (s *Server) callRenewLease(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		SliceID string `json:"slice_id"`
		Owner   string `json:"owner"`
		TTLSec  *int   `json:"ttl_sec"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	if err := requireNonEmpty("slice_id", args.SliceID); err != nil {
		return nil, err
	}
	if err := requireNonEmpty("owner", args.Owner); err != nil {
		return nil, err
	}
	return s.store.RenewLease(ctx, args.SliceID, args.Owner, resolveTTL(args.TTLSec), time.Now().UTC())
}

func (s *Server) callUpdateSlice(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		SliceID string `json:"slice_id"`
		State   string `json:"state"`
		Verdict string `json:"verdict"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	if err := requireNonEmpty("slice_id", args.SliceID); err != nil {
		return nil, err
	}
	if err := requireNonEmpty("state", args.State); err != nil {
		return nil, err
	}
	return s.store.UpdateSliceState(ctx, args.SliceID, domain.SliceState(args.State), domain.Verdict(args.Verdict), time.Now().UTC())
}

func (s *Server) callRecordRun(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		SliceID           string `json:"slice_id"`
		Harness           string `json:"harness"`
		Model             string `json:"model"`
		Phase             string `json:"phase"`
		Verdict           string `json:"verdict"`
		DurationSec       int    `json:"duration_sec"`
		Amendments        int    `json:"amendments"`
		FootprintFindings int    `json:"footprint_findings"`
		ReqDiffDirection  string `json:"req_diff_direction"`
		Notes             string `json:"notes"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	if err := requireNonEmpty("slice_id", args.SliceID); err != nil {
		return nil, err
	}
	if err := requireNonEmpty("harness", args.Harness); err != nil {
		return nil, err
	}
	if err := requireNonEmpty("phase", args.Phase); err != nil {
		return nil, err
	}
	if err := requireNonEmpty("verdict", args.Verdict); err != nil {
		return nil, err
	}
	ev := domain.RunEvent{
		SliceID:           args.SliceID,
		Harness:           args.Harness,
		Model:             args.Model,
		Phase:             args.Phase,
		Verdict:           domain.Verdict(args.Verdict),
		DurationSec:       args.DurationSec,
		Amendments:        args.Amendments,
		FootprintFindings: args.FootprintFindings,
		ReqDiffDirection:  args.ReqDiffDirection,
		Notes:             args.Notes,
	}
	return s.store.RecordRun(ctx, ev)
}

func decodeArgs(raw json.RawMessage, dst any) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	if err := json.Unmarshal(trimmed, dst); err != nil {
		return &rpcError{Code: codeInvalidParams, Message: "invalid tool arguments: " + err.Error()}
	}
	return nil
}

func requireNonEmpty(field, val string) error {
	if strings.TrimSpace(val) == "" {
		return &rpcError{Code: codeInvalidParams, Message: fmt.Sprintf("missing required argument: %s", field)}
	}
	return nil
}

func resolveTTL(ttlSec *int) time.Duration {
	if ttlSec == nil {
		return defaultLeaseTTL
	}
	return time.Duration(*ttlSec) * time.Second
}

func objectSchema(props map[string]any, required []string) map[string]any {
	s := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func stringProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func intProp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}
