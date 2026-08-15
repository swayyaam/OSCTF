package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// CookieName is the session cookie's name.
const CookieName = "osctf_session"

// Session is the server-side session state stored in Redis.
type Session struct {
	Token     string
	UserID    uuid.UUID
	Role      string
	CreatedAt time.Time
	IP        string
	UA        string
}

// ErrNoSession is returned when a token does not resolve to a live session.
var ErrNoSession = errors.New("auth: no such session")

// SessionStore manages sessions in Redis: `sess:{token}` hashes with a sliding
// TTL, plus a `sess:user:{id}` set for O(sessions) bulk revocation.
type SessionStore struct {
	rdb *redis.Client
	ttl time.Duration
}

// NewSessionStore builds a store with the configured sliding TTL.
func NewSessionStore(rdb *redis.Client, ttl time.Duration) *SessionStore {
	return &SessionStore{rdb: rdb, ttl: ttl}
}

func sessKey(token string) string    { return "sess:" + token }
func userSetKey(id uuid.UUID) string { return "sess:user:" + id.String() }

// Create mints a token, writes the session, and indexes it for the user.
func (s *SessionStore) Create(ctx context.Context, userID uuid.UUID, role, ip, ua string) (Session, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return Session{}, fmt.Errorf("auth: generating session token: %w", err)
	}
	sess := Session{
		Token:     base64.RawURLEncoding.EncodeToString(raw),
		UserID:    userID,
		Role:      role,
		CreatedAt: time.Now().UTC(),
		IP:        ip,
		UA:        ua,
	}
	pipe := s.rdb.TxPipeline()
	pipe.HSet(ctx, sessKey(sess.Token), map[string]any{
		"user_id":    userID.String(),
		"role":       role,
		"created_at": sess.CreatedAt.Format(time.RFC3339),
		"ip":         ip,
		"ua":         ua,
	})
	pipe.Expire(ctx, sessKey(sess.Token), s.ttl)
	pipe.SAdd(ctx, userSetKey(userID), sess.Token)
	// The user set outlives any one session; give it a generous TTL refreshed on
	// every create so abandoned sets do not live forever.
	pipe.Expire(ctx, userSetKey(userID), s.ttl*2)
	if _, err := pipe.Exec(ctx); err != nil {
		return Session{}, fmt.Errorf("auth: storing session: %w", err)
	}
	return sess, nil
}

// Get resolves a token, refreshing the sliding TTL when less than half remains.
func (s *SessionStore) Get(ctx context.Context, token string) (Session, error) {
	if token == "" {
		return Session{}, ErrNoSession
	}
	vals, err := s.rdb.HGetAll(ctx, sessKey(token)).Result()
	if err != nil {
		return Session{}, fmt.Errorf("auth: reading session: %w", err)
	}
	if len(vals) == 0 {
		return Session{}, ErrNoSession
	}
	userID, err := uuid.Parse(vals["user_id"])
	if err != nil {
		return Session{}, ErrNoSession
	}
	createdAt, _ := time.Parse(time.RFC3339, vals["created_at"])

	ttl, err := s.rdb.TTL(ctx, sessKey(token)).Result()
	if err == nil && ttl > 0 && ttl < s.ttl/2 {
		// Refresh the session AND re-index it, so the sess:user set can never expire
		// (or drop this token) while the session is still live — otherwise
		// DeleteAllForUser would read a stale/empty index and a banned user would keep
		// a live session. Since 2*ttl > the session's ttl, the set always outlives it.
		pipe := s.rdb.TxPipeline()
		pipe.Expire(ctx, sessKey(token), s.ttl)
		pipe.SAdd(ctx, userSetKey(userID), token)
		pipe.Expire(ctx, userSetKey(userID), s.ttl*2)
		_, _ = pipe.Exec(ctx)
	}

	return Session{
		Token:     token,
		UserID:    userID,
		Role:      vals["role"],
		CreatedAt: createdAt,
		IP:        vals["ip"],
		UA:        vals["ua"],
	}, nil
}

// Delete revokes one session.
func (s *SessionStore) Delete(ctx context.Context, token string) error {
	sess, err := s.Get(ctx, token)
	if err != nil {
		if errors.Is(err, ErrNoSession) {
			return nil
		}
		return err
	}
	pipe := s.rdb.TxPipeline()
	pipe.Del(ctx, sessKey(token))
	pipe.SRem(ctx, userSetKey(sess.UserID), token)
	_, err = pipe.Exec(ctx)
	return err
}

// DeleteAllForUser revokes every session of a user (ban, password reset).
// keepToken, when non-empty, survives (self password-change keeps the current session).
func (s *SessionStore) DeleteAllForUser(ctx context.Context, userID uuid.UUID, keepToken string) error {
	tokens, err := s.rdb.SMembers(ctx, userSetKey(userID)).Result()
	if err != nil {
		return fmt.Errorf("auth: listing user sessions: %w", err)
	}
	pipe := s.rdb.TxPipeline()
	for _, t := range tokens {
		if t == keepToken {
			continue
		}
		pipe.Del(ctx, sessKey(t))
		pipe.SRem(ctx, userSetKey(userID), t)
	}
	_, err = pipe.Exec(ctx)
	return err
}
