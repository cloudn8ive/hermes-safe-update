package platform

// Process-table parsing shared by Linux (/proc) and macOS (ps). No build
// constraint: it is plain string and file logic, tested on every OS from
// fixtures under testdata/. Untested against a live /proc or ps.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// procStat is the part of /proc/<pid>/stat we use.
type procStat struct {
	Comm       string
	State      string
	PPID       int
	StartTicks int64
}

// parseProcStat parses one /proc/<pid>/stat line. comm sits in parentheses
// and may itself contain spaces and parentheses, so the LAST ')' ends it.
func parseProcStat(line string) (procStat, error) {
	open := strings.IndexByte(line, '(')
	closeIdx := strings.LastIndexByte(line, ')')
	if open < 0 || closeIdx < open {
		return procStat{}, errors.New("malformed /proc stat line: no (comm)")
	}
	rest := strings.Fields(line[closeIdx+1:])
	// rest[0] is field 3 (state); starttime is field 22 = rest[19].
	if len(rest) < 20 {
		return procStat{}, fmt.Errorf("malformed /proc stat line: %d fields after comm", len(rest))
	}
	ppid, err := strconv.Atoi(rest[1])
	if err != nil {
		return procStat{}, fmt.Errorf("malformed /proc stat ppid %q", rest[1])
	}
	start, err := strconv.ParseInt(rest[19], 10, 64)
	if err != nil {
		return procStat{}, fmt.Errorf("malformed /proc stat starttime %q", rest[19])
	}
	return procStat{Comm: line[open+1 : closeIdx], State: rest[0], PPID: ppid, StartTicks: start}, nil
}

// parseProcCmdline turns the NUL-separated /proc/<pid>/cmdline into one
// space-joined line.
func parseProcCmdline(b []byte) string {
	s := strings.TrimRight(string(b), "\x00")
	if s == "" {
		return ""
	}
	return strings.ReplaceAll(s, "\x00", " ")
}

// clockTicks is USER_HZ, which is 100 on every Linux architecture Go ships
// for; reading sysconf would need cgo.
const clockTicks = 100

// readProcTable lists processes under a /proc-shaped directory root.
// Processes that vanish while being read are skipped.
func readProcTable(ctx context.Context, root string) ([]Process, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", root, err)
	}
	boot := bootTime(root)
	var out []Process
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 || !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		raw, err := os.ReadFile(filepath.Join(dir, "stat"))
		if err != nil {
			continue
		}
		st, err := parseProcStat(string(raw))
		if err != nil {
			continue
		}
		cmdRaw, _ := os.ReadFile(filepath.Join(dir, "cmdline"))
		p := Process{PID: pid, PPID: st.PPID, Name: st.Comm, Cmdline: parseProcCmdline(cmdRaw)}
		if exe, err := os.Readlink(filepath.Join(dir, "exe")); err == nil {
			p.Exe = exe
		} else if argv0 := strings.SplitN(p.Cmdline, " ", 2)[0]; strings.HasPrefix(argv0, "/") {
			// Unreadable exe link (another user's process, a fixture): argv[0]
			// is the best available hint, but only when it is absolute.
			p.Exe = argv0
		}
		if !boot.IsZero() {
			p.Created = boot.Add(time.Duration(st.StartTicks) * time.Second / clockTicks)
		}
		out = append(out, p)
	}
	return out, nil
}

// bootTime reads btime (epoch seconds) from <root>/stat; zero if unknown.
func bootTime(root string) time.Time {
	f, err := os.Open(filepath.Join(root, "stat"))
	if err != nil {
		return time.Time{}
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "btime "); ok {
			if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
				return time.Unix(n, 0)
			}
		}
	}
	return time.Time{}
}

// procIsZombie reports whether <root>/<pid>/stat says state Z. A missing or
// unreadable entry is not a zombie.
func procIsZombie(root string, pid int) bool {
	raw, err := os.ReadFile(filepath.Join(root, strconv.Itoa(pid), "stat"))
	if err != nil {
		return false
	}
	st, err := parseProcStat(string(raw))
	return err == nil && st.State == "Z"
}

// parsePSTable joins two `ps` outputs by pid:
//
//	ps -axwwo pid=,ppid=,comm=   (comm = full executable path on macOS)
//	ps -axwwo pid=,command=      (full command line)
//
// Two calls because comm and command can both contain spaces, so neither can
// be split out of a single line. Malformed lines are skipped.
func parsePSTable(commOut, cmdOut string) []Process {
	cmds := map[int]string{}
	for _, line := range strings.Split(cmdOut, "\n") {
		pid, rest, ok := cutInt(line)
		if ok {
			cmds[pid] = strings.TrimSpace(rest)
		}
	}
	var out []Process
	for _, line := range strings.Split(commOut, "\n") {
		pid, rest, ok := cutInt(line)
		if !ok {
			continue
		}
		ppid, exe, ok := cutInt(rest)
		if !ok {
			continue
		}
		exe = strings.TrimSpace(exe)
		out = append(out, Process{PID: pid, PPID: ppid, Name: path.Base(exe), Exe: exe, Cmdline: cmds[pid]})
	}
	return out
}

// cutInt splits "  123 rest of line" into 123 and "rest of line".
func cutInt(line string) (int, string, bool) {
	line = strings.TrimSpace(strings.TrimRight(line, "\r"))
	tok, rest, _ := strings.Cut(line, " ")
	n, err := strconv.Atoi(tok)
	if err != nil || n < 0 || rest == "" {
		return 0, "", false
	}
	return n, strings.TrimLeft(rest, " "), true
}
