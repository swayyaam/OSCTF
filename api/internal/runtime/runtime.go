// Package runtime manages challenge workload containers behind the
// ChallengeRuntime interface. v0.1 implementation: DockerRuntime. The Manager
// layers DB-backed instance lifecycle (port allocation, rows, connection info)
// over the interface. Scope: per-event shared instances (one container per
// container-kind challenge). See docs/v0.1/08-challenge-runtime.md.
package runtime

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

func asError(err error, target any) bool { return errors.As(err, target) }

// State is a challenge instance's lifecycle state.
type State string

const (
	StatePending   State = "pending"
	StateStarting  State = "starting"
	StateRunning   State = "running"
	StateUnhealthy State = "unhealthy"
	StateStopped   State = "stopped"
	StateError     State = "error"
	StateLost      State = "lost"
)

// InstanceSpec is the desired shape of a challenge instance. The v0.2 fields
// (owner, network, hardening) are additive; the Manager fills them from the
// challenge row so the runtime never has to know about instancing or flag mode.
type InstanceSpec struct {
	InstanceID   uuid.UUID
	ChallengeID  uuid.UUID
	Slug         string
	Image        string
	InternalPort int
	HostPort     int
	MemLimitMB   int
	CPUMillis    int
	Env          map[string]string // already carries FLAG (per-instance value when set)

	TeamID         *uuid.UUID // nil = shared instance (v0.1); set = per-team
	NetworkName    string     // docker network to attach; "" = shared 'osctf-challenges'
	NoEgress       bool       // true -> network created without outbound NAT (egress off)
	ReadonlyRootfs bool       // true -> read-only container rootfs
	Tmpfs          []string   // writable tmpfs mount targets, e.g. ["/tmp"]
}

// Instance is the observed state of a challenge instance.
type Instance struct {
	ID           uuid.UUID
	ChallengeID  uuid.UUID
	TeamID       *uuid.UUID
	State        State
	ContainerID  string
	HostPort     int
	Network      string
	StartedAt    *time.Time
	LastHealthAt *time.Time
	ExpiresAt    *time.Time
	Err          string
}

// ChallengeRuntime manages challenge workload containers. This is the future
// plugin surface — kept deliberately minimal.
type ChallengeRuntime interface {
	// Name returns a stable identifier, e.g. "docker".
	Name() string
	// Deploy creates and starts the instance for a challenge. Idempotent:
	// deploying an already-running instance returns it unchanged.
	Deploy(ctx context.Context, spec InstanceSpec) (Instance, error)
	// Stop halts the container but keeps it for restart/log inspection.
	Stop(ctx context.Context, instanceID uuid.UUID) error
	// Destroy stops and removes the container and frees its port.
	Destroy(ctx context.Context, instanceID uuid.UUID) error
	// Status re-inspects the live container and returns current state.
	Status(ctx context.Context, instanceID uuid.UUID) (Instance, error)
	// Logs returns up to tailLines of recent container output.
	Logs(ctx context.Context, instanceID uuid.UUID, tailLines int) (string, error)
	// Reconcile aligns tracked instances with actual runtime state.
	Reconcile(ctx context.Context) error
}

// ErrRuntimeUnavailable signals the container runtime is unreachable; callers
// map it to a 503. The platform (scoreboard, auth) works without the runtime.
type unavailableError struct{ cause error }

func (e *unavailableError) Error() string { return "runtime unavailable: " + e.cause.Error() }
func (e *unavailableError) Unwrap() error { return e.cause }

// Unavailable wraps an error as a runtime-unavailable condition.
func Unavailable(cause error) error { return &unavailableError{cause: cause} }

// IsUnavailable reports whether err is a runtime-unavailable condition.
func IsUnavailable(err error) bool {
	var u *unavailableError
	return asError(err, &u)
}
