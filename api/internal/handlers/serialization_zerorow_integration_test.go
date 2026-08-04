//go:build integration

package handlers_test

// End-to-end companion to serialization_golden_test.go: hits every reachable
// list-returning endpoint against a fresh (zero-row) database and asserts each
// list field is a JSON array, never null — exercising the handler's own make()
// guard for the inline-built page wrappers (which the unit goldens can't reach).
// A regression to `var items []T` in any handler surfaces here as a null.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
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
	"github.com/osctf/platform/internal/scoreboard"
	"github.com/osctf/platform/internal/submissions"
	"github.com/osctf/platform/internal/teams"
	"github.com/osctf/platform/internal/testsupport"
	"github.com/osctf/platform/internal/users"
)

// fullyWiredServer builds a server with every service the list endpoints touch
// and a STARTED event (so the participant board is reachable).
func fullyWiredServer(t *testing.T, pool *pgxpool.Pool, rdb *redis.Client) http.Handler {
	t.Helper()
	q := gen.New(pool)
	now := time.Now()
	if _, err := q.CreateEvent(context.Background(), gen.CreateEventParams{
		ID: uuid.Must(uuid.NewV7()), Name: "Test CTF", Description: "d",
		StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("create event: %v", err)
	}
	sessions := auth.NewSessionStore(rdb, time.Hour)
	clk := clock.System()
	ev := events.New(q, clk)
	h := handlers.New(handlers.Deps{
		Users:           users.New(q, sessions, true),
		Teams:           teams.New(pool, 4),
		Events:          ev,
		Challenges:      challenges.New(q, newMemStore()),
		Submissions:     submissions.New(pool, ev, clk, audit.New(q, discardLog())),
		Scoreboard:      scoreboard.New(q, rdb, ev, clk),
		Auth:            auth.NewEmailPasswordProvider(q, nil),
		Sessions:        sessions,
		Limiter:         redisx.NewLimiter(rdb),
		Audit:           audit.New(q, discardLog()),
		SessionTTL:      time.Hour,
		MaxAttachmentMB: 100,
	})
	return httpserver.New(httpserver.Deps{
		Log: discardLog(), Handlers: h, Sessions: sessions, BaseOrigin: testOrigin,
	})
}

func TestSerializationZeroRowListsIntegration(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	rdb := testsupport.Redis(t)
	srv := fullyWiredServer(t, pool, rdb)

	admin := makeAdmin(t, srv, pool, "root", "root@example.com")
	user := &cookieJar{}
	if rec := do(t, srv, user, http.MethodPost, "/api/v0/auth/register",
		`{"username":"alice","email":"alice@example.com","password":"supersecret1"}`); rec.Code != http.StatusCreated {
		t.Fatalf("register alice = %d (%s)", rec.Code, rec.Body)
	}
	aliceID := adminUserID(t, pool, "alice@example.com")

	cases := []struct {
		name string
		jar  *cookieJar
		path string
		// field is the JSON key that must be an array; "" means the whole body
		// is a top-level array.
		field string
	}{
		{"scoreboard", user, "/api/v0/scoreboard", "standings"},
		{"list_teams", user, "/api/v0/teams", ""},
		{"list_challenges", user, "/api/v0/challenges", ""},
		{"public_user", user, "/api/v0/users/" + aliceID, "solves"},
		{"admin_challenges_page", admin, "/api/v0/admin/challenges", "items"},
		{"admin_teams_page", admin, "/api/v0/admin/teams", "items"},
		{"admin_users_page", admin, "/api/v0/admin/users", "items"},
		{"admin_submissions_page", admin, "/api/v0/admin/submissions", "items"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := do(t, srv, c.jar, http.MethodGet, c.path, "")
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s = %d, want 200 (%s)", c.path, rec.Code, rec.Body)
			}
			body := rec.Body.Bytes()

			// A named field encoded as null is the exact regression this guards.
			if c.field != "" && strings.Contains(rec.Body.String(), `"`+c.field+`":null`) {
				t.Fatalf("%s: field %q encoded as null, want [] (%s)", c.path, c.field, body)
			}

			var root any
			if err := json.Unmarshal(body, &root); err != nil {
				t.Fatalf("%s: unmarshal: %v", c.path, err)
			}
			var list any
			if c.field == "" {
				list = root // whole body is the array
			} else {
				obj, ok := root.(map[string]any)
				if !ok {
					t.Fatalf("%s: body is not a JSON object: %s", c.path, body)
				}
				v, present := obj[c.field]
				if !present {
					t.Fatalf("%s: list field %q absent (required array): %s", c.path, c.field, body)
				}
				list = v
			}
			if list == nil {
				t.Fatalf("%s: list is null, want [] (%s)", c.path, body)
			}
			if _, ok := list.([]any); !ok {
				t.Fatalf("%s: list field is %T, want JSON array: %s", c.path, list, body)
			}
		})
	}
}
