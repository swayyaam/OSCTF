package plugin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// The boot orphan sweep exists because go-plugin v1.8.0 has no parent-death and macOS/BSD has
// no Pdeathsig: if the host is hard-killed (SIGKILL, no graceful teardown), a plugin child is
// orphaned and nothing in go-plugin reclaims it. On the next boot the loader reads the pidfiles
// left behind and kills any child that is still alive AND positively identified as ours — never
// a pid that has since been reused by an unrelated process. On Linux Pdeathsig usually makes
// this a rare backstop; on the macOS/Docker-Desktop developer path it is the ONLY mechanism.

// pidTokenArg is the argv flag carrying a launch's start-token into the plugin's command line,
// where the sweep can read it back (via ps) to confirm a pid is the child we launched — not a
// reused pid. The plugin binary ignores the flag; it exists only to be observed.
const pidTokenArg = "--osctf-plugin-token=" //nolint:gosec // G101: a CLI flag name, not a credential

// tokenArg formats the start-token as the argv flag appended to a plugin's command.
func tokenArg(token string) string { return pidTokenArg + token }

// newStartToken returns a fresh, unguessable start-token. Random (not sequential) so it cannot
// collide with an unrelated process's command line by construction.
func newStartToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "osctf-" + hex.EncodeToString(b), nil
}

// writePidfile records {pid, token} for a launched plugin under dir/name.pid, so a later boot
// can find and identify a child the host never reaped.
// pidfileRecord is one launched plugin's boot-sweep record: the plugin's pid + start-token, plus
// the OWNER — the pid of the platform process that launched it. The owner is what lets a boot
// sweep tell a true orphan (owner dead) from a plugin belonging to a SECOND platform instance
// sharing the runtime dir (owner alive) — the token alone cannot, since a live instance's plugin
// carries a perfectly valid token.
type pidfileRecord struct {
	PID   int    `json:"pid"`
	Token string `json:"token"`
	Owner int    `json:"owner"` // platform pid that launched the plugin (0 = unknown)
}

func writePidfile(dir, name string, pid int, token string, owner int) (string, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name+".pid")
	data, err := json.Marshal(pidfileRecord{PID: pid, Token: token, Owner: owner})
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// readPidfile parses a pidfile record.
func readPidfile(path string) (pidfileRecord, error) {
	//nolint:gosec // G304: path is a pidfile under our own pidfile dir, not user input.
	b, err := os.ReadFile(path)
	if err != nil {
		return pidfileRecord{}, err
	}
	var r pidfileRecord
	if err := json.Unmarshal(b, &r); err != nil {
		return pidfileRecord{}, fmt.Errorf("plugin: malformed pidfile %s: %w", path, err)
	}
	if r.PID <= 0 {
		return pidfileRecord{}, fmt.Errorf("plugin: pidfile %s missing pid", path)
	}
	return r, nil
}

// processAlive reports whether an OS process exists (signal 0 probes without delivering a
// signal). A reaped process returns ESRCH; EPERM means it exists but is not ours.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// processMatchesToken reports whether pid's command line contains token — the positive
// identification that gates a kill. Uses ps (portable across Linux and macOS); if ps cannot
// read the pid (it exited), it is treated as NOT a match, so the sweep never kills on a guess.
func processMatchesToken(pid int, token string) bool {
	if token == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	//nolint:gosec // G204: pid is an integer from our own pidfile; token is not shell-interpreted.
	out, err := exec.CommandContext(ctx, "ps", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), token)
}

// sweepOrphans reclaims plugin children the host never reaped. For each pidfile in dir:
//   - plugin process gone → remove the stale record;
//   - plugin alive but its OWNER platform is still alive → REFUSE and LEAVE the record: the plugin
//     belongs to a second platform instance sharing this runtime dir, not to us;
//   - plugin alive, owner dead, and the command line carries the matching start-token → an orphan
//     of a crashed platform, killed and its record removed;
//   - plugin alive, owner dead, token does NOT match → the pid was recycled by an unrelated
//     process; refuse to kill, drop the stale record.
//
// Returns the pids it reclaimed. The owner check is what makes two platform instances on one host
// safe; the token check is what makes pid reuse safe.
func sweepOrphans(dir string, log *slog.Logger) (reclaimed []int, err error) {
	if log == nil {
		log = slog.Default()
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // no pidfile dir yet — nothing to reclaim
		}
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".pid") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		r, rerr := readPidfile(path)
		if rerr != nil {
			log.Warn("orphan sweep: skipping unreadable pidfile", "path", path, "err", rerr)
			continue
		}
		switch {
		case !processAlive(r.PID):
			_ = os.Remove(path) // plugin already gone — clear the stale record
		case r.Owner != 0 && processAlive(r.Owner):
			// Belongs to a live platform instance sharing this runtime dir. Not ours to reap, and
			// its record is not ours to remove.
			log.Warn("orphan sweep: refusing — plugin belongs to a live platform instance",
				"pid", r.PID, "owner_pid", r.Owner)
		case processMatchesToken(r.PID, r.Token):
			if kerr := syscall.Kill(r.PID, syscall.SIGKILL); kerr != nil {
				log.Warn("orphan sweep: kill failed", "pid", r.PID, "err", kerr)
				continue
			}
			_ = os.Remove(path)
			reclaimed = append(reclaimed, r.PID)
			log.Info("orphan sweep reclaimed a leaked plugin child", "pid", r.PID)
		default:
			// Alive, owner dead, but token mismatch — the pid was recycled. Refuse; drop the stale record.
			log.Warn("orphan sweep: refusing to kill a reused pid (token mismatch)", "pid", r.PID)
			_ = os.Remove(path)
		}
	}
	return reclaimed, nil
}
