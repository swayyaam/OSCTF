package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// The --json contract (docs/v0.3.1/01-cli.md): in JSON mode, stdout carries EXACTLY one JSON
// value and nothing else, so `osctf … --json | jq` is always safe. Human-facing prose goes to
// stderr, including in JSON mode — a progress line on stdout would corrupt the parse.
type printer struct {
	json bool
	out  io.Writer
	err  io.Writer
}

func newPrinter(jsonMode bool) *printer {
	return &printer{json: jsonMode, out: os.Stdout, err: os.Stderr}
}

// data emits the machine-readable result. In human mode the caller's rendering is used instead,
// so this is a no-op there.
func (p *printer) data(v any) error {
	if !p.json {
		return nil
	}
	enc := json.NewEncoder(p.out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// human writes prose for a person. It goes to stderr ALWAYS, so it can never interleave with
// JSON on stdout.
func (p *printer) human(format string, a ...any) {
	_, _ = fmt.Fprintf(p.err, format+"\n", a...)
}

// fail renders an error. In JSON mode it is a single object on stderr, so a caller can parse a
// failure as readily as a success.
func (p *printer) fail(err error) {
	if err == nil {
		return
	}
	ce, _ := err.(*cliError)
	if ce != nil && ce.reported {
		return
	}
	if !p.json {
		_, _ = fmt.Fprintln(p.err, "error: "+err.Error())
		if ce != nil && len(ce.fields) > 0 {
			for field, msgs := range ce.fields {
				for _, m := range msgs {
					_, _ = fmt.Fprintf(p.err, "  %s: %s\n", field, m)
				}
			}
		}
		return
	}
	type jsonErr struct {
		Type   string              `json:"type,omitempty"`
		Title  string              `json:"title"`
		Detail string              `json:"detail,omitempty"`
		Fields map[string][]string `json:"field_errors,omitempty"`
	}
	body := jsonErr{Title: err.Error()}
	if ce != nil {
		body.Title = ce.msg
		body.Detail = ce.detail
		body.Fields = ce.fields
	}
	enc := json.NewEncoder(p.err)
	enc.SetIndent("", "  ")
	_ = enc.Encode(struct {
		Error jsonErr `json:"error"`
	}{body})
}
