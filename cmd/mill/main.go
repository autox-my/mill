// Command mill is the CLI for the headless Slice Board and Run Ledger.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/autox-my/mill/internal/domain"
	"github.com/autox-my/mill/internal/httpapi"
	"github.com/autox-my/mill/internal/httpclient"
	"github.com/autox-my/mill/internal/ingest"
	"github.com/autox-my/mill/internal/mcp"
	"github.com/autox-my/mill/internal/observer"
	"github.com/autox-my/mill/internal/store"
)

const (
	defaultDBPath   = ".mill/mill.db"
	defaultHTTPAddr = "127.0.0.1:8080"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	return runWithIO(args, os.Stdin, stdout, stderr)
}

type commandFunc func(dbPath string, args []string, stdin io.Reader, stdout, stderr io.Writer) int

func commandTable() map[string]commandFunc {
	return map[string]commandFunc{
		"init": func(dbPath string, _ []string, _ io.Reader, stdout, stderr io.Writer) int {
			return cmdInit(dbPath, stdout, stderr)
		},
		"slice": func(dbPath string, args []string, _ io.Reader, stdout, stderr io.Writer) int {
			return cmdSlice(dbPath, args, stdout, stderr)
		},
		"run": func(dbPath string, args []string, _ io.Reader, stdout, stderr io.Writer) int {
			return cmdRun(dbPath, args, stdout, stderr)
		},
		"stats": func(dbPath string, args []string, _ io.Reader, stdout, stderr io.Writer) int {
			return cmdStats(dbPath, args, stdout, stderr)
		},
		"mcp":     cmdMCP,
		"serve":   cmdServe,
		"ingest":  cmdIngest,
		"improve": cmdImprove,
	}
}

func runWithIO(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	dbPath, rest, err := extractDBFlag(args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if len(rest) == 0 {
		printUsage(stderr)
		return 2
	}
	if rest[0] == "-h" || rest[0] == "--help" || rest[0] == "help" {
		printUsage(stdout)
		return 0
	}
	fn, ok := commandTable()[rest[0]]
	if !ok {
		fmt.Fprintf(stderr, "unknown command %q\n", rest[0])
		printUsage(stderr)
		return 2
	}
	return fn(dbPath, rest[1:], stdin, stdout, stderr)
}

func extractDBFlag(args []string) (string, []string, error) {
	dbPath := os.Getenv("MILL_DB")
	if dbPath == "" {
		dbPath = defaultDBPath
	}
	var rest []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--db" {
			if i+1 >= len(args) {
				return "", nil, fmt.Errorf("--db requires a value")
			}
			dbPath = args[i+1]
			i++
			continue
		}
		if strings.HasPrefix(args[i], "--db=") {
			dbPath = strings.TrimPrefix(args[i], "--db=")
			continue
		}
		rest = append(rest, args[i])
	}
	return dbPath, rest, nil
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: mill [--db <path>] <init|slice|run|stats|mcp|serve|ingest|improve>")
}

type millStore interface {
	httpapi.Store
	Close() error
}

func openLocalStore(dbPath string, stderr io.Writer) (*store.Store, bool) {
	st, err := store.Open(dbPath)
	if err != nil {
		fmt.Fprintf(stderr, "open store: %v\n", err)
		return nil, false
	}
	return st, true
}

func openStore(dbPath string, stderr io.Writer) (millStore, bool) {
	if baseURL := strings.TrimSpace(os.Getenv("MILL_URL")); baseURL != "" {
		c, err := httpclient.New(baseURL, os.Getenv("MILL_API_TOKEN"))
		if err != nil {
			fmt.Fprintf(stderr, "open remote store: %v\n", err)
			return nil, false
		}
		return c, true
	}
	return openLocalStore(dbPath, stderr)
}

func cmdInit(dbPath string, stdout, stderr io.Writer) int {
	st, ok := openLocalStore(dbPath, stderr)
	if !ok {
		return 1
	}
	_ = st.Close()
	fmt.Fprintf(stdout, "initialized mill store at %s\n", dbPath)
	return 0
}

func cmdSlice(dbPath string, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: mill slice <create|list|claim|renew|update> [flags]")
		return 2
	}
	switch args[0] {
	case "create":
		return cmdSliceCreate(dbPath, args[1:], stdout, stderr)
	case "list":
		return cmdSliceList(dbPath, args[1:], stdout, stderr)
	case "claim":
		return cmdSliceLease(dbPath, args[1:], true, stdout, stderr)
	case "renew":
		return cmdSliceLease(dbPath, args[1:], false, stdout, stderr)
	case "update":
		return cmdSliceUpdate(dbPath, args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown slice subcommand %q\n", args[0])
		return 2
	}
}

func cmdSliceCreate(dbPath string, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("slice create", stderr)
	id := fs.String("id", "", "slice id")
	repo := fs.String("repo", "", "repository")
	title := fs.String("title", "", "slice title")
	claimsCSV := fs.String("claims", "", "comma-separated claim IDs")
	branch := fs.String("branch", "", "git branch")
	lockPath := fs.String("lock", "", "intent lock path")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	st, ok := openStore(dbPath, stderr)
	if !ok {
		return 1
	}
	defer st.Close()

	sl := domain.Slice{
		ID:         *id,
		Repo:       *repo,
		Title:      *title,
		Claims:     splitCSV(*claimsCSV),
		Branch:     *branch,
		IntentLock: *lockPath,
	}
	if err := st.CreateSlice(context.Background(), sl); err != nil {
		fmt.Fprintf(stderr, "slice create: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "created slice %s\n", sl.ID)
	return 0
}

func cmdSliceList(dbPath string, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("slice list", stderr)
	state := fs.String("state", "", "filter by state")
	asJSON := fs.Bool("json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	st, ok := openStore(dbPath, stderr)
	if !ok {
		return 1
	}
	defer st.Close()

	slices, err := st.ListSlices(context.Background(), *state)
	if err != nil {
		fmt.Fprintf(stderr, "slice list: %v\n", err)
		return 1
	}
	if *asJSON {
		return writeJSON(stdout, stderr, slices)
	}
	for _, sl := range slices {
		fmt.Fprintf(stdout, "%s\t%s\t%s\towner=%s\tverdict=%s\t%s\n",
			sl.ID, sl.State, sl.Repo, sl.Owner, sl.LastVerdict, sl.Title)
	}
	return 0
}

func cmdSliceLease(dbPath string, args []string, isClaim bool, stdout, stderr io.Writer) int {
	name := "slice renew"
	if isClaim {
		name = "slice claim"
	}
	fs := newFlagSet(name, stderr)
	id := fs.String("id", "", "slice id")
	owner := fs.String("owner", "", "owner id")
	ttl := fs.Duration("ttl", 30*time.Minute, "lease duration")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	st, ok := openStore(dbPath, stderr)
	if !ok {
		return 1
	}
	defer st.Close()

	var (
		sl  domain.Slice
		err error
	)
	now := time.Now().UTC()
	if isClaim {
		sl, err = st.ClaimSlice(context.Background(), *id, *owner, *ttl, now)
	} else {
		sl, err = st.RenewLease(context.Background(), *id, *owner, *ttl, now)
	}
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return 1
	}
	fmt.Fprintf(stdout, "%s\towner=%s\tattempt=%d\texpires=%s\n",
		sl.ID, sl.Owner, sl.Attempt, sl.LeaseExpiresAt.Format(time.RFC3339))
	return 0
}

func cmdSliceUpdate(dbPath string, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("slice update", stderr)
	id := fs.String("id", "", "slice id")
	state := fs.String("state", "", "next state")
	verdict := fs.String("verdict", "", "optional verdict")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	st, ok := openStore(dbPath, stderr)
	if !ok {
		return 1
	}
	defer st.Close()

	sl, err := st.UpdateSliceState(context.Background(), *id, domain.SliceState(*state), domain.Verdict(*verdict), time.Now().UTC())
	if err != nil {
		fmt.Fprintf(stderr, "slice update: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "%s\tstate=%s\tverdict=%s\n", sl.ID, sl.State, sl.LastVerdict)
	return 0
}

func cmdRun(dbPath string, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: mill run <record|list> [flags]")
		return 2
	}
	switch args[0] {
	case "record":
		return cmdRunRecord(dbPath, args[1:], stdout, stderr)
	case "list":
		return cmdRunList(dbPath, args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown run subcommand %q\n", args[0])
		return 2
	}
}

func cmdRunRecord(dbPath string, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("run record", stderr)
	sliceID := fs.String("slice", "", "slice id")
	harness := fs.String("harness", "", "harness name")
	model := fs.String("model", "", "model name")
	phase := fs.String("phase", "", "sdls phase")
	verdict := fs.String("verdict", "", "verdict")
	duration := fs.Int("duration", 0, "duration seconds")
	amendments := fs.Int("amendments", 0, "amendment count")
	findings := fs.Int("findings", 0, "footprint findings")
	reqDiff := fs.String("req-diff", "", "req diff direction")
	notes := fs.String("notes", "", "notes")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	st, ok := openStore(dbPath, stderr)
	if !ok {
		return 1
	}
	defer st.Close()

	ev, err := st.RecordRun(context.Background(), domain.RunEvent{
		SliceID:           *sliceID,
		Harness:           *harness,
		Model:             *model,
		Phase:             *phase,
		Verdict:           domain.Verdict(*verdict),
		DurationSec:       *duration,
		Amendments:        *amendments,
		FootprintFindings: *findings,
		ReqDiffDirection:  *reqDiff,
		Notes:             *notes,
	})
	if err != nil {
		fmt.Fprintf(stderr, "run record: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "recorded run #%d for %s (%s %s)\n", ev.ID, ev.SliceID, ev.Phase, ev.Verdict)
	return 0
}

func cmdRunList(dbPath string, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("run list", stderr)
	sliceID := fs.String("slice", "", "filter by slice id")
	limit := fs.Int("limit", 20, "max rows")
	asJSON := fs.Bool("json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	st, ok := openStore(dbPath, stderr)
	if !ok {
		return 1
	}
	defer st.Close()

	runs, err := st.ListRuns(context.Background(), *sliceID, *limit)
	if err != nil {
		fmt.Fprintf(stderr, "run list: %v\n", err)
		return 1
	}
	if *asJSON {
		return writeJSON(stdout, stderr, runs)
	}
	for _, r := range runs {
		fmt.Fprintf(stdout, "#%d\t%s\t%s\t%s\t%s\tamendments=%d\tfindings=%d\n",
			r.ID, r.SliceID, r.Harness, r.Phase, r.Verdict, r.Amendments, r.FootprintFindings)
	}
	return 0
}

func cmdStats(dbPath string, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("stats", stderr)
	asJSON := fs.Bool("json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	st, ok := openStore(dbPath, stderr)
	if !ok {
		return 1
	}
	defer st.Close()

	summary, err := st.Stats(context.Background())
	if err != nil {
		fmt.Fprintf(stderr, "stats: %v\n", err)
		return 1
	}
	if *asJSON {
		return writeJSON(stdout, stderr, summary)
	}
	fmt.Fprintf(stdout, "slices: %d | runs: %d (GREEN=%d, RED=%d) | amendments: %d | findings: %d\n",
		summary.TotalSlices, summary.TotalRuns, summary.GreenRuns, summary.RedRuns, summary.TotalAmendments, summary.TotalFindings)
	return 0
}

func cmdMCP(dbPath string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := newFlagSet("mcp", stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	st, ok := openStore(dbPath, stderr)
	if !ok {
		return 1
	}
	defer st.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := mcp.NewServer(st)
	if err := srv.Serve(ctx, stdin, stdout); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(stderr, "mcp: %v\n", err)
		return 1
	}
	return 0
}

func cmdServe(dbPath string, args []string, _ io.Reader, stdout, stderr io.Writer) int {
	fs := newFlagSet("serve", stderr)
	defaultAddr := strings.TrimSpace(os.Getenv("MILL_ADDR"))
	if defaultAddr == "" {
		defaultAddr = defaultHTTPAddr
	}
	addr := fs.String("addr", defaultAddr, "HTTP listen address")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	token := strings.TrimSpace(os.Getenv("MILL_API_TOKEN"))
	if token == "" {
		fmt.Fprintln(stderr, "serve: MILL_API_TOKEN is required")
		return 1
	}
	st, ok := openLocalStore(dbPath, stderr)
	if !ok {
		return 1
	}
	defer st.Close()

	srv := &http.Server{
		Addr:              *addr,
		Handler:           httpapi.NewHandler(st, token),
		ReadHeaderTimeout: 10 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	fmt.Fprintf(stdout, "listening on %s\n", *addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(stderr, "serve: %v\n", err)
		return 1
	}
	return 0
}

func cmdIngest(dbPath string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := newFlagSet("ingest", stderr)
	source := fs.String("source", "", "source format (intentgate|req-diff|check)")
	sliceID := fs.String("slice", "", "slice id")
	harness := fs.String("harness", "", "harness name")
	model := fs.String("model", "", "model name")
	phase := fs.String("phase", "verify", "sdls phase")
	duration := fs.Int("duration", 0, "duration seconds")
	filePath := fs.String("file", "", "optional input file path (defaults to stdin)")
	dryRun := fs.Bool("dry-run", false, "parse and print JSON without writing to store")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	raw, err := readIngestInput(*filePath, stdin)
	if err != nil {
		fmt.Fprintf(stderr, "ingest: %v\n", err)
		return 1
	}
	ev, err := ingest.Parse(*source, ingest.Meta{
		SliceID:     *sliceID,
		Harness:     *harness,
		Model:       *model,
		Phase:       *phase,
		DurationSec: *duration,
	}, raw)
	if err != nil {
		fmt.Fprintf(stderr, "ingest: %v\n", err)
		return 1
	}
	if *dryRun {
		return writeJSON(stdout, stderr, ev)
	}
	return persistRunEvent(dbPath, ev, stdout, stderr)
}

func readIngestInput(filePath string, stdin io.Reader) ([]byte, error) {
	if strings.TrimSpace(filePath) != "" {
		return os.ReadFile(filePath)
	}
	return io.ReadAll(stdin)
}

func persistRunEvent(dbPath string, ev domain.RunEvent, stdout, stderr io.Writer) int {
	st, ok := openStore(dbPath, stderr)
	if !ok {
		return 1
	}
	defer st.Close()

	recorded, err := st.RecordRun(context.Background(), ev)
	if err != nil {
		fmt.Fprintf(stderr, "ingest record: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "recorded run #%d for %s (%s %s)\n", recorded.ID, recorded.SliceID, recorded.Phase, recorded.Verdict)
	return 0
}

func cmdImprove(dbPath string, args []string, _ io.Reader, stdout, stderr io.Writer) int {
	fs := newFlagSet("improve", stderr)
	sliceID := fs.String("slice", "", "optional slice id filter")
	limit := fs.Int("limit", 100, "max run events to analyze")
	reportOnly := fs.Bool("report-only", false, "output ledger analysis JSON without calling LLM")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	report, ok := buildObserverReport(dbPath, *sliceID, *limit, stderr)
	if !ok {
		return 1
	}
	if *reportOnly {
		return writeJSON(stdout, stderr, report)
	}

	cfg, err := observer.LoadConfigFromEnv()
	if err != nil {
		fmt.Fprintf(stderr, "improve: %v\n", err)
		return 1
	}
	client, err := observer.NewClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "improve: %v\n", err)
		return 1
	}
	prop, err := client.Propose(context.Background(), report)
	if err != nil {
		fmt.Fprintf(stderr, "improve: %v\n", err)
		return 1
	}
	return writeJSON(stdout, stderr, prop)
}

func buildObserverReport(dbPath, sliceID string, limit int, stderr io.Writer) (observer.Report, bool) {
	st, ok := openStore(dbPath, stderr)
	if !ok {
		return observer.Report{}, false
	}
	defer st.Close()

	ctx := context.Background()
	runs, err := st.ListRuns(ctx, sliceID, limit)
	if err != nil {
		fmt.Fprintf(stderr, "improve list runs: %v\n", err)
		return observer.Report{}, false
	}
	slices, err := st.ListSlices(ctx, "")
	if err != nil {
		fmt.Fprintf(stderr, "improve list slices: %v\n", err)
		return observer.Report{}, false
	}
	return observer.AnalyzeWithSlices(runs, slices), true
}

func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

func writeJSON(stdout, stderr io.Writer, v any) int {
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fmt.Fprintf(stderr, "encode json: %v\n", err)
		return 1
	}
	return 0
}

func splitCSV(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	var out []string
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
