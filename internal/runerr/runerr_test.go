package runerr

import (
	"errors"
	"fmt"
	"testing"
)

func TestError_SaysWhereARunFailed(t *testing.T) {
	cause := errors.New(`duplicate key value violates unique constraint "orders_pkey"`)
	err := OnSide(SideTarget, At(PhaseWrite, "orders", cause))
	if got, want := err.Error(), `target · write · orders: duplicate key value violates unique constraint "orders_pkey"`; got != want {
		t.Fatalf("Error() = %q\nwant      %q", got, want)
	}
	if !errors.Is(err, cause) {
		t.Fatal("the cause is not reachable with errors.Is")
	}
	e, ok := As(err)
	if !ok || e.Side != SideTarget || e.Phase != PhaseWrite || e.Table != "orders" {
		t.Fatalf("As = %+v, %v", e, ok)
	}
}

// The innermost layer knows best: wrapping twice keeps the first phase and
// table, and only fills what is missing.
func TestAt_DoesNotOverwriteAMoreSpecificLocation(t *testing.T) {
	err := At(PhaseGenerate, "", At(PhaseWrite, "users", errors.New("refused")))
	e, _ := As(err)
	if e.Phase != PhaseWrite || e.Table != "users" {
		t.Fatalf("outer wrap replaced the location: %+v", e)
	}
	if At(PhaseWrite, "users", nil) != nil || OnSide(SideSource, nil) != nil {
		t.Fatal("wrapping nil must stay nil")
	}
	if got := At(PhaseConnect, "", errors.New("dial tcp: refused")).Error(); got != "connect: dial tcp: refused" {
		t.Fatalf("no table: %q", got)
	}
}

// Wrapped by fmt.Errorf between two locations, the location is printed once.
func TestError_LocationPrintedOnceThroughWrapping(t *testing.T) {
	inner := At(PhaseWrite, "users", errors.New("refused"))
	err := OnSide(SideTarget, fmt.Errorf("mirror: %w", inner))
	if got, want := err.Error(), "target · write · users: mirror: refused"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}
