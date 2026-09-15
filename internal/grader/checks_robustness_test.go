package grader

import (
	"encoding/json"
	"net/http"
	"testing"
)

// req builds one logged ERP request. The sequence is the caller's index, because
// the retry check reads it to tell a first answer from what came after it.
func req(seq int64, sig, path string, status int, retriable *bool) ERPRequest {
	return ERPRequest{Sequence: seq, Signature: sig, Method: "POST", Path: path,
		Status: status, Retriable: retriable}
}

func boolPtr(b bool) *bool { return &b }

// token is the authentication call every invocation opens with.
func token(seq int64) ERPRequest {
	return ERPRequest{Sequence: seq, Signature: "POST /erp/v1/auth/token",
		Method: "POST", Path: "/erp/v1/auth/token", Status: http.StatusOK}
}

// A permanent refusal is the answer to THAT run. The rule for it is to record the
// work as failed and leave it for the next run, so the next run asking again is
// the rule being obeyed and must not be scored as a retry. The same request twice
// inside one invocation is the mistake the assertion is for.
func TestNonRetriableIsJudgedPerInvocation(t *testing.T) {
	const sig = "POST /erp/v1/ap/documents:batch|prp_0000201"
	const path = "/erp/v1/ap/documents:batch"
	refused := req(2, sig, path, http.StatusServiceUnavailable, boolPtr(false))

	cases := []struct {
		name string
		log  []ERPRequest
		want Status
	}{
		{
			name: "the night after is not a retry",
			log: []ERPRequest{token(1), refused,
				token(3), req(4, sig, path, http.StatusOK, nil)},
			want: StatusPass,
		},
		{
			name: "the same run asking twice is",
			log:  []ERPRequest{token(1), refused, req(3, sig, path, http.StatusServiceUnavailable, boolPtr(false))},
			want: StatusFail,
		},
		{
			name: "a token refresh after a 401 starts no new invocation",
			log: []ERPRequest{token(1), refused,
				req(3, "GET /erp/v1/suppliers", "/erp/v1/suppliers", http.StatusUnauthorized, boolPtr(true)),
				token(4), req(5, sig, path, http.StatusServiceUnavailable, boolPtr(false))},
			want: StatusFail,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			obs := &Observed{ERPRequests: c.log}
			res, err := checkNoRetryOnNonRetriable(Assertion{ID: "T.retry"}, obs)
			if err != nil {
				t.Fatalf("check: %v", err)
			}
			if res.Status != c.want {
				t.Fatalf("status = %s, want %s (%s)", res.Status, c.want, res.Summary)
			}
		})
	}
}

// Waste means asking again for something THIS run already has. A second night
// re-reading a page its predecessor read has to spend the request again: the
// answer may have changed, and the connector cannot know that it did not.
func TestWasteAndAttemptsAreJudgedPerInvocation(t *testing.T) {
	const sig = "GET /erp/v1/suppliers|changed_since=0|after="
	const path = "/erp/v1/suppliers"
	get := func(seq int64) ERPRequest {
		return ERPRequest{Sequence: seq, Signature: sig, Method: "GET", Path: path,
			Status: http.StatusOK}
	}
	args, err := json.Marshal(map[string]any{"max": 0, "max_attempts_per_request": 3})
	if err != nil {
		t.Fatal(err)
	}
	a := Assertion{ID: "T.wasted", Args: args}

	twoNights := &Observed{ERPRequests: []ERPRequest{token(1), get(2), token(3), get(4)}}
	res, err := checkWastedRetriesMax(a, twoNights)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if res.Status != StatusPass {
		t.Fatalf("two nights: status = %s, want pass (%s)", res.Status, res.Summary)
	}
	// The page and the two runs' authentication calls: two distinct signatures
	// over the whole scenario, and the reset does not shrink the reported count.
	if got := res.Info["distinct_logical_calls"]; got != "2" {
		t.Errorf("distinct_logical_calls = %q, want 2: the count is of the whole scenario", got)
	}

	oneNight := &Observed{ERPRequests: []ERPRequest{token(1), get(2), get(3)}}
	res, err = checkWastedRetriesMax(a, oneNight)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if res.Status != StatusFail {
		t.Fatalf("one night: status = %s, want fail (%s)", res.Status, res.Summary)
	}

	// The attempt cap is per invocation too: three attempts in each of two runs
	// is not six attempts at one request.
	perRun := []ERPRequest{token(1)}
	for i := int64(0); i < 3; i++ {
		perRun = append(perRun, req(2+i, sig, path, http.StatusServiceUnavailable, boolPtr(true)))
	}
	perRun = append(perRun, token(5))
	for i := int64(0); i < 3; i++ {
		perRun = append(perRun, req(6+i, sig, path, http.StatusServiceUnavailable, boolPtr(true)))
	}
	res, err = checkWastedRetriesMax(a, &Observed{ERPRequests: perRun})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if res.Status != StatusPass {
		t.Fatalf("three attempts per run: status = %s, want pass (%s)", res.Status, res.Summary)
	}
}
