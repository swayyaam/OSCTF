package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/swayyaam/OSCTF/internal/db/gen"
)

// This file is the AUTH RETURN PATH: the host's decision about what an auth plugin's assertion
// is allowed to mean. An auth plugin decides who gets in, so unlike scoring or notification its
// return value is not trusted — it is validated here, field by field, before it becomes a
// session. The contract is docs/v0.3/04-plugin-interfaces.md ("the auth return-path contract").
//
// The guarantees this file exists to hold, each pinned by a test:
//
//  1. A plugin cannot mint an admin. Provisioning always creates the LOWEST role. No field of an
//     ExternalIdentity sets a role, and nothing here reads one.
//  2. A plugin cannot assert an existing user without a binding the CORE minted — an
//     auth_identities row from a prior permitted login, or a match on an email the provider says
//     it verified. A bare "this is alice@example.com" never takes over Alice's account.
//  3. A plugin cannot set roles. Roles change only through the admin API.
//  4. A malformed or hostile claim fails closed: no session, no provisioning, no partial state.
//
// It is deliberately NOT a defense against a malicious auth plugin, which sits inside the
// authentication trust boundary and can authenticate anyone its identity source vouches for.
// It bounds the blast radius; it does not contain a hostile plugin. Installing one is an
// operator trust decision on the level of replacing the core binary.

// RedirectProvider is the optional capability for external (OAuth/OIDC) logins. A provider
// implements it only if its plugin advertised the "redirect" capability, so a type assertion for
// it is a real capability check rather than a hopeful one — a password-only provider does not
// satisfy this interface at all.
//
// The core owns everything security-relevant around these two calls: it generates and verifies
// the state, issues the session, and maps the returned identity to a local user through Resolve.
// The provider's job is only to talk to its identity source.
type RedirectProvider interface {
	// Begin returns the URL to send the browser to, plus the provider's own round-trip value.
	// hostState is the CSRF state the core minted; the provider must place it in the authorize
	// URL unchanged, and the caller verifies that it did.
	Begin(ctx context.Context, hostState, redirectURI string) (authorizeURL, providerState string, err error)
	// Complete exchanges the callback parameters for an asserted identity.
	Complete(ctx context.Context, state string, params map[string]string) (ExternalIdentity, error)
}

// ProvisionPolicy governs what an external login may do when it carries no existing binding.
// Every policy resolves an existing binding the same way; they differ only in whether an
// unbound identity may attach to an account, and whether it may create one.
//
//	                 known binding   verified email matches   neither
//	open             log in          bind + log in            create participant
//	invite-only      log in          bind + log in            reject
//	off              log in          reject                   reject
type ProvisionPolicy string

const (
	// ProvisionOpen lets a verified identity with no account create one (lowest role).
	ProvisionOpen ProvisionPolicy = "open"
	// ProvisionInviteOnly binds only to accounts that already exist — the "invite" is an admin
	// having created the account. It never creates a user, so a compromised or careless provider
	// cannot manufacture accounts.
	ProvisionInviteOnly ProvisionPolicy = "invite-only"
	// ProvisionOff resolves only identities already bound; it neither matches nor creates.
	ProvisionOff ProvisionPolicy = "off"
)

// ParseProvisionPolicy validates the configured policy. An unrecognised value is an error
// rather than a silent default: guessing here would pick a security posture on the operator's
// behalf.
func ParseProvisionPolicy(s string) (ProvisionPolicy, error) {
	switch p := ProvisionPolicy(strings.ToLower(strings.TrimSpace(s))); p {
	case ProvisionOpen, ProvisionInviteOnly, ProvisionOff:
		return p, nil
	default:
		return "", fmt.Errorf("auth: unknown provisioning policy %q (want open, invite-only, or off)", s)
	}
}

// ExternalIdentity is the host-side view of what an auth plugin asserted. It is a CLAIM, not a
// grant; what it is allowed to mean is decided in Resolve.
type ExternalIdentity struct {
	Subject       string            // stable unique id at the provider; required
	Email         string            // may be empty
	Username      string            // a SUGGESTION; the host picks the actual username
	EmailVerified bool              // the provider asserts it verified Email (ABI 1.1)
	Claims        map[string]string // informational only, and screened below
}

// ErrExternalRejected is what every rejection matches. The caller renders one generic failure
// so a login response never reveals whether an account exists, is banned, or was refused by
// policy; the specific reason goes to the log instead.
var ErrExternalRejected = errors.New("auth: external login rejected")

// RejectionError carries the reason for the operator's log while still matching
// ErrExternalRejected for the caller.
type RejectionError struct{ Reason string }

func (e *RejectionError) Error() string        { return "auth: external login rejected: " + e.Reason }
func (e *RejectionError) Is(target error) bool { return target == ErrExternalRejected }

func reject(reason string) error { return &RejectionError{Reason: reason} }

// reservedClaimKeys are keys whose presence means the plugin is trying to assert authority it
// does not have. The host would ignore them regardless — ignoring silently would leave a plugin
// author believing the field works, and would hide a hostile one, so their presence is a hard
// rejection instead.
var reservedClaimKeys = map[string]struct{}{
	"admin": {}, "is_admin": {}, "isadmin": {},
	"role": {}, "roles": {},
	"user_id": {}, "userid": {}, "uid": {},
	"scope": {}, "scopes": {},
	"banned": {}, "hidden": {},
}

// maxSubjectLen bounds a subject before it reaches the database. Real subjects are well under
// this; a longer one is a malformed or hostile claim.
const maxSubjectLen = 255

// roleParticipant is the lowest role, and the ONLY role external provisioning ever assigns.
const roleParticipant = "user"

// noPasswordHash is stored as the password of an externally-provisioned account. It cannot parse
// as a PHC argon2id string, so VerifyPassword returns ErrInvalidHash and the credential provider
// turns that into ErrInvalidCredentials — an SSO account cannot be password-logged-into, and the
// failure is the ordinary one, not an error. Pinned by TestNoPasswordHashNeverVerifies.
const noPasswordHash = "!"

// usernameRe mirrors the registration rule; a derived username must satisfy the same constraint
// a human-chosen one does.
var usernameRe = regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`)

const (
	usernameMinLen = 3
	usernameMaxLen = 32
	// provisionAttempts bounds the derived-username search. Each attempt is one INSERT that may
	// lose a unique-violation race; a handful is plenty, and a bound means a pathological
	// collision cannot spin.
	provisionAttempts = 8
)

// ExternalResolver maps a plugin's assertion to a local user under policy.
type ExternalResolver struct {
	q      *gen.Queries
	policy ProvisionPolicy
	log    *slog.Logger
}

// NewExternalResolver builds the resolver. policy must already be validated.
func NewExternalResolver(q *gen.Queries, policy ProvisionPolicy, log *slog.Logger) *ExternalResolver {
	if log == nil {
		log = slog.Default()
	}
	return &ExternalResolver{q: q, policy: policy, log: log}
}

// Policy reports the configured provisioning policy.
func (r *ExternalResolver) Policy() ProvisionPolicy { return r.policy }

// Resolve turns an asserted external identity into a local user, or rejects it. Every rejection
// is logged with the provider and reason and returned as ErrExternalRejected.
//
// The log deliberately carries no identity fields — not the subject, not the email. A rejected
// login is exactly the case where the address may belong to someone who is not the person
// attempting it, and operator logs are not the place to accumulate that.
func (r *ExternalResolver) Resolve(ctx context.Context, provider string, id ExternalIdentity) (gen.User, error) {
	u, err := r.resolve(ctx, provider, id)
	if err != nil {
		var re *RejectionError
		if errors.As(err, &re) {
			r.log.Warn("external login rejected", "provider", provider, "policy", string(r.policy), "reason", re.Reason)
		}
		return gen.User{}, err
	}
	return u, nil
}

func (r *ExternalResolver) resolve(ctx context.Context, provider string, id ExternalIdentity) (gen.User, error) {
	if strings.TrimSpace(provider) == "" {
		return gen.User{}, reject("empty provider name")
	}
	subject := strings.TrimSpace(id.Subject)
	if subject == "" {
		return gen.User{}, reject("identity carries no subject")
	}
	if len(subject) > maxSubjectLen {
		return gen.User{}, reject("subject exceeds the maximum length")
	}
	for k := range id.Claims {
		if _, reserved := reservedClaimKeys[strings.ToLower(strings.TrimSpace(k))]; reserved {
			return gen.User{}, reject("claims carry the reserved key " + strings.ToLower(strings.TrimSpace(k)))
		}
	}

	// 1. An existing core-minted binding. This is the only path that resolves without consulting
	//    the email at all, which is why a bound account keeps working even if the provider later
	//    stops asserting verification.
	ai, err := r.q.GetAuthIdentity(ctx, gen.GetAuthIdentityParams{Provider: provider, Subject: subject})
	switch {
	case err == nil:
		u, uerr := r.q.GetUserByID(ctx, ai.UserID)
		if uerr != nil {
			return gen.User{}, fmt.Errorf("auth: loading bound user: %w", uerr)
		}
		if u.Banned {
			return gen.User{}, reject("bound account is banned")
		}
		return u, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return gen.User{}, fmt.Errorf("auth: looking up external identity: %w", err)
	}

	// 2. No binding. Everything below is gated on policy and on a verified email.
	if r.policy == ProvisionOff {
		return gen.User{}, reject("identity is not bound and provisioning is off")
	}
	email := strings.ToLower(strings.TrimSpace(id.Email))
	if email == "" {
		return gen.User{}, reject("identity is not bound and carries no email")
	}
	if !id.EmailVerified {
		// The load-bearing check. Without it, a provider that lets a user assert any address
		// turns into account takeover against every account whose address is guessable.
		return gen.User{}, reject("identity is not bound and its email is not provider-verified")
	}
	if _, perr := mail.ParseAddress(email); perr != nil || strings.ContainsAny(email, " <>") {
		return gen.User{}, reject("identity is not bound and its email is malformed")
	}

	u, err := r.q.GetUserByEmail(ctx, email)
	switch {
	case err == nil:
		if u.Banned {
			return gen.User{}, reject("matched account is banned")
		}
		if berr := r.bind(ctx, provider, subject, u.ID); berr != nil {
			return gen.User{}, berr
		}
		return u, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return gen.User{}, fmt.Errorf("auth: looking up user by email: %w", err)
	}

	if r.policy != ProvisionOpen {
		return gen.User{}, reject("no account for this verified email and policy is " + string(r.policy))
	}
	return r.provision(ctx, provider, subject, email, id.Username)
}

// bind records the core-minted (provider, subject) → user link.
func (r *ExternalResolver) bind(ctx context.Context, provider, subject string, userID uuid.UUID) error {
	_, err := r.q.CreateAuthIdentity(ctx, gen.CreateAuthIdentityParams{
		Provider: provider, Subject: subject, UserID: userID,
	})
	if err != nil && !isUniqueViolation(err) {
		return fmt.Errorf("auth: binding external identity: %w", err)
	}
	// A unique violation means a concurrent login for the same (provider, subject) bound first.
	// The binding it wrote is the one we would have written, so this is success, not a conflict.
	return nil
}

// provision creates the local account for a verified identity that has none, at the lowest role.
func (r *ExternalResolver) provision(ctx context.Context, provider, subject, email, suggested string) (gen.User, error) {
	base := sanitizeUsername(suggested)
	if base == "" {
		if at := strings.IndexByte(email, '@'); at > 0 {
			base = sanitizeUsername(email[:at])
		}
	}
	if base == "" {
		base = "user"
	}

	for attempt := range provisionAttempts {
		id, err := uuid.NewV7()
		if err != nil {
			return gen.User{}, fmt.Errorf("auth: generating user id: %w", err)
		}
		u, err := r.q.CreateUser(ctx, gen.CreateUserParams{
			ID:           id,
			Username:     candidateUsername(base, attempt),
			Email:        email,
			PasswordHash: noPasswordHash,
			Role:         roleParticipant, // never anything else, whatever the claim said
			Hidden:       false,
		})
		if err == nil {
			if berr := r.bind(ctx, provider, subject, u.ID); berr != nil {
				return gen.User{}, berr
			}
			return u, nil
		}
		if !isUniqueViolation(err) {
			return gen.User{}, fmt.Errorf("auth: provisioning user: %w", err)
		}
		// The violation was on the username or the email. Re-read by email: if a concurrent
		// login (or a registration) just took it, that account is the one this identity should
		// bind to — the same outcome the matched-email branch above would have produced.
		if existing, gerr := r.q.GetUserByEmail(ctx, email); gerr == nil {
			if existing.Banned {
				return gen.User{}, reject("matched account is banned")
			}
			if berr := r.bind(ctx, provider, subject, existing.ID); berr != nil {
				return gen.User{}, berr
			}
			return existing, nil
		} else if !errors.Is(gerr, pgx.ErrNoRows) {
			return gen.User{}, fmt.Errorf("auth: re-checking email after conflict: %w", gerr)
		}
		// Email is still free, so the collision was the username: try the next candidate.
	}
	return gen.User{}, reject("could not derive an unused username")
}

// candidateUsername returns base for the first attempt and a numbered variant afterwards,
// always within the length rule registration enforces.
func candidateUsername(base string, attempt int) string {
	if attempt == 0 {
		return base
	}
	suffix := fmt.Sprintf("-%d", attempt+1)
	if len(base)+len(suffix) > usernameMaxLen {
		base = base[:usernameMaxLen-len(suffix)]
	}
	return base + suffix
}

// sanitizeUsername reduces a provider's suggestion to something the registration rule accepts,
// or "" if nothing usable survives. The suggestion is untrusted input like any other claim.
func sanitizeUsername(s string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '.', r == '-':
			b.WriteRune(r)
		}
	}
	out := b.String()
	if len(out) > usernameMaxLen {
		out = out[:usernameMaxLen]
	}
	if len(out) < usernameMinLen || !usernameRe.MatchString(out) {
		return ""
	}
	return out
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
