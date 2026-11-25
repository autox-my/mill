// Package mcp implements a stdio JSON-RPC 2.0 Model Context Protocol server for mill.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/autox-my/mill/internal/domain"
)

const (
	protocolVersion = "2024-11-05"
	serverName      = "mill"
	serverVersion   = "0.1.0"
	defaultLeaseTTL = 15 * time.Minute
	maxScanToken    = 4 * 1024 * 1024

	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
)

// Store defines the narrow persistence contract required by the MCP server.
type Store interface {
	CreateSlice(ctx context.Context, sl domain.Slice) error
	GetSlice(ctx context.Context, id string) (domain.Slice, error)
	ListSlices(ctx context.Context, stateFilter string) ([]domain.Slice, error)
	ClaimSlice(ctx context.Context, id, owner string, ttl time.Duration, now time.Time) (domain.Slice, error)
	RenewLease(ctx context.Context, id, owner string, ttl time.Duration, now time.Time) (domain.Slice, error)
	UpdateSliceState(ctx context.Context, id string, next domain.SliceState, verdict domain.Verdict, risk domain.RiskLevel, now time.Time) (domain.Slice, error)
	RecordRun(ctx context.Context, ev domain.RunEvent) (domain.RunEvent, error)
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string {
	return e.Message
}

// Server serves MCP JSON-RPC 2.0 requests over newline-delimited stdio streams.
type Server struct {
	store   Store
	tools   []toolDef
	toolMap map[string]toolDef
}

// NewServer constructs an MCP Server backed by store.
func NewServer(store Store) *Server {
	s := &Server{store: store}
	s.tools = s.buildTools()
	s.toolMap = make(map[string]toolDef, len(s.tools))
	for _, t := range s.tools {
		s.toolMap[t.Name] = t
	}
	return s
}

// Serve reads newline-delimited JSON-RPC 2.0 requests from r and writes responses to w.
func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	if s.store == nil {
		return errors.New("mcp server store is nil")
	}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), maxScanToken)

	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		resp := s.handleLine(ctx, line)
		if resp == nil {
			continue
		}
		if err := writeResponse(w, resp); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read mcp stream: %w", err)
	}
	return nil
}

func (s *Server) handleLine(ctx context.Context, line []byte) *rpcResponse {
	var req rpcRequest
	if err := json.Unmarshal(line, &req); err != nil {
		return errorResponse(nil, codeParseError, "parse error: "+err.Error())
	}
	if req.JSONRPC != "2.0" || strings.TrimSpace(req.Method) == "" {
		return errorResponse(req.ID, codeInvalidRequest, "invalid JSON-RPC 2.0 request")
	}
	isNotification := len(bytes.TrimSpace(req.ID)) == 0
	if req.Method == "notifications/initialized" && isNotification {
		return nil
	}
	res, rpcErr := s.dispatch(ctx, req.Method, req.Params)
	if isNotification {
		return nil
	}
	if rpcErr != nil {
		return errorResponse(req.ID, rpcErr.Code, rpcErr.Message)
	}
	return &rpcResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  res,
	}
}

func (s *Server) dispatch(ctx context.Context, method string, params json.RawMessage) (any, *rpcError) {
	switch method {
	case "initialize":
		return map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities": map[string]any{
				"tools": map[string]any{},
			},
			"serverInfo": map[string]any{
				"name":    serverName,
				"version": serverVersion,
			},
		}, nil
	case "notifications/initialized":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": s.tools}, nil
	case "tools/call":
		return s.handleToolsCall(ctx, params)
	default:
		return nil, &rpcError{Code: codeMethodNotFound, Message: fmt.Sprintf("method not found: %s", method)}
	}
}

func errorResponse(id json.RawMessage, code int, msg string) *rpcResponse {
	if len(bytes.TrimSpace(id)) == 0 {
		id = json.RawMessage("null")
	}
	return &rpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &rpcError{Code: code, Message: msg},
	}
}

func writeResponse(w io.Writer, resp *rpcResponse) error {
	b, err := json.Marshal(resp)
	if err != nil {
		return fmt.Errorf("marshal mcp response: %w", err)
	}
	b = append(b, '\n')
	if _, err := w.Write(b); err != nil {
		return fmt.Errorf("write mcp response: %w", err)
	}
	return nil
}
