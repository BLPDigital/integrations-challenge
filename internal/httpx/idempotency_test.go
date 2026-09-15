package httpx

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func testKey(key string) IdempotencyKey {
	return IdempotencyKey{Tenant: "acme-ch", Method: "POST", Path: "/erp/v1/ap/documents", Key: key}
}

func TestIdempotencyReplayAndConflict(t *testing.T) {
	s := NewIdempotencyStore(0)
	k := testKey("blp:acme-ch:prp_0004311:9f21c8aa3b0e4d17")
	bodyA := []byte(`{"item_key":"prp_0004311"}`)
	bodyB := []byte(`{"item_key":"prp_0004312"}`)

	if _, outcome := s.Check(k, BodyHash(bodyA)); outcome != IdempotencyNew {
		t.Fatalf("first check = %v, want new", outcome)
	}

	stored := IdempotencyRecord{
		Status:   http.StatusOK,
		Header:   http.Header{"Content-Type": {MediaJSON}, "Content-Length": {"17"}},
		Body:     []byte(`{"status":"posted"}`),
		BodyHash: BodyHash(bodyA),
	}
	s.Put(k, stored)

	rec, outcome := s.Check(k, BodyHash(bodyA))
	if outcome != IdempotencyReplay {
		t.Fatalf("second check = %v, want replay", outcome)
	}
	if string(rec.Body) != `{"status":"posted"}` || rec.Status != 200 {
		t.Fatalf("stored record = %+v", rec)
	}
	if rec.Header.Get("Content-Length") != "" {
		t.Error("Content-Length must not be replayed")
	}

	if _, outcome := s.Check(k, BodyHash(bodyB)); outcome != IdempotencyConflict {
		t.Fatalf("different body = %v, want conflict", outcome)
	}

	// A different tenant, method or path is a different key.
	for _, other := range []IdempotencyKey{
		{Tenant: "other", Method: k.Method, Path: k.Path, Key: k.Key},
		{Tenant: k.Tenant, Method: "PUT", Path: k.Path, Key: k.Key},
		{Tenant: k.Tenant, Method: k.Method, Path: "/erp/v1/ap/documents:batch", Key: k.Key},
	} {
		if _, outcome := s.Check(other, BodyHash(bodyA)); outcome != IdempotencyNew {
			t.Errorf("key %v collided", other)
		}
	}
}

func TestWriteReplay(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteReplay(rec, IdempotencyRecord{
		Status: http.StatusMultiStatus,
		Header: http.Header{"Content-Type": {MediaJSON}, "X-Doc": {"AP-2026-0004311"}},
		Body:   []byte(`{"document_number":"AP-2026-0004311"}`),
	})
	if rec.Code != http.StatusMultiStatus {
		t.Errorf("status = %d, want 207", rec.Code)
	}
	if got := rec.Header().Get(HeaderIdempotentReplay); got != "true" {
		t.Errorf("Idempotent-Replay = %q, want true", got)
	}
	if got := rec.Header().Get("X-Doc"); got != "AP-2026-0004311" {
		t.Errorf("stored header lost: %q", got)
	}
	if got := rec.Body.String(); got != `{"document_number":"AP-2026-0004311"}` {
		t.Errorf("body = %q", got)
	}

	// A record with no status still writes a valid response.
	rec = httptest.NewRecorder()
	WriteReplay(rec, IdempotencyRecord{Body: []byte("x")})
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestIdempotencyStoreIsolatesStoredState(t *testing.T) {
	s := NewIdempotencyStore(0)
	k := testKey("k")
	body := []byte("b")
	header := http.Header{"X-A": {"1"}}
	s.Put(k, IdempotencyRecord{Status: 200, Header: header, Body: body, BodyHash: "h"})

	// Mutating the caller's copies must not change the store.
	header.Set("X-A", "2")
	body[0] = 'c'
	got, ok := s.Get(k)
	if !ok {
		t.Fatal("record vanished")
	}
	if got.Header.Get("X-A") != "1" || string(got.Body) != "b" {
		t.Fatalf("store shares state with the caller: %+v", got)
	}
	// Mutating the returned copy must not change the store either.
	got.Header.Set("X-A", "3")
	got.Body[0] = 'd'
	again, _ := s.Get(k)
	if again.Header.Get("X-A") != "1" || string(again.Body) != "b" {
		t.Fatalf("returned record shares state with the store: %+v", again)
	}
}

func TestIdempotencyEvictionIsInsertionOrder(t *testing.T) {
	// The retention floor applies, so a small cap is raised to 4096 and the
	// first 4096 keys survive.
	s := NewIdempotencyStore(1)
	total := DefaultIdempotencyRetention + 10
	for i := 0; i < total; i++ {
		s.Put(testKey("k"+strconv.Itoa(i)), IdempotencyRecord{Status: 200, BodyHash: "h"})
	}
	if got := s.Len(); got != DefaultIdempotencyRetention {
		t.Fatalf("len = %d, want %d", got, DefaultIdempotencyRetention)
	}
	// The ten oldest are gone, in insertion order, and nothing newer is.
	for i := 0; i < 10; i++ {
		if _, ok := s.Get(testKey("k" + strconv.Itoa(i))); ok {
			t.Fatalf("key %d survived eviction", i)
		}
	}
	for i := 10; i < total; i++ {
		if _, ok := s.Get(testKey("k" + strconv.Itoa(i))); !ok {
			t.Fatalf("key %d was evicted early", i)
		}
	}
	keys := s.Keys()
	if len(keys) != DefaultIdempotencyRetention {
		t.Fatalf("Keys() = %d entries", len(keys))
	}
	if !strings.HasSuffix(keys[0], "k10") {
		t.Fatalf("oldest retained key = %q", keys[0])
	}
}

func TestIdempotencyRepeatedPutKeepsPosition(t *testing.T) {
	s := NewIdempotencyStore(DefaultIdempotencyRetention)
	for i := 0; i < 3; i++ {
		s.Put(testKey("k"+strconv.Itoa(i)), IdempotencyRecord{Status: 200})
	}
	s.Put(testKey("k0"), IdempotencyRecord{Status: 201})
	got := s.Keys()
	want := []string{testKey("k0").String(), testKey("k1").String(), testKey("k2").String()}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	if rec, _ := s.Get(testKey("k0")); rec.Status != 201 {
		t.Errorf("re-put did not replace the record: %+v", rec)
	}
}

func TestIdempotencyUnlimitedRetentionNeverEvicts(t *testing.T) {
	s := NewIdempotencyStore(0)
	for i := 0; i < DefaultIdempotencyRetention+100; i++ {
		s.Put(testKey("k"+strconv.Itoa(i)), IdempotencyRecord{Status: 200})
	}
	if got, want := s.Len(), DefaultIdempotencyRetention+100; got != want {
		t.Fatalf("len = %d, want %d", got, want)
	}
}

func TestRequiresIdempotencyKey(t *testing.T) {
	tests := []struct {
		method string
		path   string
		want   bool
	}{
		{"POST", "/v1/ingest/batches", true},
		{"POST", "/v1/ingest/batches/b1/records", true},
		{"POST", "/v1/outbox/acks", true},
		{"PUT", "/v1/ingest/records/supplier/0000417", true},
		{"PATCH", "/v1/x", true},
		{"DELETE", "/v1/x", true},
		{"POST", "/erp/v1/ap/documents", true},
		{"POST", "/erp/v1/ap/documents:batch", true},
		{"GET", "/v1/outbox/proposals", false},
		{"HEAD", "/v1/outbox/proposals", false},
		{"POST", "/v1/auth/token", false},
		{"POST", "/erp/v1/auth/token", false},
		{"POST", "/admin/v1/inbox/scan", false},
		{"POST", "/erp-admin/v1/reset", false},
		{"POST", "/soap/FinancialReferenceDataService", false},
		{"POST", "/healthz", false},
	}
	for _, tc := range tests {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(""))
			if got := RequiresIdempotencyKey(r); got != tc.want {
				t.Errorf("RequiresIdempotencyKey = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRequireIdempotencyKey(t *testing.T) {
	tests := []struct {
		name     string
		method   string
		path     string
		header   string
		wantKey  string
		wantCode string
	}{
		{"present", "POST", "/erp/v1/ap/documents", "blp:acme:1:abc", "blp:acme:1:abc", ""},
		{"padded", "POST", "/erp/v1/ap/documents", "  k  ", "k", ""},
		{"missing", "POST", "/erp/v1/ap/documents", "", "", CodeIdempotencyKeyRequired},
		{"blank", "POST", "/erp/v1/ap/documents", "   ", "", CodeIdempotencyKeyRequired},
		{"not required on GET", "GET", "/erp/v1/suppliers", "", "", ""},
		{"not required on admin", "POST", "/admin/v1/reset", "", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(""))
			if tc.header != "" {
				r.Header.Set(HeaderIdempotencyKey, tc.header)
			}
			key, err := RequireIdempotencyKey(r)
			if tc.wantCode != "" {
				if err == nil {
					t.Fatal("want an error")
				}
				e := AsError(err)
				if e.Code != tc.wantCode || e.Status != 400 {
					t.Fatalf("error = %+v", e)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if key != tc.wantKey {
				t.Errorf("key = %q, want %q", key, tc.wantKey)
			}
		})
	}
}

func TestBodyHashTreatsAbsentAsEmpty(t *testing.T) {
	if BodyHash(nil) != BodyHash([]byte{}) {
		t.Error("nil and empty bodies hash differently")
	}
	if BodyHash([]byte("a")) == BodyHash([]byte("b")) {
		t.Error("different bodies hash the same")
	}
	if got, want := BodyHash([]byte("")), "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"; got != want {
		t.Errorf("BodyHash(empty) = %q, want %q", got, want)
	}
}

func TestIdempotencyOutcomeString(t *testing.T) {
	tests := map[IdempotencyOutcome]string{
		IdempotencyNew:        "new",
		IdempotencyReplay:     "replay",
		IdempotencyConflict:   "conflict",
		IdempotencyOutcome(9): "unknown",
	}
	for outcome, want := range tests {
		if got := outcome.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", outcome, got, want)
		}
	}
}
