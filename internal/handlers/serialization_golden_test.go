package handlers

// Serialization golden tests — the empirically hottest seam (the null-vs-[] bug
// hit both sides of the wire twice, plus a WS/REST field-order divergence).
//
// Two guarantees per response type:
//  1. EMPTY: with zero rows, every list encodes as [] (never null) and every
//     required map as {} — at every level, including nested lists and lists
//     inside optional objects. The empty fixtures are built through the REAL
//     mappers wherever one exists, so a mapper regressing to a nil slice
//     (`var out []T`) flips the golden and fails here.
//  2. FULL: the complete wire shape is pinned against a checked-in golden, so a
//     field rename, reorder, JSON-tag change, or type change fails.
//
// Optional/nullable fields are asserted to encode consistently when absent —
// omitempty pointers omit (matching TS `field?: T`), plain pointers emit null
// (matching TS `field?: T | null`). Regenerate goldens with UPDATE_GOLDEN=1.
//
// Endpoint-level zero-row coverage (exercising the handler's own make() guard
// for the inline-built page wrappers) lives in the integration test; this file
// pins the mapper + wire-shape layer and always runs.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/osctf/platform/internal/apigen"
	"github.com/osctf/platform/internal/challenges"
	"github.com/osctf/platform/internal/scoreboard"
	"github.com/osctf/platform/internal/teams"
)

var goldenTime = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func uidN(n byte) openapi_types.UUID {
	var u openapi_types.UUID
	for i := range u {
		u[i] = n
	}
	return u
}

func sptr(s string) *string { return &s }
func iptr(i int) *int       { return &i }
func tptr(t time.Time) *time.Time {
	return &t
}

// goldenJSON marshals v (indented, stable) and compares against
// testdata/golden/<name>.json. UPDATE_GOLDEN=1 rewrites the file. Returns the
// marshaled bytes so callers can additionally scan them.
func goldenJSON(t *testing.T, name string, v any) []byte {
	t.Helper()
	got, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal %s: %v", name, err)
	}
	got = append(got, '\n')
	path := filepath.Join("testdata", "golden", name+".json")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return got
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s (run with UPDATE_GOLDEN=1 to create): %v", path, err)
	}
	if !bytes.Equal(bytes.TrimRight(want, "\n"), bytes.TrimRight(got, "\n")) {
		t.Errorf("golden %s mismatch (UPDATE_GOLDEN=1 to accept):\n--- want ---\n%s\n--- got ---\n%s", name, want, got)
	}
	return got
}

// arrayKeys are JSON object keys that must ALWAYS encode as an array — never
// null. objectKeys must always encode as an object (the container_env map).
// A nil Go slice/map under any of these keys is the exact regression this file
// guards, and the scanner reports it with the field name.
var arrayKeys = map[string]bool{
	"items": true, "standings": true, "members": true, "solves": true,
	"attachments": true, "writable_paths": true, "unadopted": true,
	"unadopted_networks": true,
}
var objectKeys = map[string]bool{"container_env": true}

// assertNoNullContainers walks decoded JSON and fails if any arrayKeys value is
// null (should be []) or any objectKeys value is null (should be {}). This is a
// readable, field-named backstop on top of the golden byte comparison.
func assertNoNullContainers(t *testing.T, name string, raw []byte) {
	t.Helper()
	var root any
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatalf("%s: unmarshal for null-scan: %v", name, err)
	}
	var walk func(v any)
	walk = func(v any) {
		switch node := v.(type) {
		case map[string]any:
			for k, child := range node {
				if child == nil && arrayKeys[k] {
					t.Errorf("%s: field %q encoded as null; must be [] (nil-slice regression)", name, k)
				}
				if child == nil && objectKeys[k] {
					t.Errorf("%s: field %q encoded as null; must be {} (nil-map regression)", name, k)
				}
				walk(child)
			}
		case []any:
			for _, child := range node {
				walk(child)
			}
		}
	}
	walk(root)
}

// ---- EMPTY variants: built through the real mappers where one exists. --------

func TestGoldenEmpty(t *testing.T) {
	cases := []struct {
		name string
		v    any
	}{
		// Mapper-backed: zero domain input must yield [] / {}, not null.
		{"scoreboard.empty", ToScoreboard(scoreboard.Snapshot{})},
		{"challenge_admin.empty", toChallengeAdmin(challenges.Full{})},
		{"challenge_detail.empty", toChallengeDetail(challenges.Detail{})},
		{"team_mine.empty", toTeamMine(teams.Team{})},

		// Inline page wrappers: pin the intended empty wire form ([] items).
		// The handler's own make() guard is exercised end-to-end in the
		// integration zero-row test.
		{"challenge_admin_page.empty", apigen.ChallengeAdminPage{Items: []apigen.ChallengeAdmin{}, Total: 0, Page: 1, PerPage: 20}},
		{"team_admin_page.empty", apigen.TeamAdminPage{Items: []apigen.TeamAdmin{}, Total: 0, Page: 1, PerPage: 20}},
		{"user_admin_page.empty", apigen.UserAdminPage{Items: []apigen.UserAdmin{}, Total: 0, Page: 1, PerPage: 20}},
		{"submission_admin_page.empty", apigen.SubmissionAdminPage{Items: []apigen.SubmissionAdmin{}, Total: 0, Page: 1, PerPage: 20}},

		// List-inside-optional-object: routed through omitEmptySlice (as the
		// handler does), an empty unadopted list must OMIT the field, never emit
		// []. This exercises the structural guard from 4a-i.
		{"admin_instance_list.empty_no_unadopted", apigen.AdminInstanceList{
			Items:             []apigen.AdminInstance{},
			Unadopted:         omitEmptySlice([]apigen.UnadoptedContainer{}),
			UnadoptedNetworks: omitEmptySlice([]apigen.UnadoptedNetwork{}),
		}},

		// Bare top-level arrays.
		{"list_challenges.empty", []apigen.Challenge{}},
		{"list_teams.empty", []apigen.TeamSummary{}},

		// Freeze-sensitive detail types: no team/invite (optional omitted/null), empty lists.
		{"public_user.empty", apigen.PublicUser{Id: uidN(1), Username: "u", Solves: []apigen.Solve{}}},
		{"team_detail.empty", apigen.TeamDetail{Id: uidN(1), Name: "t", Members: []apigen.Member{}, Points: 0, Solves: []apigen.Solve{}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw := goldenJSON(t, c.name, c.v)
			assertNoNullContainers(t, c.name, raw)
		})
	}
}

// ---- FULL variants: every field populated, every optional present. -----------

func TestGoldenFull(t *testing.T) {
	cases := []struct {
		name string
		v    any
	}{
		{"scoreboard.full", apigen.Scoreboard{
			Frozen:      true,
			GeneratedAt: goldenTime,
			Standings: []apigen.ScoreboardEntry{{
				Rank: iptr(1), TeamId: uidN(2), Name: "Team Alpha", Points: 500,
				LastSolveAt: tptr(goldenTime), Solves: 3, Banned: false,
			}},
		}},
		{"challenge_admin.full", apigen.ChallengeAdmin{
			Id: uidN(3), Slug: "sqli", Title: "SQL Injection", Category: apigen.Category("web"),
			Description: "desc", Difficulty: diffPtr(sptr("medium")), Kind: apigen.ChallengeKind("container"),
			Type: "standard",
			Flag: "osctf{x}", FlagCaseInsensitive: false, Scoring: apigen.ScoringMode("dynamic"),
			PointsInitial: 500, PointsMin: iptr(100), Decay: iptr(50), MaxAttempts: iptr(10),
			Visible: true, Image: sptr("img:1"), InternalPort: iptr(80), MemLimitMb: 256, CpuMillis: 500,
			ContainerEnv: map[string]string{"K": "V"}, ConnectionTemplate: sptr("nc {host} {port}"),
			Instancing: apigen.Instancing("per_team"), FlagMode: apigen.FlagMode("per_instance"),
			InstanceTtlSeconds: iptr(3600), Egress: false,
			WritablePaths: []string{"/tmp"},
			Attachments:   []apigen.AttachmentAdmin{{Id: uidN(4), Filename: "f.zip", SizeBytes: 10, ContentType: "application/zip", CreatedAt: goldenTime}},
			Solves:        7, CurrentPoints: 420, CreatedAt: goldenTime, UpdatedAt: goldenTime,
		}},
		{"challenge_detail.full", apigen.ChallengeDetail{
			Id: uidN(3), Slug: "sqli", Title: "SQL Injection", Category: apigen.Category("web"),
			Difficulty: diffPtr(sptr("medium")), Points: 420, Solves: 7, SolvedByMe: true,
			Kind: apigen.ChallengeKind("container"), Instancing: apigen.Instancing("per_team"),
			FlagMode: apigen.FlagMode("per_instance"), HasInstance: true, Description: "desc",
			Attachments: []apigen.Attachment{{Id: uidN(4), Filename: "f.zip", SizeBytes: 10}},
			MaxAttempts: iptr(10), AttemptsUsed: 2,
		}},
		{"challenge.full", apigen.Challenge{
			Id: uidN(3), Slug: "sqli", Title: "SQL Injection", Category: apigen.Category("web"),
			Difficulty: diffPtr(sptr("medium")), Points: 420, Solves: 7, SolvedByMe: true,
			Kind: apigen.ChallengeKind("container"), Instancing: apigen.Instancing("per_team"),
			HasInstance: true, ConnectionInfo: sptr("nc host 30000"),
		}},
		{"team_mine.full", apigen.TeamMine{
			Id: uidN(5), Name: "Alpha", InviteCode: "INV123", CaptainId: uidN(6),
			Members: []apigen.Member{{Id: uidN(6), Username: "cap"}, {Id: uidN(7), Username: "mate"}},
		}},
		{"team_detail.full", apigen.TeamDetail{
			Id: uidN(5), Name: "Alpha", InviteCode: sptr("INV123"), Rank: iptr(2), Points: 300,
			Members: []apigen.Member{{Id: uidN(6), Username: "cap"}},
			Solves:  []apigen.Solve{{ChallengeId: uidN(3), Slug: "sqli", Title: "SQL Injection", Category: apigen.Category("web"), Points: 100, SolvedAt: goldenTime, Username: sptr("cap")}},
		}},
		{"public_user.full", apigen.PublicUser{
			Id: uidN(8), Username: "player", Team: &apigen.TeamRef{Id: uidN(5), Name: "Alpha"},
			Solves: []apigen.Solve{{ChallengeId: uidN(3), Slug: "sqli", Title: "SQL Injection", Category: apigen.Category("web"), Points: 100, SolvedAt: goldenTime}},
		}},
		{"challenge_admin_page.full", apigen.ChallengeAdminPage{
			Items: []apigen.ChallengeAdmin{{Id: uidN(3), Slug: "sqli", Title: "T", Category: apigen.Category("web"), Description: "d", Kind: apigen.ChallengeKind("static"), Type: "standard", Flag: "f", Scoring: apigen.ScoringMode("static"), PointsInitial: 100, MemLimitMb: 0, CpuMillis: 0, ContainerEnv: map[string]string{}, Instancing: apigen.Instancing("none"), FlagMode: apigen.FlagMode("static"), WritablePaths: []string{}, Attachments: []apigen.AttachmentAdmin{}, Solves: 0, CurrentPoints: 100, CreatedAt: goldenTime, UpdatedAt: goldenTime}},
			Total: 1, Page: 1, PerPage: 20,
		}},
		{"team_admin_page.full", apigen.TeamAdminPage{
			Items: []apigen.TeamAdmin{{Id: uidN(5), Name: "Alpha", MemberCount: 3, Banned: false, Hidden: false, CreatedAt: goldenTime}},
			Total: 1, Page: 1, PerPage: 20,
		}},
		{"user_admin_page.full", apigen.UserAdminPage{
			Items: []apigen.UserAdmin{{Id: uidN(8), Username: "player", Email: "p@e.com", Role: apigen.Role("user"), Banned: false, Hidden: false, Team: &apigen.TeamRef{Id: uidN(5), Name: "Alpha"}, CreatedAt: goldenTime}},
			Total: 1, Page: 1, PerPage: 20,
		}},
		{"submission_admin_page.full", apigen.SubmissionAdminPage{
			Items: []apigen.SubmissionAdmin{{Id: uidN(9), Challenge: apigen.ChallengeRef{Id: uidN(3), Slug: "sqli", Title: "T"}, Team: apigen.TeamRef{Id: uidN(5), Name: "Alpha"}, User: apigen.Member{Id: uidN(8), Username: "player"}, Provided: "osctf{x}", Correct: true, Ip: sptr("203.0.113.5"), CreatedAt: goldenTime}},
			Total: 1, Page: 1, PerPage: 20,
		}},
		{"admin_instance_list.full", apigen.AdminInstanceList{
			Items: []apigen.AdminInstance{{
				Id: uidN(10), ChallengeId: uidN(3), ChallengeSlug: sptr("sqli"), TeamId: &[]openapi_types.UUID{uidN(5)}[0], TeamName: sptr("Alpha"),
				State: apigen.InstanceState("running"), HostPort: iptr(30000), Network: sptr("osctf-team-5"),
				StartedAt: tptr(goldenTime), ExpiresAt: tptr(goldenTime), LastHealthAt: tptr(goldenTime), Error: nil,
			}},
			Unadopted:         &[]apigen.UnadoptedContainer{{ContainerId: "abc123", Image: "img:1", CreatedAt: goldenTime}},
			UnadoptedNetworks: &[]apigen.UnadoptedNetwork{{NetworkId: "net123", Name: "osctf-team-orphan"}},
		}},
		{"problem.full", apigen.Problem{
			Type: "https://osctf.dev/errors/validation", Title: "Validation failed", Status: 422,
			Detail: sptr("One or more fields are invalid."), Errors: &map[string][]string{"email": {"must be a valid email address"}}, RequestId: sptr("req-1"),
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			goldenJSON(t, c.name, c.v)
		})
	}
}

// TestOmitEmptySlice pins the structural guard for optional list fields (4a-i):
// an empty slice becomes nil (so an omitempty pointer field is OMITTED, matching
// the dashboard's optional-not-nullable typing), a non-empty slice is preserved.
func TestOmitEmptySlice(t *testing.T) {
	if got := omitEmptySlice([]apigen.UnadoptedContainer{}); got != nil {
		t.Errorf("omitEmptySlice(empty) = %v, want nil (so the field omits, not [])", got)
	}
	if got := omitEmptySlice([]int(nil)); got != nil {
		t.Errorf("omitEmptySlice(nil) = %v, want nil", got)
	}
	got := omitEmptySlice([]int{1, 2})
	if got == nil || len(*got) != 2 {
		t.Fatalf("omitEmptySlice([1,2]) = %v, want &[1 2]", got)
	}

	// End-to-end at the type level: a list built through the helper omits at zero.
	raw, err := json.Marshal(apigen.AdminInstanceList{
		Items:     []apigen.AdminInstance{},
		Unadopted: omitEmptySlice([]apigen.UnadoptedContainer{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("unadopted")) {
		t.Errorf("empty unadopted must be omitted, got: %s", raw)
	}
}
