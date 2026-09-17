// Package safego keeps a panic in one goroutine from killing the process: a
// run that panics fails with an error naming where it happened, and the stack
// goes to the log under a short id the error carries.
package safego

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"runtime/debug"

	"github.com/AxeForging/seedstorm/internal/logging"
)

// PanicError is a recovered panic.
type PanicError struct {
	Where string
	Value any
	Stack []byte
	// ID matches the log line that holds the stack.
	ID string
}

func (p *PanicError) Error() string {
	return fmt.Sprintf("internal error in %s (id %s): %v", p.Where, p.ID, p.Value)
}

// Run calls fn and returns its error, or a *PanicError if it panicked.
func Run(where string, fn func() error) (err error) {
	defer Recover(where, &err)
	return fn()
}

// Recover, deferred directly, turns a panic of the surrounding function into a
// *PanicError stored in *errp.
func Recover(where string, errp *error) {
	r := recover()
	if r == nil {
		return
	}
	p := &PanicError{Where: where, Value: r, Stack: debug.Stack(), ID: newID()}
	logging.Log.Error().Str("id", p.ID).Str("where", where).Interface("panic", r).Msg("Recovered from a panic")
	logging.Log.Debug().Str("id", p.ID).Str("stack", string(p.Stack)).Msg("Panic stack")
	*errp = p
}

func newID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
