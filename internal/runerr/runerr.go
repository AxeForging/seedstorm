// Package runerr gives a run's failure a location every surface (web, CLI,
// TUI) renders the same way: which side, which phase, which table.
package runerr

import (
	"errors"
	"strings"
)

// Side is the database of a two-sided run (compare, mirror).
const (
	SideSource = "source"
	SideTarget = "target"
)

// Phase is the step of a run that failed.
type Phase string

const (
	PhaseConnect    Phase = "connect"
	PhaseIntrospect Phase = "introspect"
	PhaseCount      Phase = "count"
	PhasePlan       Phase = "plan"
	PhaseTruncate   Phase = "truncate"
	PhaseGenerate   Phase = "generate"
	PhaseWrite      Phase = "write"
	PhaseSync       Phase = "sync"
)

// Error is a failure with its location. Empty fields are unknown.
type Error struct {
	Side  string `json:"side,omitempty"`
	Phase Phase  `json:"phase,omitempty"`
	Table string `json:"table,omitempty"`
	Err   error  `json:"-"`
}

func (e *Error) Error() string {
	var where []string
	for _, part := range []string{e.Side, string(e.Phase), e.Table} {
		if part != "" {
			where = append(where, part)
		}
	}
	msg := e.Err.Error()
	// A located error deeper in the chain prints its own location; this one
	// already carries it, so print only that error's cause.
	if inner, ok := As(e.Err); ok {
		msg = strings.Replace(msg, inner.Error(), inner.Err.Error(), 1)
	}
	if len(where) == 0 {
		return msg
	}
	return strings.Join(where, " · ") + ": " + msg
}

func (e *Error) Unwrap() error { return e.Err }

// As returns the located error in err's chain.
func As(err error) (*Error, bool) {
	var e *Error
	ok := errors.As(err, &e)
	return e, ok
}

// At locates err in a phase and table. A location already present (set
// closer to the failure) is kept; only missing parts are filled.
func At(phase Phase, table string, err error) error {
	if err == nil {
		return nil
	}
	if e, ok := As(err); ok {
		c := *e
		if c.Phase == "" {
			c.Phase = phase
		}
		if c.Table == "" {
			c.Table = table
		}
		return replace(err, e, &c)
	}
	return &Error{Phase: phase, Table: table, Err: err}
}

// OnSide records which database of a two-sided run failed.
func OnSide(side string, err error) error {
	if err == nil {
		return nil
	}
	if e, ok := As(err); ok {
		if e.Side != "" {
			return err
		}
		c := *e
		c.Side = side
		return replace(err, e, &c)
	}
	return &Error{Side: side, Err: err}
}

// replace swaps the located error for its updated copy when it is the
// outermost error; deeper in a chain, a new located error wraps the chain.
func replace(err error, old, updated *Error) error {
	if err == error(old) {
		return updated
	}
	return &Error{Side: updated.Side, Phase: updated.Phase, Table: updated.Table, Err: err}
}
