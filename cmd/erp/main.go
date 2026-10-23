// Command erp runs the mock ERP of the integrations challenge: the REST surface
// under /erp/v1, the one SOAP operation at /soap/FinancialReferenceDataService,
// the legacy file export drop and the admin surface under /erp-admin/v1.
//
// Usage:
//
//	erp --listen 127.0.0.1:8082 --scenario S1 --seed 20260416 --quota 1200 \
//	    --export-dir var/erp/export
//
// The service prints exactly one JSON line on stdout before it starts serving,
// naming the address it actually bound:
//
//	{"svc":"erp","addr":"127.0.0.1:41235"}
//
// so a harness can pass --listen 127.0.0.1:0 and never hard-code a port. Every
// request afterwards adds one JSON log line, with no timestamp in it, because a
// clock would make the transcript undiffable.
//
// Nothing here reads the wall clock. Time is the virtual clock of
// internal/simclock, which only moves when a request declares a cost, so two runs
// of the same scenario against the same request script produce byte-identical
// output.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/fatjonblp/coding_challange_integrations/internal/erp"
	"github.com/fatjonblp/coding_challange_integrations/internal/erp/web"
	"github.com/fatjonblp/coding_challange_integrations/internal/seed"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(os.Args[1:], os.Stdout, nil, ctx.Done()); err != nil {
		fmt.Fprintln(os.Stderr, "erp:", err)
		os.Exit(1)
	}
}

// options are the parsed command line.
type options struct {
	listen string
	cfg    erp.Config
}

// parseFlags parses the command line into a server configuration.
//
// --chaos defaults to on, which is what grading uses; --no-chaos turns fault
// injection off for local debugging and changes nothing else, so the state digest
// is identical either way. When both are given, --no-chaos wins: the more
// conservative flag should not depend on argument order.
func parseFlags(args []string, stderr io.Writer) (options, error) {
	fs := flag.NewFlagSet("erp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	listen := fs.String("listen", erp.DefaultListenAddr,
		"address to listen on; 127.0.0.1:0 picks a free port and prints it")
	scenario := fs.String("scenario", seed.ScenarioS0,
		"scenario to serve: "+strings.Join(seed.Scenarios(), ", "))
	seedValue := fs.Int64("seed", 0, "scenario seed; keys the dataset, the credentials and fault injection")
	quota := fs.Int("quota", 0, "hard request quota for the run; 0 means unlimited")
	chaos := fs.Bool("chaos", true, "inject the content-addressed 429 and 503 faults")
	noChaos := fs.Bool("no-chaos", false, "disable fault injection (overrides --chaos)")
	exportDir := fs.String("export-dir", erp.DefaultExportDir,
		"directory the legacy KRED-EXP delivery is written to; empty disables the drop")
	adminToken := fs.String("admin-token", "",
		"token for /erp-admin/v1; empty derives it from the seed")
	clientSecret := fs.String("client-secret", "",
		"REST client secret; empty derives it from the seed")
	soapPassword := fs.String("soap-password", "",
		"WS-Security UsernameToken password; empty derives it from the seed")
	permanentFaultAt := fs.Int("permanent-fault-at", 0,
		"answer the nth non-admin request with a permanent, non-retriable 503; 0 disables it")
	permanentFaultPath := fs.String("permanent-fault-path", "",
		"narrow the permanent fault to requests whose path contains this substring")
	emptyPageAt := fs.Int("empty-page-at", 0,
		"return one empty page, has_more still true, on the nth page of every collection; 0 disables it")
	soapTruncateAt := fs.Int("soap-truncate-at", 0,
		"cut the SOAP rate table at this many rows and report Truncated=true; 0 serves the whole table")
	requestLogLimit := fs.Int("request-log-limit", erp.DefaultRequestLogLimit,
		"how many request log entries the admin surface retains")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: erp [--listen ADDR] [--scenario ID] [--seed N] [--quota N]\n"+
			"           [--chaos|--no-chaos] [--export-dir DIR] [--admin-token TOKEN]\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return options{}, err
	}
	if fs.NArg() > 0 {
		return options{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if strings.TrimSpace(*listen) == "" {
		return options{}, errors.New("--listen must not be empty")
	}
	opts := options{
		listen: *listen,
		cfg: erp.Config{
			Scenario:           *scenario,
			Seed:               *seedValue,
			Quota:              *quota,
			Chaos:              *chaos && !*noChaos,
			ExportDir:          *exportDir,
			SOAPTruncateAt:     *soapTruncateAt,
			PermanentFaultAt:   *permanentFaultAt,
			PermanentFaultPath: *permanentFaultPath,
			EmptyPageAt:        *emptyPageAt,
			RequestLogLimit:    *requestLogLimit,
			Creds: erp.Credentials{
				AdminToken:   *adminToken,
				ClientSecret: *clientSecret,
				SOAPPassword: *soapPassword,
			},
		},
	}
	return opts, nil
}

// addrLine is the one line printed on stdout before serving starts.
type addrLine struct {
	Svc  string `json:"svc"`
	Addr string `json:"addr"`
}

// run builds the server, binds the listener, announces the address and serves
// until stop is closed.
//
// ready, when non-nil, is called with the bound address after the announcement.
// It exists so a test can drive the real command without racing the listener.
func run(args []string, stdout io.Writer, ready func(addr string), stop <-chan struct{}) error {
	opts, err := parseFlags(args, os.Stderr)
	if err != nil {
		return err
	}
	// The UI is mounted inside the ERP and reads it back, so the two are tied
	// together after construction. Observe wraps the server so the idempotency
	// view can report first status, replays and conflicts per key.
	ui := web.New(web.Options{})
	opts.cfg.UI = ui
	srv, err := erp.New(opts.cfg)
	if err != nil {
		return err
	}
	ui.Attach(web.NewAdminSource(srv))
	handler := ui.Observe(srv)
	ln, err := net.Listen("tcp", opts.listen)
	if err != nil {
		return err
	}
	addr := ln.Addr().String()
	if stdout != nil {
		// Exactly one line, and it is the first thing on stdout: a harness that
		// passed 127.0.0.1:0 reads this to learn the port.
		if _, err := fmt.Fprintf(stdout, "{\"svc\":%q,\"addr\":%q}\n", "erp", addr); err != nil {
			ln.Close()
			return err
		}
	}
	if ready != nil {
		ready(addr)
	}

	httpSrv := &http.Server{Handler: handler}
	errCh := make(chan error, 1)
	go func() { errCh <- httpSrv.Serve(ln) }()
	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-stop:
		// SIGTERM: stop accepting, let in-flight requests finish. Nothing here
		// waits on a wall clock; the harness owns the grace period.
		if err := httpSrv.Shutdown(context.Background()); err != nil {
			return err
		}
		if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}
