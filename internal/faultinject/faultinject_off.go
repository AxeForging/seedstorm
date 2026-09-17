//go:build !faultinject

// Package faultinject makes named steps of a run fail on purpose, for tests
// only. The default build compiles Hit to nothing; build with
// -tags faultinject to enable it (see faultinject_on.go).
package faultinject

import "context"

// Hit does nothing in the default build.
func Hit(context.Context, string, string) error { return nil }
