//go:build integration

package handlers_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/osctf/platform/internal/audit"
	"github.com/osctf/platform/internal/auth"
	"github.com/osctf/platform/internal/challenges"
	"github.com/osctf/platform/internal/clock"
	"github.com/osctf/platform/internal/db/gen"
	"github.com/osctf/platform/internal/events"
	"github.com/osctf/platform/internal/handlers"
	"github.com/osctf/platform/internal/httpserver"
	"github.com/osctf/platform/internal/redisx"
	"github.com/osctf/platform/internal/storage"
	"github.com/osctf/platform/internal/teams"
	"github.com/osctf/platform/internal/testsupport"
	"github.com/osctf/platform/internal/users"
)

// memStore is an in-memory ObjectStore for tests (no MinIO container needed).
type memStore struct{ m map[string][]byte }

func newMemStore() *memStore { return &memStore{m: map[string][]byte{}} }

func (s *memStore) Put(_ context.Context, key string, r io.Reader, _ int64, _ string) error {
	buf, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.m[key] = buf
	return nil
}
func (s *memStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	b, ok := s.m[key]
	if !ok {
		return nil, fmt.Errorf("not found: %s", key)
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}
func (s *memStore) Delete(_ context.Context, key string) error { delete(s.m, key); return nil }

var _ storage.ObjectStore = (*memStore)(nil)

// serverWithEvent builds a server whose event window is [now-1h, now+1h] (running)
// unless prestart is true, in which case it starts in 1h (pre).
func serverWithEvent(t *testing.T, pool *pgxpool.Pool, rdb *redis.Client, prestart bool) (http.Handler, clock.Clock) {
	t.Helper()
	q := gen.New(pool)
	clk := clock.System()
	now := time.Now().UTC()
	start, end := now.Add(-time.Hour), now.Add(time.Hour)
	if prestart {
		start, end = now.Add(time.Hour), now.Add(2*time.Hour)
	}
	if _, err := q.CreateEvent(context.Background(), gen.CreateEventParams{
		ID: uuid.Must(uuid.NewV7()), Name: "Test CTF", Description: "d", StartsAt: start, EndsAt: end,
	}); err != nil {
		t.Fatalf("create event: %v", err)
	}
	sessions := auth.NewSessionStore(rdb, time.Hour)
	h := handlers.New(handlers.Deps{
		Users:           users.New(q, sessions, true),
		Teams:           teams.New(pool, 4),
		Events:          events.New(q, clk),
		Challenges:      challenges.New(q, newMemStore()),
		Auth:            auth.NewRegistry(auth.NewEmailPasswordProvider(q, nil)),
		Sessions:        sessions,
		Limiter:         redisx.NewLimiter(rdb),
		Audit:           audit.New(q, discardLog()),
		SessionTTL:      time.Hour,
		MaxAttachmentMB: 100,
	})
	srv := httpserver.New(httpserver.Deps{
		Log: discardLog(), Handlers: h, Sessions: sessions, BaseOrigin: testOrigin,
	})
	return srv, clk
}

func TestChallengeVisibilityAndFlagOmittedIntegration(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	rdb := testsupport.Redis(t)
	srv, _ := serverWithEvent(t, pool, rdb, false)

	admin := makeAdmin(t, srv, pool, "root", "root@example.com")
	player := registerUser(t, srv, "player", "player@example.com")

	// Create an invisible challenge.
	body := `{"title":"Sanity","category":"misc","flag":"OSCTF{x}","scoring":"static","points_initial":50}`
	rec := do(t, srv, admin, http.MethodPost, "/api/v0/admin/challenges", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create challenge = %d (%s)", rec.Code, rec.Body)
	}
	var created struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)

	// Participant cannot see it (invisible → 404).
	if rec := do(t, srv, player, http.MethodGet, "/api/v0/challenges/"+created.Slug, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("invisible detail = %d, want 404", rec.Code)
	}

	// Toggle visible.
	if rec := do(t, srv, admin, http.MethodPatch, "/api/v0/admin/challenges/"+created.ID, `{"visible":true}`); rec.Code != http.StatusOK {
		t.Fatalf("make visible = %d (%s)", rec.Code, rec.Body)
	}

	// Now the participant sees it — and the JSON must NOT contain a flag field.
	rec = do(t, srv, player, http.MethodGet, "/api/v0/challenges/"+created.Slug, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("visible detail = %d", rec.Code)
	}
	var raw map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &raw)
	if _, hasFlag := raw["flag"]; hasFlag {
		t.Error("participant challenge detail leaked the flag field")
	}
	if raw["points"].(float64) != 50 {
		t.Errorf("points = %v, want 50", raw["points"])
	}

	// The board lists it, still without a flag.
	rec = do(t, srv, player, http.MethodGet, "/api/v0/challenges", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("board = %d", rec.Code)
	}
	var list []map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 1 {
		t.Fatalf("board has %d challenges, want 1", len(list))
	}
	if _, hasFlag := list[0]["flag"]; hasFlag {
		t.Error("board leaked the flag field")
	}
}

func TestAttachmentRoundTripIntegration(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	rdb := testsupport.Redis(t)
	srv, _ := serverWithEvent(t, pool, rdb, false)

	admin := makeAdmin(t, srv, pool, "root", "root@example.com")
	player := registerUser(t, srv, "player", "player@example.com")

	rec := do(t, srv, admin, http.MethodPost, "/api/v0/admin/challenges",
		`{"title":"Files","category":"forensics","flag":"OSCTF{f}","scoring":"static","points_initial":100,"visible":true}`)
	var created struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)

	// Upload a 1 MB payload.
	payload := bytes.Repeat([]byte("OSCTF-attachment-bytes-"), 45590) // ~1 MB
	want := sha256.Sum256(payload)
	upRec := uploadFile(t, srv, admin, "/api/v0/admin/challenges/"+created.ID+"/attachments", "capture.bin", payload)
	if upRec.Code != http.StatusCreated {
		t.Fatalf("upload = %d (%s)", upRec.Code, upRec.Body)
	}
	var att struct {
		ID   string `json:"id"`
		Size int64  `json:"size_bytes"`
	}
	_ = json.Unmarshal(upRec.Body.Bytes(), &att)
	if att.Size != int64(len(payload)) {
		t.Errorf("recorded size %d, want %d", att.Size, len(payload))
	}

	// Download it back and compare byte-for-byte.
	dl := do(t, srv, player, http.MethodGet, "/api/v0/challenges/"+created.Slug+"/attachments/"+att.ID, "")
	if dl.Code != http.StatusOK {
		t.Fatalf("download = %d", dl.Code)
	}
	got := sha256.Sum256(dl.Body.Bytes())
	if got != want {
		t.Error("downloaded bytes differ from uploaded bytes")
	}

	// Duplicate filename → 409.
	if r := uploadFile(t, srv, admin, "/api/v0/admin/challenges/"+created.ID+"/attachments", "capture.bin", payload); r.Code != http.StatusConflict {
		t.Fatalf("duplicate upload = %d, want 409", r.Code)
	}
}

func TestPreStartBoardForbiddenIntegration(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	rdb := testsupport.Redis(t)
	srv, _ := serverWithEvent(t, pool, rdb, true) // event starts in 1h

	player := registerUser(t, srv, "player", "player@example.com")
	if rec := do(t, srv, player, http.MethodGet, "/api/v0/challenges", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("pre-start board = %d, want 403", rec.Code)
	}
	// An admin previews pre-start (200).
	admin := makeAdmin(t, srv, pool, "root", "root@example.com")
	if rec := do(t, srv, admin, http.MethodGet, "/api/v0/challenges", ""); rec.Code != http.StatusOK {
		t.Fatalf("admin pre-start board = %d, want 200", rec.Code)
	}
}

func uploadFile(t *testing.T, srv http.Handler, jar *cookieJar, path, filename string, content []byte) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("form file: %v", err)
	}
	_, _ = fw.Write(content)
	_ = w.Close()

	r := httptest.NewRequest(http.MethodPost, path, &buf)
	r.Header.Set("Content-Type", w.FormDataContentType())
	r.Header.Set("Origin", testOrigin)
	jar.apply(r)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, r)
	return rec
}
