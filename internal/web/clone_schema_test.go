package web

import (
	"context"
	"strings"
	"testing"
)

func TestRunCloneSchema_rejectsSameTarget(t *testing.T) {
	s, err := New(Options{Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sess := &Session{ID: "one", DBType: "pgx"}
	s.sessions.sessions[sess.ID] = sess

	_, err = s.runCloneSchema(context.Background(), sess, CloneSchemaRequest{TargetID: "one"}, testJobControl{})
	if err == nil || !strings.Contains(err.Error(), "must be different") {
		t.Fatalf("err = %v, want same-target barrier", err)
	}
}

func TestRunCloneSchema_rejectsMissingTarget(t *testing.T) {
	s, err := New(Options{Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sess := &Session{ID: "source", DBType: "pgx"}

	_, err = s.runCloneSchema(context.Background(), sess, CloneSchemaRequest{TargetID: "missing"}, testJobControl{})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v, want missing target barrier", err)
	}
}

func TestRunCloneSchema_rejectsCrossDatabaseType(t *testing.T) {
	s, err := New(Options{Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	source := &Session{ID: "source", DBType: "pgx", Info: ConnectionInfo{DBType: "postgres"}}
	target := &Session{ID: "target", DBType: "mysql", Info: ConnectionInfo{DBType: "mysql"}}
	s.sessions.sessions[target.ID] = target

	_, err = s.runCloneSchema(context.Background(), source, CloneSchemaRequest{TargetID: "target"}, testJobControl{})
	if err == nil || !strings.Contains(err.Error(), "matching database types") {
		t.Fatalf("err = %v, want cross-type barrier", err)
	}
}
