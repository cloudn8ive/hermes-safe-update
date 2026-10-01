package mirror

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/config"
	"github.com/cloudn8ive/hermes-safe-update/internal/fsx"
)

// AgeText says how old the last refresh is (python-behaviour §2.12).
func AgeText(last, now time.Time) string {
	if last.IsZero() {
		return "never refreshed"
	}
	mins := int(now.Sub(last) / time.Minute)
	switch {
	case mins < 1:
		return "refreshed just now"
	case mins < 120:
		return fmt.Sprintf("refreshed %d min ago", mins)
	default:
		return fmt.Sprintf("refreshed %d h ago", mins/60)
	}
}

// SourceLine is the "Checked via" text when the mirror answered.
func SourceLine(last, now time.Time) string {
	return "local mirror, " + AgeText(last, now)
}

// validBare is the Python check: HEAD file plus objects and refs dirs. A
// missing drive therefore reads as "no mirror".
func validBare(p string) bool {
	return p != "" && isFile(filepath.Join(p, "HEAD")) && isDir(filepath.Join(p, "objects")) && isDir(filepath.Join(p, "refs"))
}

// loadMachine reads safe-update.json; a corrupt file reads as empty here
// (Status is read-only and must never fail; the CLI already warned).
func (s *Svc) loadMachine() config.MachineState {
	st, err := config.LoadMachineState(s.d.MachinePath)
	if err != nil {
		s.logf("machine state unreadable: " + err.Error())
	}
	return st
}

// mirrorPath returns the configured mirror if it is a usable bare repo.
func (s *Svc) mirrorPath() string {
	st := s.loadMachine()
	if st.Mirror != nil && validBare(st.Mirror.Path) {
		return st.Mirror.Path
	}
	if st.Mirror == nil && s.d.Cfg.Path != "" && validBare(s.d.Cfg.Path) {
		return s.d.Cfg.Path // YAML fallback (machine state wins)
	}
	return ""
}

type mirrorFileState struct {
	LastOK    float64 `json:"last_ok,omitempty"`
	LastTry   float64 `json:"last_try,omitempty"`
	LastError *string `json:"last_error"`
	Head      string  `json:"head,omitempty"`
}

func (s *Svc) readState() (mirrorFileState, map[string]json.RawMessage) {
	var st mirrorFileState
	raw := map[string]json.RawMessage{}
	data, err := os.ReadFile(s.cachePath(stateFile))
	if err != nil {
		return st, raw
	}
	_ = json.Unmarshal(data, &raw)
	_ = json.Unmarshal(data, &st)
	return st, raw
}

func (s *Svc) writeState(st mirrorFileState, raw map[string]json.RawMessage) error {
	for k, v := range map[string]any{"last_ok": st.LastOK, "last_try": st.LastTry, "last_error": st.LastError, "head": st.Head} {
		b, _ := json.Marshal(v)
		raw[k] = b
	}
	data, err := json.MarshalIndent(raw, "", " ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.cachePath(stateFile)), 0o755); err != nil {
		return err
	}
	return fsx.WriteFileAtomic(s.cachePath(stateFile), append(data, '\n'), 0o600)
}

// Status is read-only: no git, no network.
func (s *Svc) Status() Status {
	ms := s.loadMachine()
	out := Status{Declined: ms.MirrorOfferDeclined()}
	if ms.Mirror != nil {
		out.ConfiguredPath = ms.Mirror.Path
	} else {
		out.ConfiguredPath = s.d.Cfg.Path
	}
	out.OK = s.mirrorPath() != ""
	fs, _ := s.readState()
	if fs.LastOK > 0 {
		out.LastOK = time.Unix(int64(fs.LastOK), 0)
	}
	if fs.LastError != nil {
		out.LastError = *fs.LastError
	}
	if j := s.cronJob(); j != nil {
		out.JobExists = true
		out.JobActive = jobActive(j)
	}
	return out
}

// cronJob finds this machine's refresh job by script file name. Both the
// Go shim and the Python helper's name match, so an existing job from the
// Python tool is reused instead of duplicated.
func (s *Svc) cronJob() map[string]any {
	data, err := os.ReadFile(filepath.Join(s.d.Home, "cron", "jobs.json"))
	if err != nil {
		return nil
	}
	var top any
	if json.Unmarshal(data, &top) != nil {
		return nil
	}
	var list []any
	switch v := top.(type) {
	case map[string]any:
		list, _ = v["jobs"].([]any)
	case []any:
		list = v
	}
	for _, it := range list {
		j, ok := it.(map[string]any)
		if !ok {
			continue
		}
		script, _ := j["script"].(string)
		script = strings.ReplaceAll(script, `\`, "/")
		if i := strings.LastIndex(script, "/"); i >= 0 {
			script = script[i+1:]
		}
		if script == shimName || script == legacyShim {
			return j
		}
	}
	return nil
}

func jobActive(j map[string]any) bool {
	if j == nil {
		return false
	}
	if en, ok := j["enabled"].(bool); ok && !en {
		return false
	}
	if p, ok := j["paused_at"]; ok && p != nil && p != "" {
		return false
	}
	switch strings.ToLower(fmt.Sprint(j["state"])) {
	case "paused", "disabled":
		return false
	}
	return true
}

// ---- lock ----------------------------------------------------------------

// acquireLock takes the one-writer lock (O_EXCL). A lock is stale when its
// pid is dead or it is older than 30 minutes. It polls once a second for up
// to wait. The returned func releases it.
func (s *Svc) acquireLock(ctx context.Context, wait time.Duration) (func(), error) {
	path := s.cachePath(lockFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("mirror lock directory: %w", err)
	}
	deadline := s.d.Now().Add(wait)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, werr := fmt.Fprintf(f, "%d %d", os.Getpid(), s.d.Now().Unix())
			cerr := f.Close()
			if werr != nil || cerr != nil {
				_ = os.Remove(path)
				return nil, fmt.Errorf("write mirror lock %q: %w", path, firstErr(werr, cerr))
			}
			return func() { _ = os.Remove(path) }, nil
		}
		if !os.IsExist(err) {
			return nil, fmt.Errorf("mirror lock %q: %w", path, err)
		}
		if s.lockStale(path) {
			if rerr := os.Remove(path); rerr == nil || os.IsNotExist(rerr) {
				continue
			}
		}
		if !s.d.Now().Before(deadline) {
			return nil, ErrBusy
		}
		s.d.Sleep(ctx, time.Second)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

func (s *Svc) lockStale(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return os.IsNotExist(err)
	}
	f := strings.Fields(string(data))
	if len(f) < 2 {
		return true
	}
	pid, e1 := strconv.Atoi(f[0])
	ts, e2 := strconv.ParseInt(f[1], 10, 64)
	if e1 != nil || e2 != nil {
		return true
	}
	return !s.alive(pid) || s.d.Now().Sub(time.Unix(ts, 0)) > staleLock
}

// markerPID is the pid in Hermes' update-in-progress marker, 0 if none.
func (s *Svc) markerPID() int {
	data, err := os.ReadFile(s.markerPath())
	if err != nil {
		return 0
	}
	f := strings.Fields(string(data))
	if len(f) == 0 {
		return 0
	}
	pid, err := strconv.Atoi(f[0])
	if err != nil {
		return 0
	}
	return pid
}

func machineMirror(path string, now time.Time) *config.MirrorState {
	return &config.MirrorState{Path: path, SetUp: now.Local().Format("2006-01-02T15:04:05-07:00")}
}

func writeMachine(path string, st config.MachineState) error {
	return config.SaveMachineState(path, st)
}
