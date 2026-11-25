package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/autox-my/mill/internal/domain"
	"github.com/autox-my/mill/internal/mcp"
	"github.com/autox-my/mill/internal/store"
)

type rpcResp struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcErr         `json:"error,omitempty"`
}

type rpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type callToolResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open memory store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func seedSlice(t *testing.T, st *store.Store, id string) {
	t.Helper()
	err := st.CreateSlice(context.Background(), domain.Slice{
		ID:     id,
		Repo:   "github.com/autox-my/mill",
		Title:  "Test slice " + id,
		Claims: []string{"REQ-0006"},
	})
	if err != nil {
		t.Fatalf("seed slice %s: %v", id, err)
	}
}

func runRPC(t *testing.T, srv *mcp.Server, lines ...string) []rpcResp {
	t.Helper()
	input := strings.Join(lines, "\n") + "\n"
	var out bytes.Buffer
	err := srv.Serve(context.Background(), strings.NewReader(input), &out)
	if err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}
	var resps []rpcResp
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var r rpcResp
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("unmarshal response %q: %v", line, err)
		}
		resps = append(resps, r)
	}
	return resps
}

func decodeToolResult(t *testing.T, resp rpcResp) callToolResult {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("unexpected RPC error: %+v", resp.Error)
	}
	var res callToolResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatalf("unmarshal tool result: %v", err)
	}
	return res
}

func TestMCP_LifecycleAndToolsList(t *testing.T) {
	st := openTestStore(t)
	srv := mcp.NewServer(st)

	resps := runRPC(t, srv,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	)
	if len(resps) != 2 {
		t.Fatalf("expected 2 responses (notification must not reply), got %d", len(resps))
	}

	var initRes struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name string `json:"name"`
		} `json:"serverInfo"`
	}
	if err := json.Unmarshal(resps[0].Result, &initRes); err != nil {
		t.Fatalf("unmarshal initialize: %v", err)
	}
	if initRes.ProtocolVersion == "" || initRes.ServerInfo.Name != "mill" {
		t.Fatalf("unexpected initialize result: %+v", initRes)
	}

	var listRes struct {
		Tools []struct {
			Name        string         `json:"name"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(resps[1].Result, &listRes); err != nil {
		t.Fatalf("unmarshal tools/list: %v", err)
	}
	if len(listRes.Tools) != 6 {
		t.Fatalf("expected 6 tools, got %d", len(listRes.Tools))
	}
	want := map[string]bool{
		"create_slice": false,
		"list_slices":  false,
		"claim_slice":  false,
		"renew_lease":  false,
		"update_slice": false,
		"record_run":   false,
	}
	for _, tool := range listRes.Tools {
		if _, ok := want[tool.Name]; !ok {
			t.Fatalf("unexpected tool %q", tool.Name)
		}
		want[tool.Name] = true
		if tool.InputSchema == nil {
			t.Fatalf("tool %q missing inputSchema", tool.Name)
		}
	}
	for name, seen := range want {
		if !seen {
			t.Fatalf("missing tool %q", name)
		}
	}
}

func TestMCP_SliceWorkflowAndDomainErrors(t *testing.T) {
	st := openTestStore(t)
	seedSlice(t, st, "S-1")
	srv := mcp.NewServer(st)

	resps := runRPC(t, srv,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_slices","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"claim_slice","arguments":{"slice_id":"S-1","owner":"agent-a","ttl_sec":60}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"claim_slice","arguments":{"slice_id":"S-1","owner":"agent-b","ttl_sec":60}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"renew_lease","arguments":{"slice_id":"S-1","owner":"agent-a","ttl_sec":120}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"renew_lease","arguments":{"slice_id":"S-1","owner":"agent-b","ttl_sec":120}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"update_slice","arguments":{"slice_id":"S-1","state":"red","verdict":"RED"}}}`,
		`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"update_slice","arguments":{"slice_id":"S-1","state":"merged"}}}`,
		`{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"record_run","arguments":{"slice_id":"S-1","harness":"claude","phase":"red","verdict":"RED","duration_sec":12,"notes":"failing test"}}}`,
		`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"list_slices","arguments":{"state":"red"}}}`,
	)
	if len(resps) != 9 {
		t.Fatalf("expected 9 responses, got %d", len(resps))
	}

	// 1: list_slices returns 1 slice
	r1 := decodeToolResult(t, resps[0])
	if r1.IsError || !strings.Contains(r1.Content[0].Text, `"S-1"`) {
		t.Fatalf("unexpected list_slices result: %+v", r1)
	}

	// 2: claim_slice by agent-a succeeds
	r2 := decodeToolResult(t, resps[1])
	if r2.IsError || !strings.Contains(r2.Content[0].Text, `"claimed"`) {
		t.Fatalf("expected claimed slice, got %+v", r2)
	}

	// 3: claim_slice by agent-b fails with isError: true
	r3 := decodeToolResult(t, resps[2])
	if !r3.IsError {
		t.Fatalf("expected isError: true for competing claim, got %+v", r3)
	}

	// 4: renew_lease by agent-a succeeds
	r4 := decodeToolResult(t, resps[3])
	if r4.IsError {
		t.Fatalf("expected renew_lease success, got %+v", r4)
	}

	// 5: renew_lease by agent-b fails with isError: true
	r5 := decodeToolResult(t, resps[4])
	if !r5.IsError {
		t.Fatalf("expected isError: true for non-owner renew, got %+v", r5)
	}

	// 6: update_slice to red succeeds
	r6 := decodeToolResult(t, resps[5])
	if r6.IsError || !strings.Contains(r6.Content[0].Text, `"RED"`) {
		t.Fatalf("expected update_slice success, got %+v", r6)
	}

	// 7: invalid transition red -> merged returns isError: true
	r7 := decodeToolResult(t, resps[6])
	if !r7.IsError {
		t.Fatalf("expected isError: true for invalid transition, got %+v", r7)
	}

	// 8: record_run succeeds
	r8 := decodeToolResult(t, resps[7])
	if r8.IsError || !strings.Contains(r8.Content[0].Text, `"claude"`) {
		t.Fatalf("expected record_run success, got %+v", r8)
	}

	// 9: list_slices filtered by state=red returns S-1
	r9 := decodeToolResult(t, resps[8])
	if r9.IsError || !strings.Contains(r9.Content[0].Text, `"S-1"`) {
		t.Fatalf("expected filtered list_slices to contain S-1, got %+v", r9)
	}
}

func TestMCP_JSONRPCErrorCodes(t *testing.T) {
	st := openTestStore(t)
	srv := mcp.NewServer(st)

	resps := runRPC(t, srv,
		`{not-valid-json`,
		`{"jsonrpc":"1.0","id":1,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":2,"method":""}`,
		`{"jsonrpc":"2.0","id":3,"method":"unknown/method"}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":"bad"}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"unknown_tool","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"claim_slice","arguments":"bad"}}`,
		`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"claim_slice","arguments":{"slice_id":"","owner":"a"}}}`,
	)

	wantCodes := []int{-32700, -32600, -32600, -32601, -32602, -32602, -32602, -32602}
	if len(resps) != len(wantCodes) {
		t.Fatalf("expected %d responses, got %d", len(wantCodes), len(resps))
	}
	for i, code := range wantCodes {
		if resps[i].Error == nil || resps[i].Error.Code != code {
			t.Fatalf("case %d: expected error code %d, got %+v", i, code, resps[i].Error)
		}
	}
}

type failingStore struct{}

func (failingStore) CreateSlice(context.Context, domain.Slice) error {
	return errors.New("db connection lost")
}
func (failingStore) GetSlice(context.Context, string) (domain.Slice, error) {
	return domain.Slice{}, errors.New("db connection lost")
}
func (failingStore) ListSlices(context.Context, string) ([]domain.Slice, error) {
	return nil, errors.New("db connection lost")
}
func (failingStore) ClaimSlice(context.Context, string, string, time.Duration, time.Time) (domain.Slice, error) {
	return domain.Slice{}, errors.New("db connection lost")
}
func (failingStore) RenewLease(context.Context, string, string, time.Duration, time.Time) (domain.Slice, error) {
	return domain.Slice{}, errors.New("db connection lost")
}
func (failingStore) UpdateSliceState(context.Context, string, domain.SliceState, domain.Verdict, domain.RiskLevel, time.Time) (domain.Slice, error) {
	return domain.Slice{}, errors.New("db connection lost")
}
func (failingStore) RecordRun(context.Context, domain.RunEvent) (domain.RunEvent, error) {
	return domain.RunEvent{}, errors.New("db connection lost")
}

func TestMCP_InternalError(t *testing.T) {
	srv := mcp.NewServer(failingStore{})
	resps := runRPC(t, srv, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_slices","arguments":{}}}`)
	if len(resps) != 1 || resps[0].Error == nil || resps[0].Error.Code != -32603 {
		t.Fatalf("expected -32603 internal error, got %+v", resps)
	}
}

func TestMCP_CreateSlice(t *testing.T) {
	st := openTestStore(t)
	srv := mcp.NewServer(st)

	resps := runRPC(t, srv,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"create_slice","arguments":{"slice_id":"S-99","repo":"autox-my/mill","title":"Created via MCP","claims":["REQ-0006"],"branch":"feature/s-99"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"create_slice","arguments":{"slice_id":"S-99","repo":"autox-my/mill","title":"Duplicate"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"create_slice","arguments":{"slice_id":"S-100","repo":"autox-my/mill"}}}`,
	)
	if len(resps) != 3 {
		t.Fatalf("expected 3 responses, got %d", len(resps))
	}

	r1 := decodeToolResult(t, resps[0])
	if r1.IsError || !strings.Contains(r1.Content[0].Text, `"id":"S-99"`) {
		t.Fatalf("expected created slice S-99, got %+v", r1)
	}
	r2 := decodeToolResult(t, resps[1])
	if !r2.IsError || !strings.Contains(r2.Content[0].Text, "already exists") {
		t.Fatalf("expected duplicate domain error, got %+v", r2)
	}
	if resps[2].Error == nil || resps[2].Error.Code != -32602 {
		t.Fatalf("expected -32602 for missing title, got %+v", resps[2].Error)
	}
}

func TestMCP_UpdateSliceRiskAndFloor(t *testing.T) {
	st := openTestStore(t)
	seedSlice(t, st, "S-RISK")
	srv := mcp.NewServer(st)

	resps := runRPC(t, srv,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"update_slice","arguments":{"slice_id":"S-RISK","risk":"medium"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"update_slice","arguments":{"slice_id":"S-RISK"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"record_run","arguments":{"slice_id":"S-RISK","harness":"claude","phase":"verify","verdict":"GREEN","footprint_findings":1}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"update_slice","arguments":{"slice_id":"S-RISK","risk":"low"}}}`,
	)
	if len(resps) != 4 {
		t.Fatalf("expected 4 responses, got %d", len(resps))
	}
	r1 := decodeToolResult(t, resps[0])
	if r1.IsError || !strings.Contains(r1.Content[0].Text, `"risk":"medium"`) {
		t.Fatalf("expected risk=medium, got %+v", r1)
	}
	if resps[1].Error == nil || resps[1].Error.Code != -32602 {
		t.Fatalf("expected -32602 when both state and risk are missing, got %+v", resps[1].Error)
	}
	r3 := decodeToolResult(t, resps[2])
	if r3.IsError {
		t.Fatalf("expected record_run success, got %+v", r3)
	}
	r4 := decodeToolResult(t, resps[3])
	if r4.IsError || !strings.Contains(r4.Content[0].Text, `"risk":"high"`) {
		t.Fatalf("expected clamped risk=high, got %+v", r4)
	}
}

func TestMCP_AppResourceBoard(t *testing.T) {
	st := openTestStore(t)
	seedSlice(t, st, "S-APP-1")
	_, err := st.RecordRun(context.Background(), domain.RunEvent{
		SliceID: "S-APP-1",
		Harness: "claude",
		Phase:   "verify",
		Verdict: domain.VerdictGreen,
	})
	if err != nil {
		t.Fatalf("record run: %v", err)
	}
	srv := mcp.NewServer(st)

	resps := runRPC(t, srv,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"resources/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"resources/read","params":{"uri":"ui://mill/board"}}`,
		`{"jsonrpc":"2.0","id":4,"method":"resources/read","params":{"uri":"ui://mill/unknown"}}`,
		`{"jsonrpc":"2.0","id":5,"method":"resources/read","params":{}}`,
		`{"jsonrpc":"2.0","id":6,"method":"resources/read","params":"bad"}`,
	)
	if len(resps) != 6 {
		t.Fatalf("expected 6 responses, got %d", len(resps))
	}

	var initRes struct {
		Capabilities map[string]any `json:"capabilities"`
	}
	if err := json.Unmarshal(resps[0].Result, &initRes); err != nil {
		t.Fatalf("unmarshal initialize: %v", err)
	}
	if _, ok := initRes.Capabilities["resources"]; !ok {
		t.Fatalf("expected resources capability in initialize, got %+v", initRes.Capabilities)
	}

	var listRes struct {
		Resources []struct {
			URI      string `json:"uri"`
			Name     string `json:"name"`
			MimeType string `json:"mimeType"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(resps[1].Result, &listRes); err != nil {
		t.Fatalf("unmarshal resources/list: %v", err)
	}
	if len(listRes.Resources) != 1 || listRes.Resources[0].URI != "ui://mill/board" || listRes.Resources[0].MimeType != "text/html;profile=mcp-app" {
		t.Fatalf("unexpected resources/list: %+v", listRes)
	}

	if resps[2].Error != nil {
		t.Fatalf("unexpected error on resources/read: %+v", resps[2].Error)
	}
	var readRes struct {
		Contents []struct {
			URI      string `json:"uri"`
			MimeType string `json:"mimeType"`
			Text     string `json:"text"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(resps[2].Result, &readRes); err != nil {
		t.Fatalf("unmarshal resources/read: %v", err)
	}
	if len(readRes.Contents) != 1 || readRes.Contents[0].URI != "ui://mill/board" || readRes.Contents[0].MimeType != "text/html;profile=mcp-app" {
		t.Fatalf("unexpected resources/read contents: %+v", readRes)
	}
	html := readRes.Contents[0].Text
	if !strings.Contains(html, "window.__MILL_BOOTSTRAP__") || !strings.Contains(html, "S-APP-1") {
		t.Errorf("expected self-contained HTML with bootstrapped slice S-APP-1")
	}

	if resps[3].Error == nil || resps[3].Error.Code != -32002 {
		t.Fatalf("expected -32002 for unknown resource, got %+v", resps[3].Error)
	}
	if resps[4].Error == nil || resps[4].Error.Code != -32602 {
		t.Fatalf("expected -32602 for empty uri, got %+v", resps[4].Error)
	}
	if resps[5].Error == nil || resps[5].Error.Code != -32602 {
		t.Fatalf("expected -32602 for invalid params, got %+v", resps[5].Error)
	}
}
