package main

import (
	"errors"
	"fmt"
	"net/http"
)

// Exit codes, mapped to HTTP classes so an agent or a CI step can branch without parsing prose
// (docs/v0.3.1/01-cli.md). These are a contract: changing one silently breaks every script that
// depends on it.
const (
	exitOK          = 0 // success
	exitError       = 1 // generic/runtime failure
	exitUsage       = 2 // bad flags or arguments
	exitAuth        = 3 // 401/403 — not authenticated, or the token lacks scope
	exitNotFound    = 4 // 404
	exitConflict    = 5 // 409 — e.g. already solved, name taken
	exitValidation  = 6 // 422 — the request or the local spec is invalid
	exitUnavailable = 7 // 5xx — server or plugin unavailable
)

// cliError carries an exit code alongside the message, so every command can just return an error
// and main decides the process's fate in one place.
type cliError struct {
	code int
	msg  string
	// detail is the server's problem+json detail when there was one.
	detail string
	// fields is the 422 field map, surfaced in --json as field_errors.
	fields map[string][]string
	// reported marks a failure the command already rendered in its own documented shape (as
	// `challenge validate` does with {ok:false, field_errors}). main then only sets the exit
	// code, instead of printing the same information a second time on the other stream.
	reported bool
}

func (e *cliError) Error() string {
	if e.detail != "" {
		return e.msg + ": " + e.detail
	}
	return e.msg
}

func errf(code int, format string, a ...any) error {
	return &cliError{code: code, msg: fmt.Sprintf(format, a...)}
}

// codeOf resolves the process exit code for any error a command returns.
func codeOf(err error) int {
	if err == nil {
		return exitOK
	}
	var ce *cliError
	if errors.As(err, &ce) {
		return ce.code
	}
	return exitError
}

// exitForStatus maps an HTTP status to the taxonomy. Anything unrecognised below 500 is a
// generic error rather than a guess — a wrong code is worse than a vague one, because a script
// branches on it.
func exitForStatus(status int) int {
	switch {
	case status >= 200 && status < 300:
		return exitOK
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return exitAuth
	case status == http.StatusNotFound:
		return exitNotFound
	case status == http.StatusConflict:
		return exitConflict
	case status == http.StatusUnprocessableEntity:
		return exitValidation
	case status >= 500:
		return exitUnavailable
	default:
		return exitError
	}
}
