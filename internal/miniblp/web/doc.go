// Package web serves the digital twin's read-only inspection interface,
// BUILD-SPEC 14: the seven views that let a reviewer or a candidate see whether
// the data arrived and what happened to it, without reading a log line.
//
// It is a presentation layer and never a second source of truth. Every number
// on every page comes from the twin the [UI] is bound to, through exactly two
// doors:
//
//   - the revision store ([Twin.Store]), for records, revisions, provenance,
//     raw source bytes, proposals and exceptions. It is the twin's database and
//     already exposes key-ordered streaming scans, full revision histories and
//     the state digest, so the UI re-derives none of it;
//   - the twin's own free admin surface ([Twin.Handler]), for the batch, receipt
//     and run reports and for the audit chain, which the Server holds in memory
//     and the store does not. The UI calls it in process, with no socket and no
//     client, and decodes the same JSON a grader reads.
//
// The two derived quantities on the dashboard - the invoice funnel and the
// exception histogram - are documented projections of that data, computed in
// [Dashboard], never a parallel accounting of it.
//
// Cost: nothing. /ui is mounted outside the accounting Guard and the admin
// prefix is exempt from it, so refreshing a page spends no quota, advances no
// virtual clock, meets no injected fault and writes no log line. An observer
// cannot perturb the transcript it observes.
//
// Determinism: no wall clock, no randomness, no map iteration in any output.
// The virtual clock the header shows is the Governor's. Every list is explicitly
// sorted, every histogram is emitted in ascending code order, and paging is by
// natural key rather than by offset, so a page is stable while a store grows.
//
// Offline by construction: one HTML page per view rendered with html/template,
// one embedded stylesheet, the official SVG lockup served from the binary, and a
// single inline script whose only job is the auto-refresh toggle. No CDN, no
// webfont fetch, no JavaScript dependency, no build step.
//
// Wiring, given [miniblp.Config] carries the UI handler while the UI needs the
// Server that Config builds:
//
//	ui, err := web.New(web.Config{AdminToken: token})
//	srv, err := miniblp.New(miniblp.Config{ /* ... */ UI: ui, AdminToken: token})
//	ui.Bind(srv)
package web
