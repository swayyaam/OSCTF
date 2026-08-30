package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// The exit-code taxonomy is a contract: agents and CI branch on these without parsing prose, so a
// silent change here breaks callers that never see a compile error.
func TestExitCodeTaxonomy(t *testing.T) {
	cases := map[int]int{
		http.StatusOK:                  exitOK,
		http.StatusCreated:             exitOK,
		http.StatusNoContent:           exitOK,
		http.StatusUnauthorized:        exitAuth,
		http.StatusForbidden:           exitAuth,
		http.StatusNotFound:            exitNotFound,
		http.StatusConflict:            exitConflict,
		http.StatusUnprocessableEntity: exitValidation,
		http.StatusInternalServerError: exitUnavailable,
		http.StatusBadGateway:          exitUnavailable,
		http.StatusServiceUnavailable:  exitUnavailable,
		// Deliberately generic rather than guessed: a wrong code is worse than a vague one.
		http.StatusBadRequest:      exitError,
		http.StatusTooManyRequests: exitError,
	}
	for status, want := range cases {
		if got := exitForStatus(status); got != want {
			t.Errorf("exitForStatus(%d) = %d, want %d", status, got, want)
		}
	}
}

// In JSON mode stdout must carry exactly one JSON value and nothing else, so `… --json | jq` is
// always safe. Prose goes to stderr even in JSON mode; a progress line on stdout corrupts the parse.
func TestJSONModeKeepsStdoutPure(t *testing.T) {
	var out, errb bytes.Buffer
	p := &printer{json: true, out: &out, err: &errb}

	p.human("this is for a person, not a parser")
	if err := p.data(map[string]string{"slug": "web-login"}); err != nil {
		t.Fatalf("data: %v", err)
	}

	var v map[string]string
	if err := json.Unmarshal(out.Bytes(), &v); err != nil {
		t.Fatalf("stdout is not a single JSON value: %v\n%s", err, out.String())
	}
	if v["slug"] != "web-login" {
		t.Errorf("stdout payload = %v", v)
	}
	if errb.Len() == 0 {
		t.Error("the human line went nowhere; it belongs on stderr")
	}
	if bytes.Contains(out.Bytes(), []byte("for a person")) {
		t.Error("prose leaked onto stdout in JSON mode")
	}
}

// A failure in JSON mode is itself a JSON object, on stderr, so a caller can parse a failure as
// readily as a success.
func TestJSONModeRendersErrorsAsJSON(t *testing.T) {
	var out, errb bytes.Buffer
	p := &printer{json: true, out: &out, err: &errb}
	p.fail(&cliError{code: exitValidation, msg: "invalid spec",
		fields: map[string][]string{"slug": {"is required"}}})

	var body struct {
		Error struct {
			Title  string              `json:"title"`
			Fields map[string][]string `json:"field_errors"`
		} `json:"error"`
	}
	if err := json.Unmarshal(errb.Bytes(), &body); err != nil {
		t.Fatalf("stderr is not JSON: %v\n%s", err, errb.String())
	}
	if body.Error.Title != "invalid spec" || len(body.Error.Fields["slug"]) == 0 {
		t.Errorf("error payload = %+v", body.Error)
	}
	if out.Len() != 0 {
		t.Errorf("an error wrote to stdout, which would corrupt a piped parse: %s", out.String())
	}
}

// A command that already rendered its failure in its own documented shape must not have it
// printed a second time on the other stream.
func TestReportedErrorIsNotRenderedTwice(t *testing.T) {
	var out, errb bytes.Buffer
	p := &printer{json: true, out: &out, err: &errb}
	p.fail(&cliError{code: exitValidation, msg: "already said", reported: true})
	if errb.Len() != 0 {
		t.Errorf("a reported error was rendered again: %s", errb.String())
	}
}

// Precedence is documented as flags → environment → context. Getting this wrong is the kind of
// bug where someone edits a context and watches an env var quietly win.
func TestTargetPrecedence(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OSCTF_CONFIG_DIR", dir)
	writeConfig(t, dir, `current-context: ctx
contexts:
  ctx:
    url: https://from-context.test
    token: token-from-context
`)

	t.Run("context when nothing else is set", func(t *testing.T) {
		t.Setenv("OSCTF_URL", "")
		t.Setenv("OSCTF_TOKEN", "")
		got, err := resolveTarget("", "", "")
		if err != nil {
			t.Fatal(err)
		}
		if got.url != "https://from-context.test" || got.token != "token-from-context" {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("environment beats context", func(t *testing.T) {
		t.Setenv("OSCTF_URL", "https://from-env.test")
		t.Setenv("OSCTF_TOKEN", "token-from-env")
		got, err := resolveTarget("", "", "")
		if err != nil {
			t.Fatal(err)
		}
		if got.url != "https://from-env.test" || got.token != "token-from-env" {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("flags beat everything", func(t *testing.T) {
		t.Setenv("OSCTF_URL", "https://from-env.test")
		t.Setenv("OSCTF_TOKEN", "token-from-env")
		got, err := resolveTarget("https://from-flag.test", "token-from-flag", "")
		if err != nil {
			t.Fatal(err)
		}
		if got.url != "https://from-flag.test" || got.token != "token-from-flag" {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("sources mix per field", func(t *testing.T) {
		// A flag URL with a context token is legal, and should not silently fall back to the
		// context's URL as well.
		t.Setenv("OSCTF_URL", "")
		t.Setenv("OSCTF_TOKEN", "")
		got, err := resolveTarget("https://from-flag.test", "", "")
		if err != nil {
			t.Fatal(err)
		}
		if got.url != "https://from-flag.test" || got.token != "token-from-context" {
			t.Fatalf("got %+v", got)
		}
	})
}

func TestUnknownContextIsAUsageError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OSCTF_CONFIG_DIR", dir)
	t.Setenv("OSCTF_URL", "")
	t.Setenv("OSCTF_TOKEN", "")
	writeConfig(t, dir, "contexts: {}\n")
	_, err := resolveTarget("", "", "nope")
	if codeOf(err) != exitUsage {
		t.Fatalf("resolveTarget(unknown context) exit = %d, want %d (%v)", codeOf(err), exitUsage, err)
	}
}

func TestContextNameDerivedFromURL(t *testing.T) {
	cases := map[string]string{
		"https://ctf.example.com":      "ctf.example.com",
		"https://ctf.example.com/":     "ctf.example.com",
		"http://localhost:8080":        "localhost",
		"https://ctf.example.com/path": "ctf.example.com",
	}
	for in, want := range cases {
		if got := contextNameFor(in); got != want {
			t.Errorf("contextNameFor(%q) = %q, want %q", in, got, want)
		}
	}
}

func writeConfig(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
