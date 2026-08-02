//go:build integration

package handlers_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/osctf/platform/internal/audit"
	"github.com/osctf/platform/internal/auth"
	"github.com/osctf/platform/internal/challenges"
	"github.com/osctf/platform/internal/clock"
	"github.com/osctf/platform/internal/db/gen"
	"github.com/osctf/platform/internal/events"
	"github.com/osctf/platform/internal/flags"
	"github.com/osctf/platform/internal/handlers"
	"github.com/osctf/platform/internal/httpserver"
	"github.com/osctf/platform/internal/redisx"
	"github.com/osctf/platform/internal/runtime"
	"github.com/osctf/platform/internal/scheduler"
	"github.com/osctf/platform/internal/scoreboard"
	"github.com/osctf/platform/internal/submissions"
	"github.com/osctf/platform/internal/teams"
	"github.com/osctf/platform/internal/testsupport"
	"github.com/osctf/platform/internal/users"
	"github.com/osctf/platform/internal/ws"
)

// 3a — the authorization policy MATRIX. Every REST route (see policy_test.go's
// OpenAPI-derived table) is driven across seven identities and four event phases, and
// the EXACT expected status is asserted for every one of the ~1200 cells. A route that
// returns 500 where the policy says 403 fails; a cell whose expectation is not declared
// fails rather than defaulting. Deliberately-unusual cells carry a one-line rationale
// where they are declared. Failure output names route, identity, phase, expected, and
// got so a broken cell is diagnosable without a rerun.
//
// One seeded stack is reused across all cells (setPhase rewrites the event window per
// cell so a mutation in one cell cannot bleed the phase into the next). Rejection cells
// (401/403/404/409) run against the shared identities — the auth/phase/ownership gate
// fires before any mutation, so they are hermetic. Success cells that would mutate
// shared state (create/join/leave team, start/stop/extend an instance, logout, change
// password, admin destroy/delete) run against freshly-minted disposable principals or
// throwaway targets of the same identity class, so the reusable identities stay
// pristine. submitFlag returns 200 for both correct and incorrect flags (correctness is
// in the body, not the status), so it submits a deliberately-wrong flag through the
// shared jars and records no solve.

// --- identities ---------------------------------------------------------------

const (
	idAnon    = "anon"       // no session
	idMember  = "member"     // participant, ordinary member of the OWNING team (Alpha)
	idNoTeam  = "no-team"    // participant, not on any team
	idOther   = "other-team" // participant on a DIFFERENT team than the resource (Bravo)
	idCaptain = "captain"    // captain of the OWNING team (Alpha)
	idAdmin   = "admin"      // admin (has NO team)
	// idBannedLiv is a banned user whose session was NOT revoked. This is an
	// UNREACHABLE-BY-DESIGN state: revoke-at-ban deletes every session when a user is
	// banned, so a live banned session should never exist. The identity is asserted as
	// defense-in-depth — its 200s on user routes are NOT intended behaviour; they exist so
	// that a regression which lets a banned session survive (or which starts trusting the
	// session without the ban re-check) is caught here rather than in production.
	idBannedLiv = "banned-live"
)

var identities = []string{idAnon, idMember, idNoTeam, idOther, idCaptain, idAdmin, idBannedLiv}

// onTeam reports whether the identity is a member of some team (Alpha, Bravo, or
// Cheaters). admin and no-team are teamless; anon is unauthenticated.
func onTeam(id string) bool {
	return id == idMember || id == idOther || id == idCaptain || id == idBannedLiv
}

// --- phases -------------------------------------------------------------------

const (
	phPre     = "pre"
	phRunning = "running"
	phFrozen  = "frozen"
	phEnded   = "ended"
)

var phases = []string{phPre, phRunning, phFrozen, phEnded}

// phaseWindow returns the event window (relative to real now, since the stack uses
// clock.System()) that places the event in ph. Frozen is the running window plus a
// freeze point in the past — freeze is phase-independent, so the phase is still Running.
func phaseWindow(ph string) (start, end time.Time, freeze *time.Time) {
	now := time.Now().UTC()
	switch ph {
	case phPre:
		return now.Add(time.Hour), now.Add(2 * time.Hour), nil
	case phRunning:
		return now.Add(-time.Hour), now.Add(time.Hour), nil
	case phFrozen:
		f := now.Add(-30 * time.Minute)
		return now.Add(-time.Hour), now.Add(time.Hour), &f
	case phEnded:
		return now.Add(-2 * time.Hour), now.Add(-time.Hour), nil
	}
	panic("unknown phase " + ph)
}

// --- seeded stack -------------------------------------------------------------

type mtx struct {
	t    *testing.T
	pool *pgxpool.Pool
	srv  http.Handler
	jars map[string]*cookieJar
	seq  int

	challengeSlug string // main visible per-team container challenge
	challengeID   string
	attachmentID  string // a real attachment on the main challenge
	ownTeamID     string // Alpha
	memberUserID  string // alice (Alpha member) — the getUser target
	bravoInvite   string // Bravo's invite code (joinTeam target)

	victimUserID string // a plain user admin-mutation routes target
	victimTeamID string // a plain team adminUpdateTeam targets
	victimChalID string // a plain challenge adminUpdate/upload/deleteAttachment target
	sharedChalID string // a deployed shared-instance challenge (admin single-instance routes)
	dummyAttID   string // a random id for admin delete-attachment rejection cells
}

func (m *mtx) uniq() string { m.seq++; return strconv.Itoa(m.seq) }

func (m *mtx) setPhase(ph string) {
	start, end, freeze := phaseWindow(ph)
	if _, err := m.pool.Exec(context.Background(),
		`UPDATE events SET starts_at=$1, ends_at=$2, freeze_at=$3`, start, end, freeze); err != nil {
		m.t.Fatalf("set phase %s: %v", ph, err)
	}
}

func matrixServer(t *testing.T, pool *pgxpool.Pool, rdb *redis.Client) http.Handler {
	t.Helper()
	q := gen.New(pool)
	now := time.Now().UTC()
	if _, err := q.CreateEvent(context.Background(), gen.CreateEventParams{
		ID: uuid.Must(uuid.NewV7()), Name: "CTF", Description: "d",
		StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("event: %v", err)
	}
	sessions := auth.NewSessionStore(rdb, time.Hour)
	clk := clock.System()
	ev := events.New(q, clk)
	sb := scoreboard.New(q, rdb, ev, clk)
	mgr := runtime.NewManager(runtime.NewFakeRuntime(q), q, "ctf.example.com", 30000, 30999)
	sched := scheduler.New(mgr, q, ev, flags.NewGenerator("osctf"), audit.New(q, discardLog()), clk, discardLog(),
		scheduler.Config{TTL: time.Hour, Extend: 30 * time.Minute, MaxTTL: 4 * time.Hour, Quota: 50})
	h := handlers.New(handlers.Deps{
		Users: users.New(q, sessions, true), Teams: teams.New(pool, 4), Events: ev,
		Challenges:  challenges.New(q, newMemStore()),
		Submissions: submissions.New(pool, ev, clk, audit.New(q, discardLog())),
		Scoreboard:  sb, Recompute: func(ctx context.Context) { _ = sb.Recompute(ctx) },
		Runtime: mgr, Scheduler: sched,
		Auth: auth.NewEmailPasswordProvider(q, nil), Sessions: sessions,
		Limiter: redisx.NewLimiter(rdb), Audit: audit.New(q, discardLog()),
		SessionTTL: time.Hour, MaxAttachmentMB: 10,
	})
	return httpserver.New(httpserver.Deps{Log: discardLog(), Handlers: h, Sessions: sessions, BaseOrigin: testOrigin})
}

func newMatrix(t *testing.T) *mtx {
	t.Helper()
	pool, _ := testsupport.Postgres(t)
	rdb := testsupport.Redis(t)
	m := &mtx{t: t, pool: pool, srv: matrixServer(t, pool, rdb), jars: map[string]*cookieJar{}}
	m.seed()
	return m
}

func (m *mtx) seed() {
	t := m.t
	m.setPhase(phRunning) // seed while the event is live (createChallenge, deploy, uploads)

	m.jars[idAnon] = &cookieJar{}
	m.jars[idAdmin] = makeAdmin(t, m.srv, m.pool, "admin", "admin@x.test")
	m.jars[idCaptain] = teamUp(t, m.srv, "cap", "cap@x.test", "Alpha")
	m.jars[idMember] = registerUser(t, m.srv, "alice", "alice@x.test")
	m.jars[idNoTeam] = registerUser(t, m.srv, "loner", "loner@x.test")
	m.jars[idOther] = teamUp(t, m.srv, "bob", "bob@x.test", "Bravo")

	// alice joins Alpha via the captain's invite code → an ordinary (non-captain) member.
	m.ownTeamID = m.getMe(idCaptain).teamID
	m.jars[idMember].joinViaInvite(t, m.srv, m.inviteCode(m.jars[idCaptain], m.ownTeamID))
	m.memberUserID = m.getMe(idMember).userID

	// Bravo's invite code drives the joinTeam REJECTION cells (already-on-a-team callers).
	bravoID := m.getMe(idOther).teamID
	m.bravoInvite = m.inviteCode(m.jars[idOther], bravoID)

	// banned-with-live-session: register + team + ban WITHOUT revoking the session.
	m.jars[idBannedLiv] = teamUp(t, m.srv, "cheater", "cheat@x.test", "Cheaters")
	m.ban("cheat@x.test")

	admin := m.jars[idAdmin]
	// Main challenge: visible per-team container, plus a real attachment for downloads.
	// flag_mode defaults to static: submitFlag can be tested without first standing up a
	// per-instance flag (a per_instance challenge 403s submissions with no live instance).
	m.challengeID, m.challengeSlug = createChallenge(t, m.srv, admin, `{
		"title":"Main","category":"web","kind":"container","flag":"OSCTF{main}",
		"image":"osctf/example:0.1","internal_port":8000,"connection_template":"http://{host}:{port}",
		"scoring":"static","points_initial":100,"visible":true,"instancing":"per_team"
	}`)
	m.attachmentID = m.uploadAttachment(m.challengeID)

	// Throwaway targets for admin-mutation routes (never the reusable identities).
	m.victimUserID = m.getMe2(registerUser(t, m.srv, "victim", "victim@x.test")).userID
	m.victimTeamID = m.getMe2(teamUp(t, m.srv, "vcap", "vcap@x.test", "Victims")).teamID
	m.victimChalID, _ = createChallenge(t, m.srv, admin, standardChallengeBody("Victim"))

	// A deployed shared-instance challenge for the admin single-instance routes.
	m.sharedChalID, _ = createChallenge(t, m.srv, admin, sharedContainerBody("SharedInst"))
	if rec := do(t, m.srv, admin, http.MethodPost, "/api/v0/admin/challenges/"+m.sharedChalID+"/instance", ""); rec.Code != http.StatusOK {
		t.Fatalf("seed shared deploy = %d (%s)", rec.Code, rec.Body)
	}
	m.dummyAttID = uuid.NewString()
}

func standardChallengeBody(title string) string {
	return fmt.Sprintf(`{"title":%q,"category":"misc","flag":"OSCTF{x}","scoring":"static","points_initial":100,"visible":true}`, title)
}

func sharedContainerBody(title string) string {
	return fmt.Sprintf(`{"title":%q,"category":"web","kind":"container","flag":"OSCTF{c}","image":"osctf/example:0.1","internal_port":8000,"connection_template":"http://{host}:{port}","scoring":"static","points_initial":100,"visible":true}`, title)
}

// --- seed helpers -------------------------------------------------------------

type meInfo struct{ userID, teamID string }

func (m *mtx) getMe(id string) meInfo { return m.getMe2(m.jars[id]) }
func (m *mtx) getMe2(jar *cookieJar) meInfo {
	rec := do(m.t, m.srv, jar, http.MethodGet, "/api/v0/auth/me", "")
	if rec.Code != http.StatusOK {
		m.t.Fatalf("getMe = %d (%s)", rec.Code, rec.Body)
	}
	var out struct {
		Id   string `json:"id"`
		Team *struct {
			Id string `json:"id"`
		} `json:"team"`
	}
	parseJSON(m.t, rec, &out)
	info := meInfo{userID: out.Id}
	if out.Team != nil {
		info.teamID = out.Team.Id
	}
	return info
}

func (m *mtx) inviteCode(jar *cookieJar, teamID string) string {
	rec := do(m.t, m.srv, jar, http.MethodGet, "/api/v0/teams/"+teamID, "")
	var out struct {
		InviteCode string `json:"invite_code"`
	}
	parseJSON(m.t, rec, &out)
	if out.InviteCode == "" {
		m.t.Fatalf("no invite code for team %s (body=%s)", teamID, rec.Body)
	}
	return out.InviteCode
}

// freshJoinableInvite creates a brand-new empty team and returns its invite code, so a
// joinTeam success cell joins a team with room (Bravo fills to its member cap otherwise).
func (m *mtx) freshJoinableInvite() string {
	cap := m.freshTeam()
	return m.inviteCode(cap, m.getMe2(cap).teamID)
}

func (m *mtx) ban(email string) {
	if _, err := m.pool.Exec(context.Background(), `UPDATE users SET banned=true WHERE email=$1`, email); err != nil {
		m.t.Fatalf("ban %s: %v", email, err)
	}
}

func (m *mtx) uploadAttachment(chalID string) string {
	rec := uploadFile(m.t, m.srv, m.jars[idAdmin], "/api/v0/admin/challenges/"+chalID+"/attachments", "a"+m.uniq()+".bin", []byte("payload"))
	if rec.Code != http.StatusCreated {
		m.t.Fatalf("upload attachment = %d (%s)", rec.Code, rec.Body)
	}
	var out struct {
		Id string `json:"id"`
	}
	parseJSON(m.t, rec, &out)
	return out.Id
}

// cookieJar.joinViaInvite joins a team by invite code (used in seed for alice→Alpha).
func (j *cookieJar) joinViaInvite(t *testing.T, srv http.Handler, code string) {
	t.Helper()
	if rec := do(t, srv, j, http.MethodPost, "/api/v0/teams/join", `{"invite_code":"`+code+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("join via invite = %d (%s)", rec.Code, rec.Body)
	}
}

func parseJSON(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("json unmarshal: %v (body=%s)", err, rec.Body)
	}
}

// --- fresh disposable principals (for mutating success cells) ------------------

func (m *mtx) freshNoTeam() *cookieJar {
	u := "nt" + m.uniq()
	return registerUser(m.t, m.srv, u, u+"@x.test")
}
func (m *mtx) freshTeam() *cookieJar {
	u := "tm" + m.uniq()
	return teamUp(m.t, m.srv, u, u+"@x.test", "T"+u)
}

func (m *mtx) freshBannedOnTeam() *cookieJar {
	u := "bl" + m.uniq()
	jar := teamUp(m.t, m.srv, u, u+"@x.test", "B"+u)
	m.ban(u + "@x.test")
	return jar
}

// freshOnTeam returns a fresh on-team principal of the given identity class: a banned
// live session for banned-live, an ordinary team member otherwise.
func (m *mtx) freshOnTeam(id string) *cookieJar {
	if id == idBannedLiv {
		return m.freshBannedOnTeam()
	}
	return m.freshTeam()
}

// --- throwaway admin targets --------------------------------------------------

func (m *mtx) throwawayChallenge() string {
	id, _ := createChallenge(m.t, m.srv, m.jars[idAdmin], standardChallengeBody("Throw"+m.uniq()))
	return id
}

func (m *mtx) throwawaySharedDeployed() (chalID, instID string) {
	chalID, _ = createChallenge(m.t, m.srv, m.jars[idAdmin], sharedContainerBody("Throw"+m.uniq()))
	rec := do(m.t, m.srv, m.jars[idAdmin], http.MethodPost, "/api/v0/admin/challenges/"+chalID+"/instance", "")
	if rec.Code != http.StatusOK {
		m.t.Fatalf("throwaway deploy = %d (%s)", rec.Code, rec.Body)
	}
	var out struct {
		Id string `json:"id"`
	}
	parseJSON(m.t, rec, &out)
	return chalID, out.Id
}

// --- the route model ----------------------------------------------------------

type mroute struct {
	op     string
	expect func(m *mtx, id, ph string) int // declared expected status; 0 ⇒ undeclared ⇒ fail
	build  func(m *mtx, id, ph string) (jar *cookieJar, method, path, body string)
	exec   func(m *mtx, id, ph string) int // optional; overrides build+do (multipart)
}

// --- expectation helpers ------------------------------------------------------

func adminRoute(success int) func(*mtx, string, string) int {
	return func(_ *mtx, id, _ string) int {
		switch id {
		case idAnon:
			return http.StatusUnauthorized // 401
		case idAdmin:
			return success
		default:
			return http.StatusForbidden // 403 — incl. banned-live (requireAdmin re-reads → banned)
		}
	}
}

func userRoute(success int) func(*mtx, string, string) int {
	return func(_ *mtx, id, _ string) int {
		if id == idAnon {
			return http.StatusUnauthorized
		}
		return success // any authenticated caller, INCLUDING banned-live (requireUser trusts the session)
	}
}

func publicRoute() func(*mtx, string, string) int {
	return func(_ *mtx, _, _ string) int { return http.StatusOK }
}

// startedRoute: the participant board/detail/download are gated on the event having
// started. In the pre phase a non-admin gets 403 (admin previews); once started (running/
// frozen/ended) any authenticated caller gets 200.
func startedRoute() func(*mtx, string, string) int {
	return func(_ *mtx, id, ph string) int {
		if id == idAnon {
			return http.StatusUnauthorized
		}
		if ph == phPre {
			if id == idAdmin {
				return http.StatusOK
			}
			return http.StatusForbidden
		}
		return http.StatusOK
	}
}

// --- default builders ---------------------------------------------------------

func shared(method, path, body string) func(*mtx, string, string) (*cookieJar, string, string, string) {
	return func(m *mtx, id, _ string) (*cookieJar, string, string, string) {
		return m.jars[id], method, path, body
	}
}

func sharedP(method string, path func(*mtx) string, body string) func(*mtx, string, string) (*cookieJar, string, string, string) {
	return func(m *mtx, id, _ string) (*cookieJar, string, string, string) {
		return m.jars[id], method, path(m), body
	}
}

func TestPolicyMatrixIntegration(t *testing.T) {
	m := newMatrix(t)
	P := "/api/v0"

	routes := []mroute{
		// --- public ---------------------------------------------------------
		{op: "register", expect: func(_ *mtx, _, _ string) int { return http.StatusCreated },
			build: func(m *mtx, _, _ string) (*cookieJar, string, string, string) {
				u := "reg" + m.uniq()
				return &cookieJar{}, http.MethodPost, P + "/auth/register",
					`{"username":"` + u + `","email":"` + u + `@x.test","password":"supersecret1"}`
			}},
		{op: "login", expect: publicRoute(), // fresh account per cell: login throttles per-email
			build: func(m *mtx, _, _ string) (*cookieJar, string, string, string) {
				u := "log" + m.uniq()
				_ = registerUser(m.t, m.srv, u, u+"@x.test")
				return &cookieJar{}, http.MethodPost, P + "/auth/login", `{"email":"` + u + `@x.test","password":"supersecret1"}`
			}},
		{op: "getEvent", expect: publicRoute(), build: shared(http.MethodGet, P+"/event", "")},
		{op: "getScoreboard", expect: publicRoute(), build: shared(http.MethodGet, P+"/scoreboard", "")},
		{op: "listTeams", expect: publicRoute(), build: shared(http.MethodGet, P+"/teams", "")},
		{op: "getTeam", expect: publicRoute(), build: sharedP(http.MethodGet, func(m *mtx) string { return P + "/teams/" + m.ownTeamID }, "")},
		{op: "getUser", expect: publicRoute(), build: sharedP(http.MethodGet, func(m *mtx) string { return P + "/users/" + m.memberUserID }, "")},

		// --- authenticated participant --------------------------------------
		{op: "getMe", expect: userRoute(http.StatusOK), build: shared(http.MethodGet, P+"/auth/me", "")},
		{op: "logout", expect: userRoute(http.StatusNoContent), // fresh session per authed cell (logout destroys it)
			build: func(m *mtx, id, _ string) (*cookieJar, string, string, string) {
				if id == idAnon {
					return m.jars[idAnon], http.MethodPost, P + "/auth/logout", ""
				}
				jar := m.freshNoTeam()
				if id == idBannedLiv {
					jar = m.freshBannedOnTeam() // banned-live logs out fine — Logout trusts the session
				}
				return jar, http.MethodPost, P + "/auth/logout", ""
			}},
		{op: "changePassword", expect: userRoute(http.StatusNoContent),
			build: func(m *mtx, id, _ string) (*cookieJar, string, string, string) {
				body := `{"current_password":"supersecret1","new_password":"newsecret1"}`
				if id == idAnon {
					return m.jars[idAnon], http.MethodPatch, P + "/auth/me/password", body
				}
				jar := m.freshNoTeam()
				if id == idBannedLiv {
					jar = m.freshBannedOnTeam()
				}
				return jar, http.MethodPatch, P + "/auth/me/password", body
			}},
		{op: "listChallenges", expect: startedRoute(), build: shared(http.MethodGet, P+"/challenges", "")},
		{op: "getChallenge", expect: startedRoute(), build: sharedP(http.MethodGet, func(m *mtx) string { return P + "/challenges/" + m.challengeSlug }, "")},
		{op: "downloadAttachment", expect: startedRoute(),
			build: sharedP(http.MethodGet, func(m *mtx) string {
				return P + "/challenges/" + m.challengeSlug + "/attachments/" + m.attachmentID
			}, "")},

		// --- team lifecycle -------------------------------------------------
		{op: "createTeam", // teamless (no-team, admin) create; on-team → 409 already on a team
			expect: func(_ *mtx, id, _ string) int {
				if id == idAnon {
					return http.StatusUnauthorized
				}
				if onTeam(id) {
					return http.StatusConflict
				}
				return http.StatusCreated // no-team, admin (admin has no team)
			},
			build: func(m *mtx, id, _ string) (*cookieJar, string, string, string) {
				body := `{"name":"CT` + m.uniq() + `"}`
				if id != idAnon && !onTeam(id) {
					return m.freshNoTeam(), http.MethodPost, P + "/teams", body // fresh teamless principal
				}
				return m.jars[id], http.MethodPost, P + "/teams", body
			}},
		{op: "joinTeam",
			expect: func(_ *mtx, id, _ string) int {
				if id == idAnon {
					return http.StatusUnauthorized
				}
				if onTeam(id) {
					return http.StatusConflict
				}
				return http.StatusOK
			},
			build: func(m *mtx, id, _ string) (*cookieJar, string, string, string) {
				if id != idAnon && !onTeam(id) { // success: fresh joiner into a fresh team with room
					return m.freshNoTeam(), http.MethodPost, P + "/teams/join", `{"invite_code":"` + m.freshJoinableInvite() + `"}`
				}
				return m.jars[id], http.MethodPost, P + "/teams/join", `{"invite_code":"` + m.bravoInvite + `"}`
			}},
		{op: "leaveTeam",
			expect: func(_ *mtx, id, _ string) int {
				if id == idAnon {
					return http.StatusUnauthorized
				}
				if onTeam(id) {
					return http.StatusNoContent
				}
				return http.StatusNotFound // no-team, admin: not on a team
			},
			build: func(m *mtx, id, _ string) (*cookieJar, string, string, string) {
				if onTeam(id) {
					return m.freshOnTeam(id), http.MethodPost, P + "/teams/leave", ""
				}
				return m.jars[id], http.MethodPost, P + "/teams/leave", ""
			}},
		{op: "renameTeam", // ownership: captain of Alpha. admin is NOT exempt (admins rename via adminUpdateTeam).
			expect: func(_ *mtx, id, _ string) int {
				switch id {
				case idAnon:
					return http.StatusUnauthorized
				case idCaptain:
					return http.StatusOK
				default:
					return http.StatusForbidden // member, no-team, other-team captain, admin, banned-live
				}
			},
			build: func(m *mtx, id, _ string) (*cookieJar, string, string, string) {
				return m.jars[id], http.MethodPatch, P + "/teams/" + m.ownTeamID, `{"name":"AR` + m.uniq() + `"}`
			}},
		{op: "regenerateInviteCode",
			expect: func(_ *mtx, id, _ string) int {
				switch id {
				case idAnon:
					return http.StatusUnauthorized
				case idCaptain:
					return http.StatusOK
				default:
					return http.StatusForbidden
				}
			},
			build: sharedP(http.MethodPost, func(m *mtx) string { return P + "/teams/" + m.ownTeamID + "/invite-code" }, "")},

		// --- submissions + per-team instances -------------------------------
		{op: "submitFlag", // wrong flag → still 200; correctness is in the body, so no solve is recorded
			expect: func(_ *mtx, id, ph string) int {
				if id == idAnon {
					return http.StatusUnauthorized
				}
				if !onTeam(id) {
					return http.StatusForbidden // no-team / admin: "must be on a team" (before the phase check)
				}
				if ph == phRunning || ph == phFrozen {
					return http.StatusOK // banned-live included: requireUser trusts the session (2f-iv)
				}
				return http.StatusForbidden // pre/ended: "the event is not running"
			},
			// A deliberately-wrong flag → 200 (incorrect; correctness is in the body, not the
			// status) and records no solve. Success cells use fresh teams so the shared teams'
			// per-(team,challenge) submit rate limit is never approached.
			build: func(m *mtx, id, ph string) (*cookieJar, string, string, string) {
				path := P + "/challenges/" + m.challengeSlug + "/submit"
				if onTeam(id) && (ph == phRunning || ph == phFrozen) {
					return m.freshOnTeam(id), http.MethodPost, path, `{"flag":"OSCTF{wrong}"}`
				}
				return m.jars[id], http.MethodPost, path, `{"flag":"OSCTF{wrong}"}`
			}},
		{op: "startInstance",
			expect: func(_ *mtx, id, ph string) int {
				if id == idAnon {
					return http.StatusUnauthorized
				}
				if !onTeam(id) {
					return http.StatusForbidden // no-team / admin: not on a team
				}
				if ph == phRunning || ph == phFrozen {
					return http.StatusCreated
				}
				return http.StatusConflict // pre/ended: requireRunning → 409
			},
			build: func(m *mtx, id, ph string) (*cookieJar, string, string, string) {
				path := P + "/challenges/" + m.challengeSlug + "/instance"
				if onTeam(id) && (ph == phRunning || ph == phFrozen) {
					return m.freshOnTeam(id), http.MethodPost, path, "" // fresh team ⇒ first start ⇒ 201
				}
				return m.jars[id], http.MethodPost, path, ""
			}},
		{op: "stopInstance", // no phase gate; success needs an instance to stop
			expect: func(_ *mtx, id, ph string) int {
				if id == idAnon {
					return http.StatusUnauthorized
				}
				if !onTeam(id) {
					return http.StatusForbidden
				}
				if ph == phRunning || ph == phFrozen {
					return http.StatusNoContent // fresh team: start then stop
				}
				return http.StatusNotFound // pre/ended: cannot start, so nothing to stop
			},
			build: func(m *mtx, id, ph string) (*cookieJar, string, string, string) {
				path := P + "/challenges/" + m.challengeSlug + "/instance"
				if onTeam(id) {
					jar := m.freshOnTeam(id)
					if ph == phRunning || ph == phFrozen {
						_ = do(m.t, m.srv, jar, http.MethodPost, path, "") // start first
					}
					return jar, http.MethodDelete, path, ""
				}
				return m.jars[id], http.MethodDelete, path, ""
			}},
		{op: "extendInstance",
			expect: func(_ *mtx, id, ph string) int {
				if id == idAnon {
					return http.StatusUnauthorized
				}
				if !onTeam(id) {
					return http.StatusForbidden
				}
				if ph == phRunning || ph == phFrozen {
					return http.StatusOK
				}
				// pre/ended: the fresh team has no instance → 404 (existence check precedes the
				// phase gate). A LIVE instance extended after the event ends → 409; that path is
				// covered by TestExtendRejectedAfterEventEnd (3a-ix).
				return http.StatusNotFound
			},
			build: func(m *mtx, id, ph string) (*cookieJar, string, string, string) {
				base := P + "/challenges/" + m.challengeSlug + "/instance"
				if onTeam(id) {
					jar := m.freshOnTeam(id)
					if ph == phRunning || ph == phFrozen {
						_ = do(m.t, m.srv, jar, http.MethodPost, base, "")
					}
					return jar, http.MethodPost, base + "/extend", ""
				}
				return m.jars[id], http.MethodPost, base + "/extend", ""
			}},

		// --- admin ----------------------------------------------------------
		{op: "adminGetEvent", expect: adminRoute(http.StatusOK), build: shared(http.MethodGet, P+"/admin/event", "")},
		{op: "adminUpdateEvent", expect: adminRoute(http.StatusOK),
			build: func(m *mtx, id, ph string) (*cookieJar, string, string, string) {
				start, end, freeze := phaseWindow(ph)
				body := fmt.Sprintf(`{"name":"CTF","description":"d","starts_at":%q,"ends_at":%q`,
					start.Format(time.RFC3339), end.Format(time.RFC3339))
				if freeze != nil {
					body += fmt.Sprintf(`,"freeze_at":%q`, freeze.Format(time.RFC3339))
				}
				body += "}"
				return m.jars[id], http.MethodPatch, P + "/admin/event", body
			}},
		{op: "adminGetStats", expect: adminRoute(http.StatusOK), build: shared(http.MethodGet, P+"/admin/stats", "")},
		{op: "adminListUsers", expect: adminRoute(http.StatusOK), build: shared(http.MethodGet, P+"/admin/users", "")},
		{op: "adminUpdateUser", expect: adminRoute(http.StatusOK),
			build: sharedP(http.MethodPatch, func(m *mtx) string { return P + "/admin/users/" + m.victimUserID }, `{"role":"user"}`)},
		{op: "adminResetPassword", expect: adminRoute(http.StatusNoContent),
			build: sharedP(http.MethodPost, func(m *mtx) string { return P + "/admin/users/" + m.victimUserID + "/password" }, `{"new_password":"resetpass1"}`)},
		{op: "adminListTeams", expect: adminRoute(http.StatusOK), build: shared(http.MethodGet, P+"/admin/teams", "")},
		{op: "adminUpdateTeam", expect: adminRoute(http.StatusOK),
			build: func(m *mtx, id, _ string) (*cookieJar, string, string, string) {
				return m.jars[id], http.MethodPatch, P + "/admin/teams/" + m.victimTeamID, `{"name":"VT` + m.uniq() + `"}`
			}},
		{op: "adminListChallenges", expect: adminRoute(http.StatusOK), build: shared(http.MethodGet, P+"/admin/challenges", "")},
		{op: "adminCreateChallenge", expect: adminRoute(http.StatusCreated),
			build: func(m *mtx, id, _ string) (*cookieJar, string, string, string) {
				return m.jars[id], http.MethodPost, P + "/admin/challenges", standardChallengeBody("AC" + m.uniq())
			}},
		{op: "adminGetChallenge", expect: adminRoute(http.StatusOK),
			build: sharedP(http.MethodGet, func(m *mtx) string { return P + "/admin/challenges/" + m.challengeID }, "")},
		{op: "adminUpdateChallenge", expect: adminRoute(http.StatusOK),
			build: func(m *mtx, id, _ string) (*cookieJar, string, string, string) {
				return m.jars[id], http.MethodPatch, P + "/admin/challenges/" + m.victimChalID, `{"title":"U` + m.uniq() + `"}`
			}},
		// Deleting a challenge is blocked mid-event (running/frozen → phase is Running) unless
		// ?confirm=true, so a live board can't be edited out from under players — pre/ended delete.
		{op: "adminDeleteChallenge",
			expect: func(_ *mtx, id, ph string) int {
				switch id {
				case idAnon:
					return http.StatusUnauthorized
				case idAdmin:
					if ph == phRunning || ph == phFrozen {
						return http.StatusConflict
					}
					return http.StatusNoContent
				default:
					return http.StatusForbidden
				}
			},
			build: func(m *mtx, id, ph string) (*cookieJar, string, string, string) {
				if id == idAdmin && (ph == phPre || ph == phEnded) {
					return m.jars[idAdmin], http.MethodDelete, P + "/admin/challenges/" + m.throwawayChallenge(), "" // 204
				}
				// Running/frozen admin (→409) and every non-admin (→401/403) hit the reusable
				// victim challenge; a 409/401/403 returns before any delete, so it survives.
				return m.jars[id], http.MethodDelete, P + "/admin/challenges/" + m.victimChalID, ""
			}},
		{op: "adminUploadAttachment", expect: adminRoute(http.StatusCreated),
			exec: func(m *mtx, id, _ string) int { // multipart — cannot go through do()
				return uploadFile(m.t, m.srv, m.jars[id],
					P+"/admin/challenges/"+m.victimChalID+"/attachments", "u"+m.uniq()+".bin", []byte("x")).Code
			}},
		{op: "adminDeleteAttachment", expect: adminRoute(http.StatusNoContent),
			build: func(m *mtx, id, _ string) (*cookieJar, string, string, string) {
				if id == idAdmin {
					att := m.uploadAttachment(m.victimChalID)
					return m.jars[idAdmin], http.MethodDelete, P + "/admin/challenges/" + m.victimChalID + "/attachments/" + att, ""
				}
				return m.jars[id], http.MethodDelete, P + "/admin/challenges/" + m.victimChalID + "/attachments/" + m.dummyAttID, ""
			}},
		{op: "adminGetInstance", expect: adminRoute(http.StatusOK),
			build: sharedP(http.MethodGet, func(m *mtx) string { return P + "/admin/challenges/" + m.sharedChalID + "/instance" }, "")},
		{op: "adminDeployInstance", expect: adminRoute(http.StatusOK), // idempotent redeploy of the shared instance
			build: sharedP(http.MethodPost, func(m *mtx) string { return P + "/admin/challenges/" + m.sharedChalID + "/instance" }, "")},
		{op: "adminRestartInstance", expect: adminRoute(http.StatusOK),
			build: sharedP(http.MethodPost, func(m *mtx) string { return P + "/admin/challenges/" + m.sharedChalID + "/instance/restart" }, "")},
		{op: "adminDestroyInstance", expect: adminRoute(http.StatusNoContent),
			build: func(m *mtx, id, _ string) (*cookieJar, string, string, string) {
				if id == idAdmin {
					chal, _ := m.throwawaySharedDeployed()
					return m.jars[idAdmin], http.MethodDelete, P + "/admin/challenges/" + chal + "/instance", ""
				}
				return m.jars[id], http.MethodDelete, P + "/admin/challenges/" + m.sharedChalID + "/instance", ""
			}},
		{op: "adminGetInstanceLogs", expect: adminRoute(http.StatusOK),
			build: sharedP(http.MethodGet, func(m *mtx) string { return P + "/admin/challenges/" + m.sharedChalID + "/instance/logs" }, "")},
		{op: "adminListSubmissions", expect: adminRoute(http.StatusOK), build: shared(http.MethodGet, P+"/admin/submissions", "")},
		{op: "adminListInstances", expect: adminRoute(http.StatusOK), build: shared(http.MethodGet, P+"/admin/instances", "")},
		{op: "adminDestroyInstanceById", expect: adminRoute(http.StatusNoContent),
			build: func(m *mtx, id, _ string) (*cookieJar, string, string, string) {
				if id == idAdmin {
					_, inst := m.throwawaySharedDeployed()
					return m.jars[idAdmin], http.MethodDelete, P + "/admin/instances/" + inst, ""
				}
				return m.jars[id], http.MethodDelete, P + "/admin/instances/" + m.dummyAttID, ""
			}},
	}

	start := time.Now()
	cells := 0
	fails := 0
	for _, ph := range phases {
		for _, r := range routes {
			for _, id := range identities {
				m.setPhase(ph) // reset the window per cell so a mutation cannot bleed the phase forward
				want := r.expect(m, id, ph)
				if want == 0 {
					t.Errorf("MATRIX op=%s identity=%s phase=%s: NO expectation declared (cells must be declared, not defaulted)", r.op, id, ph)
					fails++
					cells++
					continue
				}
				var got int
				if r.exec != nil {
					got = r.exec(m, id, ph)
				} else {
					jar, method, path, body := r.build(m, id, ph)
					got = do(t, m.srv, jar, method, path, body).Code
				}
				cells++
				if got != want {
					t.Errorf("MATRIX op=%s identity=%s phase=%s: expected %d, got %d", r.op, id, ph, want, got)
					fails++
				}
			}
		}
	}
	elapsed := time.Since(start)
	t.Logf("policy matrix: %d routes × %d identities × %d phases = %d cells in %s (%d failures)",
		len(routes), len(identities), len(phases), cells, elapsed.Round(time.Millisecond), fails)
}

// cookieHeader renders the jar's cookies for a WebSocket dial (which does not go
// through the do() helper's jar plumbing).
func (j *cookieJar) cookieHeader() string {
	parts := make([]string, 0, len(j.cookies))
	for _, c := range j.cookies {
		parts = append(parts, c.Name+"="+c.Value)
	}
	return strings.Join(parts, "; ")
}

// TestPolicyMatrixWebSocketIntegration covers the one route the HTTP-status grid cannot:
// the scoreboard WebSocket (/api/v0/ws). Its declared policy is public with NO
// per-connection auth (policy_test.go), so the assertion is that the upgrade is accepted
// — and a hello frame delivered — for every identity (anonymous, a participant, a banned
// live session, an admin) in every phase. A per-connection auth check regressing in would
// reject one of these identities and fail the cell.
func TestPolicyMatrixWebSocketIntegration(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	rdb := testsupport.Redis(t)
	q := gen.New(pool)
	now := time.Now().UTC()
	if _, err := q.CreateEvent(context.Background(), gen.CreateEventParams{
		ID: uuid.Must(uuid.NewV7()), Name: "CTF", Description: "d",
		StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("event: %v", err)
	}
	sessions := auth.NewSessionStore(rdb, time.Hour)
	ev := events.New(q, clock.System())
	sb := scoreboard.New(q, rdb, ev, clock.System())
	hubCtx, hubCancel := context.WithCancel(context.Background())
	defer hubCancel()
	hub := ws.NewHub(discardLog())
	go hub.Run(hubCtx)
	sb.SetBroadcaster(func(s scoreboard.Snapshot) { hub.BroadcastScoreboard(handlers.ToScoreboard(s)) })
	_ = sb.Recompute(context.Background())
	h := handlers.New(handlers.Deps{
		Users: users.New(q, sessions, true), Teams: teams.New(pool, 4), Events: ev,
		Challenges: challenges.New(q, newMemStore()),
		Scoreboard: sb, Recompute: func(ctx context.Context) { _ = sb.Recompute(ctx) },
		Auth: auth.NewEmailPasswordProvider(q, nil), Sessions: sessions,
		Limiter: redisx.NewLimiter(rdb), Audit: audit.New(q, discardLog()), SessionTTL: time.Hour,
	})
	mux := httpserver.New(httpserver.Deps{Log: discardLog(), Handlers: h, Sessions: sessions, BaseOrigin: testOrigin, WSHandler: hub.Handler()})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	jars := map[string]*cookieJar{
		idAnon:      {},
		idMember:    teamUp(t, mux, "wsp", "wsp@x.test", "WsPlayers"),
		idAdmin:     makeAdmin(t, mux, pool, "wsadmin", "wsadmin@x.test"),
		idBannedLiv: teamUp(t, mux, "wsban", "wsban@x.test", "WsBanned"),
	}
	if _, err := pool.Exec(context.Background(), `UPDATE users SET banned=true WHERE email='wsban@x.test'`); err != nil {
		t.Fatalf("ban: %v", err)
	}
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/v0/ws"

	for _, ph := range phases {
		start, end, freeze := phaseWindow(ph)
		if _, err := pool.Exec(context.Background(), `UPDATE events SET starts_at=$1, ends_at=$2, freeze_at=$3`, start, end, freeze); err != nil {
			t.Fatalf("set phase %s: %v", ph, err)
		}
		for _, id := range []string{idAnon, idMember, idAdmin, idBannedLiv} {
			opts := &websocket.DialOptions{HTTPHeader: http.Header{}}
			if ch := jars[id].cookieHeader(); ch != "" {
				opts.HTTPHeader.Set("Cookie", ch)
			}
			dialCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			conn, _, err := websocket.Dial(dialCtx, wsURL, opts)
			if err != nil {
				t.Errorf("MATRIX op=ws identity=%s phase=%s: dial rejected: %v (want public accept)", id, ph, err)
				cancel()
				continue
			}
			if typ := readType(t, conn); typ != "hello" {
				t.Errorf("MATRIX op=ws identity=%s phase=%s: first frame = %q, want hello", id, ph, typ)
			}
			_ = conn.Close(websocket.StatusNormalClosure, "")
			cancel()
		}
	}
}
