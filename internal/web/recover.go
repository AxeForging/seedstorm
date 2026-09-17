package web

import (
	"errors"
	"net/http"

	"github.com/AxeForging/seedstorm/internal/safego"
)

// recoverHandler answers a panicking request with a JSON 500 carrying an error
// id (the stack is logged under it) instead of dropping the connection.
func recoverHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := safego.Run(r.Method+" "+r.URL.Path, func() error {
			next.ServeHTTP(w, r)
			return nil
		})
		var p *safego.PanicError
		if errors.As(err, &p) {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": p.Error(), "errorId": p.ID})
		}
	})
}
