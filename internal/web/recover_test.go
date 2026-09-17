package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AxeForging/seedstorm/internal/safego"
)

// A panicking job used to crash `serve`: every session and every other job
// went with it. It must end as a failed job while other jobs keep running.
func TestManager_PanickingJobFailsAloneAndOthersFinish(t *testing.T) {
	m := NewManager()
	release := make(chan struct{})
	other := m.Start(context.Background(), "steady", func(ctx context.Context, jc JobControl) (map[string]any, error) {
		<-release
		return map[string]any{"ok": true}, nil
	})
	boom := m.Start(context.Background(), "boom", func(ctx context.Context, jc JobControl) (map[string]any, error) {
		jc.Phase("write")
		var list []int
		i := 3
		_ = list[i]
		return nil, nil
	})
	select {
	case <-boom.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("panicking job never ended")
	}
	st := boom.State()
	var p *safego.PanicError
	if st.Status != JobFailed || !errors.As(st.Err, &p) {
		t.Fatalf("boom state = %+v, want failed with a recovered panic", st)
	}
	close(release)
	select {
	case <-other.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("the other job did not finish")
	}
	if st := other.State(); st.Status != JobDone {
		t.Fatalf("other job = %+v", st)
	}
}

// A handler panic returns a JSON error the page can show, with an id to find
// the stack in the server log, instead of a dropped connection.
func TestRecoverHandler_AnswersJSON500(t *testing.T) {
	h := recoverHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("handler exploded")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/graph", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %q", rec.Body.String())
	}
	if !strings.Contains(body["error"], "internal error") || len(body["errorId"]) != 8 {
		t.Fatalf("body = %v", body)
	}
	if strings.Contains(rec.Body.String(), "goroutine") {
		t.Fatal("the stack leaked into the response")
	}
}
