package sdk

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/swayyaam/OSCTF/internal/plugin/pluginpb"
)

// Identity is what an auth plugin returns for an authenticated principal.
//
// IMPORTANT — an Identity is a CLAIM, not a grant, and this SDK surface is deliberately ahead of
// the host. As of this build NO auth plugin can be loaded: the plugin registrar's auth arm is nil
// until milestone M3, and M3 ships RETURN-PATH VALIDATION first (docs/v0.3/10-milestones.md). When
// it lands, the host maps an Identity to a local user under HOST policy — a plugin cannot set a
// role, mint an admin, or self-provision. Claims in particular is informational: putting
// "role":"admin" (or anything like it) in Claims grants nothing. Do not build a plugin that
// assumes an Identity confers authority; the defense that constrains it is the host's, by design,
// and it is not the plugin's to bypass. Until M3 you can develop and contract-test an auth plugin,
// but the host will not call it.
type Identity struct {
	Subject  string            // stable unique id at the provider (the "sub")
	Email    string            // may be empty
	Username string            // suggested username; the host decides the actual one
	Claims   map[string]string // informational only — see the type doc; NOT authority

	// EmailVerified reports whether YOU verified this address (OIDC's email_verified claim).
	// Set it truthfully: the host requires it before binding a login to an EXISTING account by
	// email, so leaving it false is the safe default and simply means the host will not match on
	// email. Do not set it because the address merely looks plausible — an address you did not
	// verify lets a user assert someone else's account. Added in ABI 1.1.
	EmailVerified bool
}

// BeginRedirect is what a redirect-capability auth plugin returns to start an external login: the
// URL to send the browser to, and an opaque state the host round-trips back to Complete.
type BeginRedirect struct {
	AuthorizeURL string
	State        string
}

// PasswordAuth is the "password" capability: verify a submitted identifier/secret directly. An
// auth plugin implements this, RedirectAuth, or both — Serve derives the advertised capabilities
// from which it satisfies.
type PasswordAuth interface {
	Info() Info
	// Authenticate verifies the credential. A nil error with an Identity means success; an error
	// means the credential was rejected or could not be checked (the host treats both as a failed
	// login — it never falls open). See the Identity doc: success is a claim, not a grant.
	Authenticate(identifier, secret string) (Identity, error)
}

// RedirectAuth is the "redirect" capability: an external (e.g. OIDC) login. Begin starts it,
// Complete finishes it after the provider redirects back.
type RedirectAuth interface {
	Info() Info
	Begin(redirectURI string) (BeginRedirect, error)
	Complete(state string, params map[string]string) (Identity, error)
}

// newAuthAdapter builds the auth adapter from an impl that satisfies PasswordAuth and/or
// RedirectAuth, panicking if it satisfies neither (Serve must not start a plugin that answers no
// auth method). Capabilities are derived from what it satisfies — never author-declared.
func newAuthAdapter(impl any) pluginpb.AuthServer {
	pw, _ := impl.(PasswordAuth)
	rd, _ := impl.(RedirectAuth)
	base, ok := impl.(interface{ Info() Info })
	if !ok || (pw == nil && rd == nil) {
		panic("sdk.Serve(Auth, …): impl must implement sdk.PasswordAuth and/or sdk.RedirectAuth")
	}
	return &authAdapter{pw: pw, rd: rd, base: base}
}

// authAdapter translates the generated gRPC AuthServer into calls on the author's PasswordAuth /
// RedirectAuth. A method for an unimplemented capability returns Unimplemented — the host only
// calls advertised capabilities, but this fails closed if it ever calls one that is absent.
type authAdapter struct {
	pluginpb.UnimplementedAuthServer
	pw   PasswordAuth
	rd   RedirectAuth
	base interface{ Info() Info }
}

func (a *authAdapter) Info(context.Context, *pluginpb.InfoRequest) (*pluginpb.InfoResponse, error) {
	var caps []string
	if a.pw != nil {
		caps = append(caps, "password")
	}
	if a.rd != nil {
		caps = append(caps, "redirect")
	}
	return infoResponse(a.base.Info(), pluginpb.PluginType_PLUGIN_TYPE_AUTH, caps...), nil
}

func (a *authAdapter) Authenticate(_ context.Context, req *pluginpb.AuthenticateRequest) (*pluginpb.Identity, error) {
	if a.pw == nil {
		return nil, status.Error(codes.Unimplemented, "this auth plugin does not support the password capability")
	}
	id, err := a.pw.Authenticate(req.GetIdentifier(), req.GetSecret())
	if err != nil {
		return nil, err
	}
	return toWireIdentity(id), nil
}

func (a *authAdapter) Begin(_ context.Context, req *pluginpb.BeginRequest) (*pluginpb.BeginResponse, error) {
	if a.rd == nil {
		return nil, status.Error(codes.Unimplemented, "this auth plugin does not support the redirect capability")
	}
	b, err := a.rd.Begin(req.GetRedirectUri())
	if err != nil {
		return nil, err
	}
	return &pluginpb.BeginResponse{AuthorizeUrl: b.AuthorizeURL, State: b.State}, nil
}

func (a *authAdapter) Complete(_ context.Context, req *pluginpb.CompleteRequest) (*pluginpb.Identity, error) {
	if a.rd == nil {
		return nil, status.Error(codes.Unimplemented, "this auth plugin does not support the redirect capability")
	}
	id, err := a.rd.Complete(req.GetState(), req.GetParams())
	if err != nil {
		return nil, err
	}
	return toWireIdentity(id), nil
}

func toWireIdentity(i Identity) *pluginpb.Identity {
	return &pluginpb.Identity{
		Subject:       i.Subject,
		Email:         i.Email,
		Username:      i.Username,
		Claims:        i.Claims,
		EmailVerified: i.EmailVerified,
	}
}
