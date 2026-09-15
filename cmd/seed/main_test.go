package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// dumpFiles are the files every dump must contain.
var dumpFiles = []string{
	"amended_invoices.json",
	"change_seqs.json",
	"cost_centers.json",
	"expected_exceptions.json",
	"fx_gaps.json",
	"fx_rows.json",
	"invoices.json",
	"purchase_order_lines.json",
	"purchase_orders.json",
	"summary.json",
	"suppliers.json",
	"uom_conversions.json",
}

func TestRunWritesADump(t *testing.T) {
	dir := t.TempDir()
	if err := run([]string{"--scenario", "S0", "--seed", "7", "--out", dir}, nil); err != nil {
		t.Fatal(err)
	}
	for _, name := range dumpFiles {
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var any any
		if err := json.Unmarshal(body, &any); err != nil {
			t.Errorf("%s is not valid JSON: %v", name, err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(dir, "legacy"))
	if err != nil {
		t.Fatal(err)
	}
	txt, ok := 0, 0
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		switch filepath.Ext(e.Name()) {
		case ".txt":
			txt++
			if info.Size() == 0 {
				t.Errorf("%s is empty", e.Name())
			}
		case ".ok":
			ok++
			if info.Size() != 0 {
				t.Errorf("the sentinel %s must be empty, it is %d bytes", e.Name(), info.Size())
			}
		default:
			t.Errorf("unexpected file %s in the legacy drop", e.Name())
		}
	}
	if txt == 0 || txt != ok {
		t.Errorf("%d data files and %d sentinels", txt, ok)
	}
}

func TestRunIsByteIdentical(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	for _, dir := range []string{a, b} {
		if err := run([]string{"--scenario", "S0", "--seed", "20260416", "--out", dir}, nil); err != nil {
			t.Fatal(err)
		}
	}
	names := append([]string(nil), dumpFiles...)
	names = append(names,
		filepath.Join("legacy", "KRED_0100_20260416_001.txt"),
		filepath.Join("legacy", "KRED_0100_20260416_001.ok"),
	)
	for _, name := range names {
		x, err := os.ReadFile(filepath.Join(a, name))
		if err != nil {
			t.Fatal(err)
		}
		y, err := os.ReadFile(filepath.Join(b, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(x) != string(y) {
			t.Errorf("%s differs between two runs with the same flags", name)
		}
	}
}

func TestRunRejectsBadFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"no output directory", []string{"--scenario", "S0"}},
		{"unknown scenario", []string{"--scenario", "nope", "--out", t.TempDir()}},
		{"stray argument", []string{"--scenario", "S0", "--out", t.TempDir(), "extra"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := run(tc.args, nil); err == nil {
				t.Fatal("the invocation was accepted")
			}
		})
	}
}

func TestRunOverwritesAnExistingDump(t *testing.T) {
	dir := t.TempDir()
	if err := run([]string{"--scenario", "S0", "--seed", "1", "--out", dir}, nil); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir, "suppliers.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"--scenario", "S0", "--seed", "2", "--out", dir}, nil); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(dir, "suppliers.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) == string(after) {
		t.Error("a second run with a different seed left the old dump in place")
	}
}
