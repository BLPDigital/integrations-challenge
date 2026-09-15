// Command miniblp runs the digital twin service: the file importer, the REST
// ingest surface, the matching engine, the outbox and the admin surface.
//
// Usage:
//
//	miniblp --listen 127.0.0.1:8081 --data-dir var/miniblp \
//	        --scenario S1 --seed 20260416 --quota 500 --admin-token TOKEN
//
// The chosen address is printed as one JSON line on stdout before the server
// starts serving:
//
//	{"svc":"miniblp","addr":"127.0.0.1:41234"}
//
// which is what makes --listen 127.0.0.1:0 usable: the grader allocates no fixed
// port, reads the address from this line and polls GET /healthz. Everything else
// on stdout is the structured request log, one JSON object per line and never
// carrying a timestamp, so two runs of one scenario produce diffable output.
//
// Credentials are not flags. The connector's client credentials come from the
// environment as TWIN_CLIENT_ID and TWIN_CLIENT_SECRET and the admin token from
// MINIBLP_ADMIN_TOKEN or --admin-token; a secret on a command line is visible in
// every process listing on the machine.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	_ "github.com/fatjonblp/coding_challange_integrations/internal/importer/all"
	"github.com/fatjonblp/coding_challange_integrations/internal/miniblp"
	"github.com/fatjonblp/coding_challange_integrations/internal/miniblp/web"
	"github.com/fatjonblp/coding_challange_integrations/internal/seed"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "miniblp:", err)
		os.Exit(1)
	}
}

// run parses the flags, starts the server and serves until the process is asked
// to stop. It returns an error rather than exiting so it is testable.
func run(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("miniblp", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	listen := fs.String("listen", "127.0.0.1:8081",
		"address to listen on; 127.0.0.1:0 picks a free port and prints it")
	dataDir := fs.String("data-dir", filepath.Join("var", "miniblp"),
		"directory holding the revision store")
	inboxDir := fs.String("inbox-dir", "",
		"file channel root; default <data-dir>/inbox")
	outboxDir := fs.String("outbox-dir", "",
		"mirrored proposal exports; default <data-dir>/outbox")
	seedValue := fs.Int64("seed", 0,
		"scenario seed; keys fault injection, tokens and cursors")
	scenario := fs.String("scenario", seed.ScenarioS0,
		"scenario name: "+strings.Join(seed.Scenarios(), ", "))
	quota := fs.Int("quota", 0,
		"hard request quota for the run; 0 means unlimited")
	permanentFaultAt := fs.Int("permanent-fault-at", 0,
		"answer the nth matching request with a permanent, non-retriable 503; 0 disables it")
	permanentFaultPath := fs.String("permanent-fault-path", "",
		"narrow the permanent fault to requests whose path contains this substring")
	chaos := fs.Bool("chaos", true,
		"enable seed-keyed fault injection")
	noChaos := fs.Bool("no-chaos", false,
		"disable fault injection; it changes nothing else, so the state digest is identical")
	adminToken := fs.String("admin-token", os.Getenv("MINIBLP_ADMIN_TOKEN"),
		"token gating /admin/v1; defaults to MINIBLP_ADMIN_TOKEN")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: miniblp [flags]\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}

	if *inboxDir == "" {
		*inboxDir = filepath.Join(*dataDir, "inbox")
	}
	if *outboxDir == "" {
		*outboxDir = filepath.Join(*dataDir, "outbox")
	}
	token := *adminToken
	if token == "" {
		// The admin surface fails closed on an empty token, and a twin whose
		// admin surface cannot be reached cannot be scanned or reset. A local
		// default keeps `make up` working; grading always passes its own.
		token = "miniblp-admin-token"
	}

	// The UI reads the twin back through its own admin surface, so it is built
	// first, handed to the server, and bound afterwards.
	ui, err := web.New(web.Config{AdminToken: token})
	if err != nil {
		return err
	}

	srv, err := miniblp.New(miniblp.Config{
		UI:                 ui,
		DataDir:            *dataDir,
		InboxDir:           *inboxDir,
		OutboxDir:          *outboxDir,
		Scenario:           *scenario,
		Seed:               *seedValue,
		Quota:              *quota,
		Chaos:              *chaos && !*noChaos,
		PermanentFaultAt:   *permanentFaultAt,
		PermanentFaultPath: *permanentFaultPath,
		AdminToken:         token,
		ClientID:           os.Getenv("TWIN_CLIENT_ID"),
		ClientSecret:       os.Getenv("TWIN_CLIENT_SECRET"),
		RunID:              os.Getenv("MINIBLP_RUN_ID"),
		Log:                stdout,
	})
	if err != nil {
		return err
	}
	defer srv.Close()
	ui.Bind(srv)

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	// One JSON line, before anything is served, so a parent process can read
	// the address it did not choose and start polling /healthz.
	line, err := json.Marshal(struct {
		Svc  string `json:"svc"`
		Addr string `json:"addr"`
	}{"miniblp", ln.Addr().String()})
	if err != nil {
		return err
	}
	if _, err := stdout.Write(append(line, '\n')); err != nil {
		return err
	}
	if f, ok := stdout.(*os.File); ok {
		_ = f.Sync()
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	httpServer := &http.Server{Handler: srv.Handler()}
	errs := make(chan error, 1)
	go func() { errs <- httpServer.Serve(ln) }()

	select {
	case err := <-errs:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		// Teardown is SIGTERM to the process group, so the shutdown has to be
		// prompt and must not wait on an idle keep-alive connection.
		shutdownCtx, cancel := context.WithCancel(context.Background())
		cancel()
		_ = httpServer.Shutdown(shutdownCtx)
		return nil
	}
}
