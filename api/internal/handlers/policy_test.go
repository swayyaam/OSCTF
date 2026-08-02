package handlers_test

import (
	"os"
	"regexp"
	"sort"
	"testing"
)

// 3a — the authorization policy table. Every operationId in openapi.yaml MUST have an
// entry here; TestPolicyTableCoversEveryRoute derives the route list from the spec so
// a NEW operationId with no policy fails CI the way generate-drift does. The five
// "known" public routes plus the two the audit surfaced (getScoreboard, getUser) are
// declared public EXPLICITLY, never omitted. The WebSocket endpoint is included under
// the sentinel key "ws".
//
// Fields:
//
//	role      — "public" | "user" | "admin" (minimum identity)
//	ownership — "" | "team" | "captain" (resource must belong to the caller's team;
//	            captain = caller must be that team's captain)
//	phases    — event phases in which the op is permitted; nil = any phase
//	note      — rationale for anything unusual (esp. public), so the declaration is the
//	            place a surprising policy is justified, not the test being loosened
type policy struct {
	role      string
	ownership string
	phases    []string
	note      string
}

// wsRoute is the policy key for the WebSocket endpoint (not an OpenAPI operationId).
const wsRoute = "ws"

var policyTable = map[string]policy{
	// --- public (security: []) — all seven declared explicitly ---
	"register":      {role: "public", note: "sign-up"},
	"login":         {role: "public", note: "sign-in"},
	"getEvent":      {role: "public", note: "public event metadata"},
	"listTeams":     {role: "public", note: "public team list"},
	"getTeam":       {role: "public", note: "public team page. FREEZE: solves/points after freeze_at hidden from non-admins EXCEPT the team's own members (own team sees its own progress); admin live. Freeze is NOT lifted at event end — a frozen event stays frozen after ending until an admin clears freeze_at."},
	"getScoreboard": {role: "public", note: "PUBLIC BOARD — surfaced by the 3a audit; not in the original 'five known' list, intentional"},
	"getUser":       {role: "public", note: "PUBLIC PROFILE (username+solves); hidden users 404 to non-admins — surfaced by the 3a audit, intentional. FREEZE: same rule as getTeam — post-freeze_at solves hidden from non-admins except the viewed user's own teammates; admin live."},
	"ws":            {role: "public", note: "WebSocket scoreboard stream — no per-connection auth (v0.1), same public data as getScoreboard"},

	// --- authenticated participant ---
	"getMe":          {role: "user"},
	"logout":         {role: "user"},
	"changePassword": {role: "user"},
	"listChallenges": {role: "user"},
	"getChallenge":   {role: "user"},

	// downloads: authenticated; a hidden/unreleased challenge is 404 (3b).
	"downloadAttachment": {role: "user"},

	// team lifecycle
	"createTeam":           {role: "user"},
	"joinTeam":             {role: "user"},
	"leaveTeam":            {role: "user"},
	"renameTeam":           {role: "user", ownership: "captain"},
	"regenerateInviteCode": {role: "user", ownership: "captain"},

	// submissions + per-team instances (phase- and ownership-gated in the service layer)
	"submitFlag":    {role: "user", phases: []string{"running"}},
	"startInstance": {role: "user", ownership: "team", phases: []string{"running"}},
	"stopInstance":  {role: "user", ownership: "team"},
	// running is code-ENFORCED, not emergent: Scheduler.Extend calls requireRunning after
	// the existence check, so a missing instance is 404 and a live instance outside the
	// running phase is 409 (3a-ix). Without the gate, extend could push a live instance's
	// TTL past the event end during the cleanup window.
	"extendInstance": {role: "user", ownership: "team", phases: []string{"running"}},

	// --- admin ---
	"adminGetEvent":            {role: "admin"},
	"adminUpdateEvent":         {role: "admin"},
	"adminGetStats":            {role: "admin"},
	"adminListUsers":           {role: "admin"},
	"adminGetInstance":         {role: "admin"},
	"adminUpdateUser":          {role: "admin"},
	"adminResetPassword":       {role: "admin"},
	"adminListTeams":           {role: "admin"},
	"adminUpdateTeam":          {role: "admin"},
	"adminListChallenges":      {role: "admin"},
	"adminCreateChallenge":     {role: "admin"},
	"adminGetChallenge":        {role: "admin"},
	"adminUpdateChallenge":     {role: "admin"},
	"adminDeleteChallenge":     {role: "admin"},
	"adminUploadAttachment":    {role: "admin"},
	"adminDeleteAttachment":    {role: "admin"},
	"adminListSubmissions":     {role: "admin"},
	"adminListInstances":       {role: "admin"},
	"adminGetInstanceLogs":     {role: "admin"},
	"adminDeployInstance":      {role: "admin"},
	"adminRestartInstance":     {role: "admin"},
	"adminDestroyInstance":     {role: "admin"},
	"adminDestroyInstanceById": {role: "admin"},
}

var operationIDRe = regexp.MustCompile(`(?m)^\s*operationId:\s*(\S+)`)

// operationIDsFromSpec derives the route list from the OpenAPI document.
func operationIDsFromSpec(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile("../../openapi/openapi.yaml")
	if err != nil {
		t.Fatalf("read openapi.yaml: %v", err)
	}
	var ops []string
	for _, m := range operationIDRe.FindAllStringSubmatch(string(b), -1) {
		ops = append(ops, m[1])
	}
	sort.Strings(ops)
	return ops
}

// TestPolicyTableCoversEveryRoute makes an undeclared route a CI failure: every
// operationId in the spec must have a policy entry, and no entry may be stale (the WS
// sentinel excepted, since it is not an operationId).
func TestPolicyTableCoversEveryRoute(t *testing.T) {
	ops := operationIDsFromSpec(t)
	inSpec := map[string]bool{}
	for _, op := range ops {
		inSpec[op] = true
		if _, ok := policyTable[op]; !ok {
			t.Errorf("operationId %q has NO policy entry — declare its auth policy in policyTable", op)
		}
	}
	for key := range policyTable {
		if key == wsRoute {
			continue
		}
		if !inSpec[key] {
			t.Errorf("policy entry %q is not a real operationId (stale — remove or fix)", key)
		}
	}
	// The seven public routes must be declared public, not omitted.
	for _, pub := range []string{"register", "login", "getEvent", "listTeams", "getTeam", "getScoreboard", "getUser"} {
		if policyTable[pub].role != "public" {
			t.Errorf("known public route %q not declared public", pub)
		}
	}
}
