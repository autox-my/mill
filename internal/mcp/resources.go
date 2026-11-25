package mcp

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/autox-my/mill/internal/domain"
	"github.com/autox-my/mill/internal/observer"
	"github.com/autox-my/mill/internal/webui"
)

const (
	boardResourceURI     = "ui://mill/board"
	mcpAppMimeType       = "text/html;profile=mcp-app"
	codeResourceNotFound = -32002
	boardRunLimit        = 100
)

type snapshotStore interface {
	ListRuns(ctx context.Context, sliceID string, limit int) ([]domain.RunEvent, error)
	Stats(ctx context.Context) (domain.StatsSummary, error)
}

type resourceDef struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description"`
	MimeType    string `json:"mimeType"`
}

type resourceContent struct {
	URI      string `json:"uri"`
	MimeType string `json:"mimeType"`
	Text     string `json:"text"`
}

type boardBootstrap struct {
	Stats  domain.StatsSummary `json:"stats"`
	Slices []domain.Slice      `json:"slices"`
	Runs   []domain.RunEvent   `json:"runs"`
	Report observer.Report     `json:"report"`
}

func (s *Server) handleResourcesList() map[string]any {
	return map[string]any{
		"resources": []resourceDef{
			{
				URI:         boardResourceURI,
				Name:        "mill Board",
				Description: "Interactive read-only mill Slice Board, Run Ledger, and Observer Hotspots UI",
				MimeType:    mcpAppMimeType,
			},
		},
	}
}

func (s *Server) handleResourcesRead(ctx context.Context, raw json.RawMessage) (any, *rpcError) {
	var params struct {
		URI string `json:"uri"`
	}
	if len(raw) == 0 {
		return nil, &rpcError{Code: codeInvalidParams, Message: "resource uri is required"}
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: "invalid resources/read params: " + err.Error()}
	}
	if strings.TrimSpace(params.URI) == "" {
		return nil, &rpcError{Code: codeInvalidParams, Message: "resource uri is required"}
	}
	if params.URI != boardResourceURI {
		return nil, &rpcError{Code: codeResourceNotFound, Message: "Resource not found"}
	}

	bootstrap, err := s.buildBoardBootstrap(ctx)
	if err != nil {
		return nil, &rpcError{Code: codeInternalError, Message: err.Error()}
	}
	html, err := webui.BoardAppHTML(bootstrap)
	if err != nil {
		return nil, &rpcError{Code: codeInternalError, Message: err.Error()}
	}
	return map[string]any{
		"contents": []resourceContent{
			{
				URI:      boardResourceURI,
				MimeType: mcpAppMimeType,
				Text:     html,
			},
		},
	}, nil
}

func (s *Server) buildBoardBootstrap(ctx context.Context) (boardBootstrap, error) {
	slices, err := s.store.ListSlices(ctx, "")
	if err != nil {
		return boardBootstrap{}, err
	}
	if slices == nil {
		slices = []domain.Slice{}
	}

	runs := []domain.RunEvent{}
	stats := summarizeSlices(slices)

	if ss, ok := s.store.(snapshotStore); ok {
		loadedRuns, err := ss.ListRuns(ctx, "", boardRunLimit)
		if err != nil {
			return boardBootstrap{}, err
		}
		if loadedRuns != nil {
			runs = loadedRuns
		}
		stats, err = ss.Stats(ctx)
		if err != nil {
			return boardBootstrap{}, err
		}
	}

	return boardBootstrap{
		Stats:  stats,
		Slices: slices,
		Runs:   runs,
		Report: observer.AnalyzeWithSlices(runs, slices),
	}, nil
}

func summarizeSlices(slices []domain.Slice) domain.StatsSummary {
	byState := make(map[domain.SliceState]int)
	for _, sl := range slices {
		byState[sl.State]++
	}
	return domain.StatsSummary{
		TotalSlices: len(slices),
		ByState:     byState,
	}
}
