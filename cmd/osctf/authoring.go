package main

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/swayyaam/OSCTF/internal/apiclient"
	"github.com/swayyaam/OSCTF/internal/challengespec"
)

// ---------------------------------------------------------------------------- package

func newPackageCmd() *cobra.Command {
	var out string
	cmd := &cobra.Command{
		Use:   "package <dir>",
		Short: "Build a challenge bundle (offline)",
		Long: "Validates the directory and writes a tarball of challenge.yaml plus the files it " +
			"declares.\n\nThe archive is DETERMINISTIC: entries sorted by path, timestamps zeroed, " +
			"ownership and mode fixed. The same input produces byte-identical output, so a bundle " +
			"can be checksummed, cached, and compared across machines — which is the difference " +
			"between an artifact and a blob that merely happens to work.",
		Args: usageArgs(cobra.ExactArgs(1)),
		RunE: func(_ *cobra.Command, args []string) error {
			p := newPrinter(g.json)
			dir := filepath.Clean(args[0])
			spec, err := loadSpec(dir)
			if err != nil {
				return err
			}
			if out == "" {
				out = spec.Slug + ".tar"
			}
			data, err := buildBundle(dir, spec)
			if err != nil {
				return err
			}
			if err := os.WriteFile(out, data, 0o600); err != nil {
				return errf(exitError, "writing %s: %v", out, err)
			}
			p.human("Wrote %s (%d bytes, %d files).", out, len(data), len(spec.Files)+1)
			return p.data(struct {
				Bundle string   `json:"bundle"`
				Slug   string   `json:"slug"`
				Files  []string `json:"files"`
				Bytes  int      `json:"bytes"`
			}{out, spec.Slug, spec.Files, len(data)})
		},
	}
	cmd.Flags().StringVarP(&out, "out", "o", "", "output path (default: <slug>.tar)")
	return cmd
}

// buildBundle writes the tar in a fixed order with fixed metadata. Nothing here may depend on the
// filesystem's mtimes, ownership, or directory ordering, or two machines produce different bytes
// for the same challenge.
func buildBundle(dir string, spec challengespec.Spec) ([]byte, error) {
	members := append([]string{"challenge.yaml"}, spec.Files...)
	sort.Strings(members)

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, name := range members {
		clean := filepath.Clean(name)
		if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			// A spec that reaches outside its own directory would package someone else's files.
			return nil, errf(exitValidation, "file %q escapes the challenge directory", name)
		}
		body, err := os.ReadFile(filepath.Join(dir, clean)) //nolint:gosec // paths are inside the author's own dir, checked above
		if err != nil {
			return nil, errf(exitValidation, "reading %s: %v", clean, err)
		}
		hdr := &tar.Header{
			Name:     filepath.ToSlash(clean),
			Mode:     0o644,
			Size:     int64(len(body)),
			Typeflag: tar.TypeReg,
			// Zeroed on purpose — see the determinism note above.
			Format: tar.FormatUSTAR,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, errf(exitError, "writing tar header for %s: %v", clean, err)
		}
		if _, err := tw.Write(body); err != nil {
			return nil, errf(exitError, "writing %s into the bundle: %v", clean, err)
		}
	}
	if err := tw.Close(); err != nil {
		return nil, errf(exitError, "closing the bundle: %v", err)
	}
	return buf.Bytes(), nil
}

// ---------------------------------------------------------------------------- create

func newCreateCmd() *cobra.Command {
	var visible bool
	cmd := &cobra.Command{
		Use:   "create <dir>",
		Short: "Create a challenge from a directory (admin)",
		Long: "Validates locally with the same rules the server applies, then creates the " +
			"challenge and uploads the files it declares. Validation runs FIRST so an invalid " +
			"spec costs no server round trip and no half-created challenge.",
		Args: usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := newPrinter(g.json)
			dir := filepath.Clean(args[0])
			spec, err := loadSpec(dir)
			if err != nil {
				return err
			}
			body := specToCreate(spec, visible)

			r, err := call("creating the challenge",
				func(c *apiclient.ClientWithResponses) (*apiclient.AdminCreateChallengeResponse, error) {
					return c.AdminCreateChallengeWithResponse(cmd.Context(), body)
				},
				func(r *apiclient.AdminCreateChallengeResponse) int { return r.StatusCode() },
				func(r *apiclient.AdminCreateChallengeResponse) []byte { return r.Body })
			if err != nil {
				return err
			}
			created := r.JSON201
			if created == nil {
				return errf(exitError, "the server created the challenge but returned no body")
			}

			uploaded, err := uploadFiles(cmd, dir, spec, created.Id)
			if err != nil {
				// The challenge exists; the files did not all land. Say exactly that, because
				// "create failed" would send someone looking for a challenge that is already there.
				return errf(codeOf(err), "challenge %q was created, but uploading its files failed: %v",
					created.Slug, err)
			}

			p.human("Created %s (%d file(s) uploaded).", created.Slug, uploaded)
			return p.data(struct {
				Slug     string `json:"slug"`
				ID       string `json:"id"`
				Uploaded int    `json:"uploaded"`
				Visible  bool   `json:"visible"`
			}{created.Slug, created.Id.String(), uploaded, created.Visible})
		},
	}
	cmd.Flags().BoolVar(&visible, "visible", false, "publish immediately (default: created hidden)")
	return cmd
}

func uploadFiles(cmd *cobra.Command, dir string, spec challengespec.Spec, id apiclient.IdPath) (int, error) {
	n := 0
	for _, f := range spec.Files {
		clean := filepath.Clean(f)
		body, err := os.ReadFile(filepath.Join(dir, clean)) //nolint:gosec // the author's own directory
		if err != nil {
			return n, errf(exitValidation, "reading %s: %v", clean, err)
		}
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		part, err := mw.CreateFormFile("file", filepath.Base(clean))
		if err != nil {
			return n, errf(exitError, "building the upload for %s: %v", clean, err)
		}
		if _, err := part.Write(body); err != nil {
			return n, errf(exitError, "building the upload for %s: %v", clean, err)
		}
		if err := mw.Close(); err != nil {
			return n, errf(exitError, "building the upload for %s: %v", clean, err)
		}
		_, err = call("uploading "+clean,
			func(c *apiclient.ClientWithResponses) (*apiclient.AdminUploadAttachmentResponse, error) {
				return c.AdminUploadAttachmentWithBodyWithResponse(cmd.Context(), id, mw.FormDataContentType(), &buf)
			},
			func(r *apiclient.AdminUploadAttachmentResponse) int { return r.StatusCode() },
			func(r *apiclient.AdminUploadAttachmentResponse) []byte { return r.Body })
		if err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// specToCreate maps the on-disk spec onto the API's create body. Only fields the spec actually
// set are sent: the server owns every default, and echoing our own guesses back at it would make
// the CLI a second source of truth for them.
func specToCreate(s challengespec.Spec, visible bool) apiclient.ChallengeAdminCreate {
	b := apiclient.ChallengeAdminCreate{
		Title:         s.Title,
		Flag:          s.Flag,
		PointsInitial: s.PointsInitial,
		Category:      apiclient.Category(s.Category),
		Slug:          ptr(s.Slug),
		Visible:       ptr(visible || s.Visible),
	}
	if s.Description != "" {
		b.Description = ptr(s.Description)
	}
	if s.Difficulty != "" {
		d := apiclient.Difficulty(s.Difficulty)
		b.Difficulty = &d
	}
	if s.Scoring != "" {
		m := apiclient.ScoringMode(s.Scoring)
		b.Scoring = &m
	}
	if s.Kind != "" {
		k := apiclient.ChallengeKind(s.Kind)
		b.Kind = &k
	}
	if s.Instancing != "" {
		i := apiclient.Instancing(s.Instancing)
		b.Instancing = &i
	}
	if s.FlagMode != "" {
		f := apiclient.FlagMode(s.FlagMode)
		b.FlagMode = &f
	}
	if s.Image != "" {
		b.Image = ptr(s.Image)
	}
	if s.ConnectionTemplate != "" {
		b.ConnectionTemplate = ptr(s.ConnectionTemplate)
	}
	if s.FlagCaseInsensitive {
		b.FlagCaseInsensitive = ptr(true)
	}
	if len(s.ContainerEnv) > 0 {
		b.ContainerEnv = &s.ContainerEnv
	}
	if len(s.WritablePaths) > 0 {
		b.WritablePaths = &s.WritablePaths
	}
	b.PointsMin = s.PointsMin
	b.Decay = s.Decay
	b.MaxAttempts = s.MaxAttempts
	b.InternalPort = s.InternalPort
	b.MemLimitMb = s.MemLimitMB
	b.CpuMillis = s.CPUMillis
	b.InstanceTtlSeconds = s.InstanceTTLSeconds
	b.Egress = s.Egress
	return b
}

func ptr[T any](v T) *T { return &v }

func loadSpec(dir string) (challengespec.Spec, error) {
	spec, err := challengespec.ParseFile(filepath.Join(dir, "challenge.yaml"), filepath.Base(dir))
	if err != nil {
		var ve *challengespec.ValidationError
		if asValidation(err, &ve) {
			return challengespec.Spec{}, &cliError{code: exitValidation, msg: ve.Message, fields: ve.FieldErrors()}
		}
		return challengespec.Spec{}, errf(exitValidation, "%v", err)
	}
	return spec, nil
}

// ---------------------------------------------------------------------------- init

func newInitCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "init", Short: "Scaffold authoring workspaces (offline)"}
	cmd.AddCommand(&cobra.Command{
		Use:   "challenge <slug>",
		Short: "Scaffold a challenge directory",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(_ *cobra.Command, args []string) error {
			p := newPrinter(g.json)
			slug := args[0]
			// Validate the slug through the shared rules rather than a second regex here, so the
			// scaffold cannot produce something the server would reject.
			if _, err := challengespec.Parse([]byte(scaffoldYAML(slug)), slug); err != nil {
				return errf(exitValidation, "%q is not a usable slug: %v", slug, err)
			}
			if _, err := os.Stat(slug); err == nil {
				return errf(exitConflict, "%s already exists", slug)
			}
			if err := os.MkdirAll(filepath.Join(slug, "src"), 0o750); err != nil {
				return errf(exitError, "creating %s: %v", slug, err)
			}
			if err := os.WriteFile(filepath.Join(slug, "challenge.yaml"), []byte(scaffoldYAML(slug)), 0o600); err != nil {
				return errf(exitError, "writing challenge.yaml: %v", err)
			}
			p.human("Scaffolded %s/. Edit challenge.yaml, then `osctf challenge validate %s`.", slug, slug)
			return p.data(struct {
				Slug string `json:"slug"`
				Dir  string `json:"dir"`
			}{slug, slug})
		},
	})
	return cmd
}

func scaffoldYAML(slug string) string {
	return fmt.Sprintf(`slug: %s
title: %s
category: web
difficulty: easy
description: |
  Describe the challenge here. Markdown works.
flag: osctf{change_me}
scoring: dynamic
points_initial: 500
points_min: 100
decay: 25
visible: false
`, slug, strings.ReplaceAll(slug, "-", " "))
}

var _ io.Reader
