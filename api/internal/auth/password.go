// Package auth owns credentials and sessions: argon2id password hashing (PHC
// strings), the AuthProvider interface with the v0.1 email+password
// implementation, Redis-backed sessions, and the request identity context.
// Parameters per docs/v0.1/06-auth.md.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/argon2"

	"github.com/osctf/platform/internal/apperr"
)

// Argon2id parameters (docs/v0.1/06-auth.md). Changing them re-hashes stored
// passwords transparently on the next successful login (NeedsRehash).
const (
	argonMemoryKiB  = 64 * 1024
	argonIterations = 3
	argonThreads    = 4
	argonSaltLen    = 16
	argonKeyLen     = 32
)

// perHashBytes is the transient memory a single argon2id derivation costs
// (m=64 MiB). The concurrency gate below bounds how many run at once, so peak
// hashing memory is at most (gate size) × perHashBytes.
const perHashBytes = argonMemoryKiB * 1024

// ErrInvalidHash marks a stored hash that cannot be parsed as a PHC argon2id string.
var ErrInvalidHash = errors.New("auth: invalid password hash")

// Concurrency gate ------------------------------------------------------------
//
// argon2id is memory-hard by design: each derivation allocates ~64 MiB. With no
// bound, a registration/login thundering-herd — a venue signing in at once, now
// that the per-IP register limit permits a burst (issue #1) — spikes memory into
// the gigabytes and can OOM a modestly-sized host on the recommended sizing. The
// gate caps concurrent derivations; requests past the cap queue for up to
// hashWait, then are shed with a 503 + Retry-After rather than allocating.
// Covers registration hashing, login verification, and the unknown-email timing
// burn alike (issue #3).
var (
	hashSem      chan struct{} // nil ⇒ gate disabled (unbounded; pre-v0.2.1 behaviour)
	hashWait     time.Duration // max time a request queues before it is shed
	hashInFlight atomic.Int32  // derivations currently past the gate
	hashPeak     atomic.Int32  // high-water mark of hashInFlight (observability/tests)
)

// ConfigureHashGate bounds concurrent argon2id derivations to n, each queued
// request waiting at most maxWait before its caller receives a 503-mapped
// error. n <= 0 disables the gate. Call once at startup before serving; the
// package var is read without a lock, which is safe under that set-once
// discipline (tests reconfigure in the sequential, non-parallel phase).
func ConfigureHashGate(n int, maxWait time.Duration) {
	if n <= 0 {
		hashSem = nil
		hashWait = 0
		return
	}
	hashSem = make(chan struct{}, n)
	hashWait = maxWait
}

// acquireHash reserves a hashing slot, honouring ctx cancellation and the queue
// timeout. The returned release must be called when the derivation completes.
// On timeout it returns an *apperr.Unavailable carrying a Retry-After.
func acquireHash(ctx context.Context) (release func(), err error) {
	sem := hashSem
	if sem == nil {
		enterHash()
		return exitHash, nil
	}
	timer := time.NewTimer(hashWait)
	select {
	case sem <- struct{}{}:
		timer.Stop()
		enterHash()
		return func() { exitHash(); <-sem }, nil
	case <-ctx.Done():
		timer.Stop()
		return nil, ctx.Err()
	case <-timer.C:
		return nil, &apperr.Unavailable{
			Detail:     "the server is busy verifying passwords; retry shortly",
			RetryAfter: hashWait,
		}
	}
}

// enterHash records one more in-flight derivation and lifts the peak watermark.
func enterHash() {
	n := hashInFlight.Add(1)
	for {
		peak := hashPeak.Load()
		if n <= peak || hashPeak.CompareAndSwap(peak, n) {
			break
		}
	}
}

func exitHash() { hashInFlight.Add(-1) }

// deriveKey is the single chokepoint through which every argon2id call passes,
// so the gate covers hashing and verification uniformly.
func deriveKey(ctx context.Context, password, salt []byte, iters, mem uint32, threads uint8, keyLen uint32) ([]byte, error) {
	release, err := acquireHash(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	return argon2.IDKey(password, salt, iters, mem, threads, keyLen), nil
}

// HashPassword derives an argon2id hash and encodes it as a PHC string:
// $argon2id$v=19$m=65536,t=3,p=4$<b64 salt>$<b64 hash>. It blocks on the
// concurrency gate and may return an *apperr.Unavailable (503) under overload.
func HashPassword(ctx context.Context, password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: generating salt: %w", err)
	}
	key, err := deriveKey(ctx, []byte(password), salt, argonIterations, argonMemoryKiB, argonThreads, argonKeyLen)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemoryKiB, argonIterations, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

type phcParams struct {
	memoryKiB  uint32
	iterations uint32
	threads    uint8
	salt       []byte
	hash       []byte
}

func parsePHC(encoded string) (phcParams, error) {
	var p phcParams
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return p, ErrInvalidHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return p, ErrInvalidHash
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.memoryKiB, &p.iterations, &p.threads); err != nil {
		return p, ErrInvalidHash
	}
	var err error
	if p.salt, err = base64.RawStdEncoding.DecodeString(parts[4]); err != nil {
		return p, ErrInvalidHash
	}
	if p.hash, err = base64.RawStdEncoding.DecodeString(parts[5]); err != nil {
		return p, ErrInvalidHash
	}
	return p, nil
}

// VerifyPassword recomputes the hash with the parameters stored in the PHC
// string and compares in constant time. It blocks on the concurrency gate and
// may return an *apperr.Unavailable (503) under overload — callers must
// distinguish that from a (false, nil) mismatch (see EmailPasswordProvider).
func VerifyPassword(ctx context.Context, password, encoded string) (bool, error) {
	p, err := parsePHC(encoded)
	if err != nil {
		return false, err
	}
	//nolint:gosec // G115: hash length is a fixed 32 bytes, never overflows uint32.
	key, err := deriveKey(ctx, []byte(password), p.salt, p.iterations, p.memoryKiB, p.threads, uint32(len(p.hash)))
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(key, p.hash) == 1, nil
}

// NeedsRehash reports whether the stored hash uses parameters different from the
// current configuration (upgrade path: re-hash on next successful login).
func NeedsRehash(encoded string) bool {
	p, err := parsePHC(encoded)
	if err != nil {
		return true
	}
	return p.memoryKiB != argonMemoryKiB || p.iterations != argonIterations || p.threads != argonThreads
}

// dummyHash is a fixed valid PHC hash of an unguessable string, used to equalize
// login timing when the email does not exist (docs/v0.1/06-auth.md). Computed at
// package init, before the gate is configured, so it never blocks.
var dummyHash = func() string {
	h, err := HashPassword(context.Background(), "osctf-dummy-timing-uniformity")
	if err != nil {
		panic(err) // crypto/rand failure at init is unrecoverable
	}
	return h
}()

// BurnHash performs one argon2 verification against the dummy hash so a login
// with an unknown email costs the same as one with a known email. It returns the
// gate error (if any) so the caller can shed the unknown-email path the same way
// it sheds the known-email path — keeping the 401/503 status uniform under load.
func BurnHash(ctx context.Context, password string) error {
	_, err := VerifyPassword(ctx, password, dummyHash)
	return err
}

// DefaultHashConcurrency derives a gate size from the host memory limit (cgroup
// v2/v1, then /proc/meminfo), budgeting a quarter of memory to concurrent
// hashing, clamped to [2,64]. It falls back to GOMAXPROCS when the memory limit
// can't be read (e.g. non-Linux dev hosts).
func DefaultHashConcurrency() int {
	if k := hashConcurrencyFor(availableMemoryBytes()); k > 0 {
		return k
	}
	return clampInt(runtime.GOMAXPROCS(0), 2, 64)
}

// hashConcurrencyFor turns a memory budget into a concurrent-hash ceiling: a
// quarter of memory divided by the per-hash cost, clamped to [2,64]. Returns 0
// when memBytes is 0 (unknown) so DefaultHashConcurrency can fall back.
func hashConcurrencyFor(memBytes uint64) int {
	if memBytes == 0 {
		return 0
	}
	k := memBytes / 4 / perHashBytes
	switch {
	case k < 2:
		k = 2
	case k > 64:
		k = 64
	}
	//nolint:gosec // G115: k is clamped to [2,64] above, never overflows int.
	return int(k)
}

func clampInt(v, lo, hi int) int {
	switch {
	case v < lo:
		return lo
	case v > hi:
		return hi
	default:
		return v
	}
}

// availableMemoryBytes reports the process memory limit in bytes, preferring the
// cgroup limit (the real container ceiling) over host RAM. Returns 0 when no
// source is readable (e.g. macOS), signalling the caller to fall back.
func availableMemoryBytes() uint64 {
	if b, ok := readCgroupLimit("/sys/fs/cgroup/memory.max"); ok { // cgroup v2
		return b
	}
	if b, ok := readCgroupLimit("/sys/fs/cgroup/memory/memory.limit_in_bytes"); ok { // cgroup v1
		return b
	}
	return readMemTotal("/proc/meminfo")
}

// readCgroupLimit parses a single-value cgroup memory file. Returns ok=false for
// "max" (v2 unlimited), a v1 unlimited sentinel, or any parse failure/absence.
func readCgroupLimit(path string) (uint64, bool) {
	//nolint:gosec // G304: fixed cgroup paths chosen by availableMemoryBytes, not user input.
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	s := strings.TrimSpace(string(data))
	if s == "max" {
		return 0, false
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil || v == 0 || v >= 1<<62 { // >=1<<62 ⇒ effectively unlimited
		return 0, false
	}
	return v, true
}

// readMemTotal parses MemTotal (kB) from a /proc/meminfo-shaped file. Returns 0
// on any failure.
func readMemTotal(path string) uint64 {
	//nolint:gosec // G304: fixed /proc/meminfo path chosen by availableMemoryBytes, not user input.
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0
		}
		kb, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0
		}
		return kb * 1024
	}
	return 0
}
