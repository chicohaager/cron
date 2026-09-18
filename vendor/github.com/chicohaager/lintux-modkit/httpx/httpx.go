// Package httpx is the small HTTP layer the Lintux modules share: JSON
// responses with stable error codes the UI translates, request-body limits,
// a same-origin check for state-changing requests, request logging, and a
// static-file fallback for the module UI.
package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

// DefaultMaxBody caps request bodies unless a module overrides it.
const DefaultMaxBody = 1 << 20

// Error is a request failure with a stable code for the UI and a message
// for logs and API clients.
type Error struct {
	Status int
	Code   string
	Msg    string
}

func (e *Error) Error() string { return e.Msg }

// BadRequest builds a 400 error with a code.
func BadRequest(code, format string, a ...interface{}) error {
	return &Error{Status: http.StatusBadRequest, Code: code, Msg: fmt.Sprintf(format, a...)}
}

// WriteJSON encodes v with the given status.
func WriteJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("[httpx] write response: %v", err)
	}
}

// WriteError answers with {"error": msg, "code": code}.
func WriteError(w http.ResponseWriter, status int, code, msg string) {
	WriteJSON(w, status, map[string]string{"error": msg, "code": code})
}

// WriteErr maps an *Error to its status and code and anything else to 500.
func WriteErr(w http.ResponseWriter, err error) {
	var e *Error
	if errors.As(err, &e) {
		WriteError(w, e.Status, e.Code, e.Msg)
		return
	}
	WriteError(w, http.StatusInternalServerError, "internal", err.Error())
}

// MethodNotAllowed and NotFound are the two answers every router needs.
func MethodNotAllowed(w http.ResponseWriter) {
	WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
}

func NotFound(w http.ResponseWriter) {
	WriteError(w, http.StatusNotFound, "not_found", "not found")
}

// Decode reads a JSON body into v; on failure it answers 400 and returns false.
func Decode(w http.ResponseWriter, r *http.Request, v interface{}) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_json", "invalid JSON: "+err.Error())
		return false
	}
	return true
}

// Logging logs one line per request and caps mutating bodies at maxBody.
func Logging(prefix string, maxBody int64) func(http.Handler) http.Handler {
	if maxBody <= 0 {
		maxBody = DefaultMaxBody
	}
	return func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			if r.Method == http.MethodPost || r.Method == http.MethodPut {
				r.Body = http.MaxBytesReader(w, r.Body, maxBody)
			}
			h.ServeHTTP(w, r)
			log.Printf("[%s] %s %s %v", prefix, r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
		})
	}
}

// SameOrigin is the CSRF check for state-changing requests. Browsers always
// send Origin on POST/PUT/DELETE fetches; it must name this host. A request
// without Origin is a non-browser client and is allowed — it still needs a
// bearer token, which a cross-site page cannot obtain.
func SameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	origin = strings.TrimPrefix(strings.TrimPrefix(origin, "https://"), "http://")
	return strings.EqualFold(origin, r.Host)
}

// CSRF rejects cross-origin POST/PUT/DELETE with 403.
func CSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost, http.MethodPut, http.MethodDelete:
			if !SameOrigin(r) {
				WriteError(w, http.StatusForbidden, "cross_origin", "cross-origin request rejected")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// Static serves the module UI from dir under prefix (e.g. "/modules/cron/")
// and redirects "/" there. On ZimaOS the gateway serves these files itself;
// this keeps the UI reachable on the daemon's own loopback port.
func Static(prefix, dir string, next http.Handler) http.Handler {
	prefix = "/" + strings.Trim(prefix, "/") + "/"
	files := http.StripPrefix(prefix, http.FileServer(http.Dir(dir)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/" || r.URL.Path+"/" == prefix:
			http.Redirect(w, r, prefix, http.StatusFound)
		case strings.HasPrefix(r.URL.Path, prefix):
			if strings.Contains(r.URL.Path, "..") {
				http.NotFound(w, r)
				return
			}
			files.ServeHTTP(w, r)
		default:
			next.ServeHTTP(w, r)
		}
	})
}
