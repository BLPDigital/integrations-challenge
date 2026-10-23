// Package web serves the ERP's read-only web UI, the one described in section 14
// of the build specification: server-side rendered with html/template, embedded
// with embed.FS, zero JavaScript dependencies, no CDN, no webfont, and therefore
// working offline and inside a grading sandbox with no network at all.
//
// # Why it looks like this
//
// The UI is deliberately plain: gray and white surfaces, boxy bordered tables, a
// monospace-leaning type stack for data and a plain sans stack for prose, no
// logo, no rounded corners, no gradients, and none of the BLP palette. That is a
// requirement rather than an unfinished stylesheet. This is the CUSTOMER's system
// of record, and it sits next to the twin's BLP-branded UI in every review; the
// two must be unmistakable at a glance, so nobody ever reads an ERP screen as a
// twin screen or the other way round. Red appears only for a genuine warning
// state, which in this UI means exactly one thing: a double-posting attempt.
//
// # What it is for
//
// Seven views, each answering one question a reviewer or a candidate would
// otherwise answer by reading a log file:
//
//   - the overview: which scenario and seed are loaded, what the entity counts
//     are, where the quota and the virtual clock stand, and the four counters
//     that decide a submission (requests, 429s, injected 5xx and, above all,
//     duplicate_document_attempts),
//   - the entity browsers over suppliers, purchase orders with their lines, cost
//     centers and the unit-of-measure conversion table, with search by key and
//     stable paging,
//   - the received AP documents, each with the idempotency key that created it,
//   - the duplicate view, which groups posting attempts by external reference and
//     flags every double posting in red, because that is the view a reviewer
//     opens to see a double-posting bug in one second; a safe retry under the
//     same idempotency key is deliberately not flagged, so the red state never
//     cries wolf,
//   - the idempotency key table with first status, replay count and conflict
//     count,
//   - the request log, newest first, filterable by endpoint and by status class,
//     with a summary strip that makes an N+1 read pattern or a retry storm
//     visible without reading a single log line,
//   - the SOAP call log with SOAPAction validity, fault code, row count served
//     and the echoed correlation id.
//
// # Wiring
//
// The UI is mounted inside the ERP itself, which serves it free of charge and
// without authentication under /ui (section 14: both UIs are read-only, live on
// the same port as the API, cost no quota and never require authentication,
// because they are local development tools). Because [erp.Config] takes the UI
// handler and the UI reads the server, the two are tied together after
// construction:
//
//	ui := web.New(web.Options{})
//	cfg.UI = ui
//	srv, err := erp.New(cfg)
//	if err != nil { ... }
//	ui.Attach(web.NewAdminSource(srv))
//
//	// Optional, and only for the idempotency view: with the UI in front of the
//	// server it records the first status, the replays and the conflicts of every
//	// idempotency key. Without it those three columns read "n/a".
//	handler := ui.Observe(srv)
//
// [NewAdminSource] reads the ERP through its own admin surface in process, with
// no socket and no network, and regenerates the scenario's dataset with
// [seed.Generate], which is a pure function of (scenario, seed). Nothing here
// reads the wall clock, and nothing here mutates the ERP: every handler is a GET
// that renders what it read.
package web
