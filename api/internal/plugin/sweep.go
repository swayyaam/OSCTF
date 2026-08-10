package plugin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
func writePidfile(dir, name string, pid int, token string) (string, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name+".pid")
	if err := os.WriteFile(path, []byte(fmt.Sprintf("%d\n%s\n", pid, token)), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// readPidfile parses a {pid, token} pidfile.
func readPidfile(path string) (pid int, token string, err error) {
	//nolint:gosec // G304: path is a pidfile under our own pidfile dir, not user input.
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, "", err
	}
	lines := strings.SplitN(strings.TrimSpace(string(b)), "\n", 2)
	if len(lines) != 2 {
		return 0, "", fmt.Errorf("plugin: malformed pidfile %s", path)
	}
	pid, err = strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil {
		return 0, "", fmt.Errorf("plugin: bad pid in %s: %w", path, err)
	}
	return pid, strings.TrimSpace(lines[1]), nil
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

// sweepOrphans reclaims plugin children the host never reaped. For each pidfile in dir: if the
// process is gone, the stale pidfile is removed; if it is alive AND its command line carries the
// matching start-token, it is an orphan of ours and is killed; if it is alive but the token does
// NOT match, the pid has been reused by an unrelated process and the sweep REFUSES to kill it
// (removing only the stale pidfile). Returns the pids it reclaimed.
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
		pid, token, rerr := readPidfile(path)
		if rerr != nil {
			log.Warn("orphan sweep: skipping unreadable pidfile", "path", path, "err", rerr)
			continue
		}
		switch {
		case !processAlive(pid):
			_ = os.Remove(path) // owner already gone — clear the stale record
		case processMatchesToken(pid, token):
			if kerr := syscall.Kill(pid, syscall.SIGKILL); kerr != nil {
				log.Warn("orphan sweep: kill failed", "pid", pid, "err", kerr)
				continue
			}
			_ = os.Remove(path)
			reclaimed = append(reclaimed, pid)
			log.Info("orphan sweep reclaimed a leaked plugin child", "pid", pid)
		default:
			// Alive but not ours — the pid was recycled. Refuse to kill; drop the stale record.
			log.Warn("orphan sweep: refusing to kill a reused pid (token mismatch)", "pid", pid)
			_ = os.Remove(path)
		}
	}
	return reclaimed, nil
}
