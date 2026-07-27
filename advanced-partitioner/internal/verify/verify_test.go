package verify

import (
	"strings"
	"testing"
)

func TestReporterPassAndFail(t *testing.T) {
	// nil logger is tolerated; the Reporter still tracks missing items.
	r := New(nil, "x")
	r.OK("a")
	r.Require(true, "present", "item1")
	if r.Failed() {
		t.Fatal("should not be failed")
	}
	if err := r.Done(); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}

	r2 := New(nil, "y")
	r2.Require(false, "present", "item2")
	r2.Missing("item3")
	if !r2.Failed() {
		t.Fatal("should be failed")
	}
	err := r2.Done()
	if err == nil || !strings.Contains(err.Error(), "item2") || !strings.Contains(err.Error(), "item3") {
		t.Fatalf("expected aggregate error naming both items, got %v", err)
	}
}

func TestLines(t *testing.T) {
	got := Lines("a\n\n  \nb\r\n")
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("Lines = %#v", got)
	}
}
