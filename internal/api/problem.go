package api

import (
	"encoding/json"
	"net/http"

	"github.com/DonBlack-117/watchkeeper/internal/check"
)

const problemBase = "https://github.com/DonBlack-117/watchkeeper/blob/main/docs/problems.md#"

// Problem is an RFC 9457 problem details body.
type Problem struct {
	Type   string             `json:"type"`
	Title  string             `json:"title"`
	Status int                `json:"status"`
	Detail string             `json:"detail,omitempty"`
	Errors []check.FieldError `json:"errors,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeProblem(w http.ResponseWriter, p Problem) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}

func badRequest(w http.ResponseWriter, detail string) {
	writeProblem(w, Problem{Type: problemBase + "bad-request", Title: "Bad request", Status: http.StatusBadRequest, Detail: detail})
}

func notFound(w http.ResponseWriter, detail string) {
	writeProblem(w, Problem{Type: problemBase + "not-found", Title: "Not found", Status: http.StatusNotFound, Detail: detail})
}

func invalid(w http.ResponseWriter, errs []check.FieldError) {
	writeProblem(w, Problem{Type: problemBase + "validation", Title: "Validation failed", Status: http.StatusUnprocessableEntity, Detail: "one or more fields are invalid", Errors: errs})
}

func internalError(w http.ResponseWriter) {
	writeProblem(w, Problem{Type: problemBase + "internal", Title: "Internal error", Status: http.StatusInternalServerError})
}

func unauthorized(w http.ResponseWriter) {
	writeProblem(w, Problem{Type: problemBase + "unauthorized", Title: "Unauthorized", Status: http.StatusUnauthorized, Detail: "a valid bearer token is required"})
}

func conflict(w http.ResponseWriter, detail string) {
	writeProblem(w, Problem{Type: problemBase + "conflict", Title: "Conflict", Status: http.StatusConflict, Detail: detail})
}

func tooManyRequests(w http.ResponseWriter) {
	writeProblem(w, Problem{Type: problemBase + "rate-limited", Title: "Too many requests", Status: http.StatusTooManyRequests})
}

func forbidden(w http.ResponseWriter, detail string) {
	writeProblem(w, Problem{Type: problemBase + "forbidden", Title: "Forbidden", Status: http.StatusForbidden, Detail: detail})
}

func badCredentials(w http.ResponseWriter) {
	writeProblem(w, Problem{Type: problemBase + "unauthorized", Title: "Unauthorized", Status: http.StatusUnauthorized, Detail: "wrong username or password"})
}
