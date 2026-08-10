package plugin

import (
	"context"
	"os/exec"
	"time"

	hclog "github.com/hashicorp/go-hclog"
	goplugin "github.com/hashicorp/go-plugin"
)

// conn is a live plugin connection owned by the supervisor: the dispensed type-specific client
// the loader routes to, plus the two lifecycle handles the supervisor needs — kill (reap the
// process) and wait (block until the process exits). It is created by a launchFn and is the
// only handle to the child; the supervisor is the sole owner and closes it via kill.
type conn struct {
	client any                       // dispensed client (pluginpb.ScoringClient, …); read by dispatch
	kill   func()                    // reap the process (idempotent; go-plugin's Client.Kill)
	wait   func(ctx context.Context) // blocks until the process has exited or ctx is done
	pid    int                       // OS pid of the plugin process (0 for a fake launcher)
}

// launchFn launches the plugin process and returns a live conn, or an error if the process
// never reaches a dispensable client — crash-on-launch, handshake failure, or ABI mismatch.
// Injected so the supervisor's restart policy is tested against a fake launcher on an injected
// clock, while production uses realLaunch against a real subprocess.
type launchFn func(ctx context.Context) (*conn, error)

// sleeper waits for d or until ctx is cancelled. Injected so the restart backoff does not make
// tests wait real wall-clock time; production uses realSleep.
type sleeper func(ctx context.Context, d time.Duration) error

// instantSleep is the test sleeper: it returns immediately, so the backoff between restart
// attempts costs no wall-clock time. The computed backoff duration is still passed to it (and
// ignored), so the cap/attempt logic runs exactly as in production.
func instantSleep(context.Context, time.Duration) error { return nil }

// realSleep waits d, honouring ctx cancellation (a stop during backoff returns promptly).
func realSleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// launchSpec is everything realLaunch needs to start one plugin: the binary, its argv (the
// boot-sweep start token is appended here in #1), the dispense key for its declared type, and
// the two timing bounds — how long to wait for the handshake, and how often to poll the child
// for liveness once it is up.
type launchSpec struct {
	bin          string
	args         []string
	key          string
	startTimeout time.Duration
	pollInterval time.Duration
}

// realLaunch returns a launchFn that starts the plugin as a real subprocess over the OSCTF
// go-plugin handshake, dispenses the type-specific client, and returns a conn. SysProcAttr sets
// parent-death (Linux) + a process group; go-plugin sets neither on its own.
func realLaunch(spec launchSpec) launchFn {
	return func(context.Context) (*conn, error) {
		// context.Background, not the launch ctx: go-plugin owns the process lifecycle and reaps
		// it via Client.Kill (the supervisor's conn.kill), so the process must NOT also be tied to
		// an exec context that could SIGKILL it from under go-plugin. Same pattern as the harness.
		//nolint:gosec // G204: launches a configured plugin binary (path from the validated manifest).
		cmd := exec.CommandContext(context.Background(), spec.bin, spec.args...)
		cmd.SysProcAttr = procAttr()

		st := spec.startTimeout
		if st <= 0 {
			st = 30 * time.Second
		}
		client := goplugin.NewClient(&goplugin.ClientConfig{
			HandshakeConfig:  Handshake,
			Plugins:          HostPluginSet(),
			Cmd:              cmd,
			AllowedProtocols: []goplugin.Protocol{goplugin.ProtocolGRPC},
			Logger:           hclog.NewNullLogger(),
			StartTimeout:     st,
		})

		rpc, err := client.Client()
		if err != nil {
			client.Kill()
			return nil, err
		}
		raw, err := rpc.Dispense(spec.key)
		if err != nil {
			client.Kill()
			return nil, err
		}

		pi := spec.pollInterval
		if pi <= 0 {
			pi = 250 * time.Millisecond
		}
		pid := 0
		if cmd.Process != nil {
			pid = cmd.Process.Pid
		}
		return &conn{
			client: raw,
			kill:   client.Kill,
			pid:    pid,
			// go-plugin v1.8.0 exposes no exit channel — only Exited() — so the watcher polls.
			// The poll interval is the crash-detection gap: a dispatch in that window hits a
			// dead client and gets a mapped UNAVAILABLE (invariant #4's crash-gap facet), never
			// a hang. Checked before the first sleep so an already-dead child is seen at once.
			wait: func(wctx context.Context) {
				tk := time.NewTicker(pi)
				defer tk.Stop()
				for {
					if client.Exited() {
						return
					}
					select {
					case <-wctx.Done():
						return
					case <-tk.C:
					}
				}
			},
		}, nil
	}
}
