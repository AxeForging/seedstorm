package safego

import (
	"errors"
	"regexp"
	"strings"
	"testing"
)

func TestRun_TurnsAPanicIntoAnError(t *testing.T) {
	err := Run("write users", func() error {
		var list []int
		i := 3
		_ = list[i] // a real runtime panic, not a hand-made one
		return nil
	})
	var p *PanicError
	if !errors.As(err, &p) {
		t.Fatalf("err = %v (%T), want *PanicError", err, err)
	}
	if p.Where != "write users" || !strings.Contains(err.Error(), "index out of range") {
		t.Fatalf("panic error = %+v / %q", p, err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}$`).MatchString(p.ID) {
		t.Fatalf("id = %q, want 8 hex chars to find it in the server log", p.ID)
	}
	if !strings.Contains(string(p.Stack), "safego_test.go") {
		t.Fatalf("stack does not point at the panic:\n%s", p.Stack)
	}
	if strings.Contains(err.Error(), "goroutine") {
		t.Fatalf("the message carries the stack; it belongs in the log only: %q", err)
	}
}

func TestRun_PassesErrorsAndSuccessThrough(t *testing.T) {
	want := errors.New("plain failure")
	if err := Run("x", func() error { return want }); err != want {
		t.Fatalf("err = %v, want the function's own error", err)
	}
	if err := Run("x", func() error { return nil }); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

func TestRecover_InADeferKeepsAnExistingError(t *testing.T) {
	f := func() (err error) {
		defer Recover("deferred", &err)
		panic("kaput")
	}
	var p *PanicError
	if err := f(); !errors.As(err, &p) || p.Value != "kaput" {
		t.Fatalf("err = %v", err)
	}
}
