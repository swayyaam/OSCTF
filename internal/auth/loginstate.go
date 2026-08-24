package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// LoginStateCookie carries the state value bound to one in-flight external login. It exists so
// the callback can prove it reached the SAME browser that started the login: the query parameter
// alone is attacker-suppliable, and a state that only lives server-side would still accept a
// callback replayed into someone else's browser (login CSRF — the victim silently ends up signed
// in as the attacker).
const LoginStateCookie = "osctf_login_state"

// LoginStateTTL bounds one redirect round trip. Short on purpose: it is the window in which a
// captured state would be useful. The store and the bound cookie share this one value so they
// cannot expire at different times.
const LoginStateTTL = 10 * time.Minute

// ErrNoLoginState means the state was never issued, already used, or expired. All three are the
// same to a caller: the login does not proceed.
var ErrNoLoginState = errors.New("auth: no such login state")

// LoginState is the core-owned record of one in-flight external login.
//
// The core mints this value, not the provider. A provider-chosen state would put CSRF protection
// inside the component the return-path contract says not to trust; ProviderState is carried
// alongside purely so it can be handed back to the plugin on completion.
type LoginState struct {
	Provider      string `json:"provider"`
	ProviderState string `json:"provider_state"`
}

// LoginStateStore keeps in-flight external logins in Redis under a short TTL. Entries are
// SINGLE-USE: consuming one deletes it, so a captured callback URL cannot be replayed.
type LoginStateStore struct {
	rdb *redis.Client
	ttl time.Duration
}

// NewLoginStateStore builds the store. ttl should be short — a login redirect is a round trip
// through the provider, not a session.
func NewLoginStateStore(rdb *redis.Client, ttl time.Duration) *LoginStateStore {
	return &LoginStateStore{rdb: rdb, ttl: ttl}
}

func loginStateKey(token string) string { return "login:state:" + token }

// Create mints a state token for a login against provider and stores it under the TTL. The
// returned token goes to the provider as `state` AND into the bound cookie.
func (s *LoginStateStore) Create(ctx context.Context, provider, providerState string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("auth: generating login state: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	payload, err := json.Marshal(LoginState{Provider: provider, ProviderState: providerState})
	if err != nil {
		return "", fmt.Errorf("auth: encoding login state: %w", err)
	}
	if err := s.rdb.Set(ctx, loginStateKey(token), payload, s.ttl).Err(); err != nil {
		return "", fmt.Errorf("auth: storing login state: %w", err)
	}
	return token, nil
}

// Consume fetches and DELETES the state in one round trip, so a state is usable exactly once.
// An absent, expired, or already-consumed token returns ErrNoLoginState.
func (s *LoginStateStore) Consume(ctx context.Context, token string) (LoginState, error) {
	if token == "" {
		return LoginState{}, ErrNoLoginState
	}
	payload, err := s.rdb.GetDel(ctx, loginStateKey(token)).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return LoginState{}, ErrNoLoginState
		}
		// A Redis outage fails the login CLOSED. There is no in-memory fallback: one would make
		// the single-use guarantee depend on which replica served the request.
		return LoginState{}, fmt.Errorf("auth: reading login state: %w", err)
	}
	var st LoginState
	if err := json.Unmarshal(payload, &st); err != nil {
		return LoginState{}, fmt.Errorf("auth: decoding login state: %w", err)
	}
	return st, nil
}
