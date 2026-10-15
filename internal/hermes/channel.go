package hermes

// Release-channel awareness (Hermes v0.21.6+, "Safe Update v0.3.0").
//
// Hermes updates a source checkout from one of two channels: "main" (the tip
// of origin/main) or "stable" (the latest published GitHub release, installed
// as a detached checkout). An explicit `--branch main` beats the channel, which
// is what Safe Update always passed before channels existed.
//
// This file holds everything that decides which channel a run uses and how
// `hermes update` / `hermes update --check` are called for it:
//
//   - ProbeCaps / ParseCaps: does this Hermes know `--channel` and `--install-id`?
//   - ResolveChannel: Hermes' own effective channel, read OFFLINE and read-only
//     (install id, then `update.installs.<id>.channel` in <home>/config.yaml,
//     else the official-origin default). Safe Update never writes that config.
//   - BuildPlan / BuildUpdateArgv / BuildCheckArgv: the argv per mode.
//
// The wording of Hermes' check output lives in contracts.go (ParseChannelCheck).

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/cloudn8ive/hermes-safe-update/internal/config"
	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
)

const (
	// channelProbeTimeout bounds `hermes update --help` and `--install-id` (Python
	// start-up only; they touch neither the network nor the checkout). The
	// launcher alone takes ~20 s to start on a loaded machine, so 15 s was
	// too tight: a timeout must stay a rare event, not a normal one.
	channelProbeTimeout = 45 * time.Second
	// OfficialRepository is the only GitHub repository whose checkouts default
	// to the stable channel in Hermes (update_channel._tracks_official_origin).
	OfficialRepository = "NousResearch/hermes-agent"
)

// probeEnv is the environment of the probes and the check (UTF-8 output).
var probeEnv = map[string]string{"PYTHONIOENCODING": "utf-8"}

// ---- capability probe ----

// Where Caps came from (Caps.Source).
const (
	CapsFromHelp   = "help"         // `hermes update --help` was read
	CapsFromSource = "source files" // the help failed; the checkout's own source says
	CapsFromNone   = "none"         // neither could say
)

// Caps is what `hermes update --help` says this Hermes supports.
type Caps struct {
	Channel    bool   // `--channel` is listed: release channels exist
	InstallID  bool   // `--install-id` is listed: the offline channel read works
	SetChannel bool   // `--set-channel` is listed: `channel set` can switch the channel
	Probed     bool   // the capabilities are known (from the help text or the source files)
	Err        string // why `update --help` failed ("" when it was read)
	Source     string // CapsFromHelp | CapsFromSource | CapsFromNone
}

var (
	channelFlagRe   = regexp.MustCompile(`(?:^|[^A-Za-z0-9-])--channel(?:[^A-Za-z0-9-]|$)`)
	installIDFlagRe = regexp.MustCompile(`(?:^|[^A-Za-z0-9-])--install-id(?:[^A-Za-z0-9-]|$)`)
	setChannelRe    = regexp.MustCompile(`(?:^|[^A-Za-z0-9-])--set-channel(?:[^A-Za-z0-9-]|$)`)
)

// ParseCaps reads the help text of `hermes update`. "--set-channel" does not
// count as "--channel".
func ParseCaps(help string) Caps {
	return Caps{
		Channel:    channelFlagRe.MatchString(help),
		InstallID:  installIDFlagRe.MatchString(help),
		SetChannel: setChannelRe.MatchString(help),
		Probed:     true,
		Source:     CapsFromHelp,
	}
}

// ProbeCaps runs `hermes update --help` (UTF-8 output). Any failure (cannot
// start, timeout, non-zero exit, empty output) is "no caps" with Err set;
// ProbeCapsOrSource then asks the checkout's source files before a caller
// decides this Hermes has no channels.
func ProbeCaps(ctx context.Context, run execx.Runner, launcher string) Caps {
	res, err := run.Run(ctx, execx.Cmd{Argv: []string{launcher, "update", "--help"}, Timeout: channelProbeTimeout, Env: probeEnv})
	switch {
	case err != nil:
		return Caps{Err: err.Error(), Source: CapsFromNone}
	case res.TimedOut:
		return Caps{Err: "timed out", Source: CapsFromNone}
	case res.Code != 0:
		return Caps{Err: fmt.Sprintf("exit code %d", res.Code), Source: CapsFromNone}
	case strings.TrimSpace(res.Output) == "":
		return Caps{Err: "no output", Source: CapsFromNone}
	}
	return ParseCaps(res.Output)
}

// FileCaps answers from the checkout's own source when `update --help` could
// not be read: release channels exist when the checkout holds
// hermes_cli/update_channel.py and the update sub-command's definition
// (hermes_cli/subcommands/update.py) names "--channel". The other two flags
// are looked up by their quoted text the same way. Nothing found, or nothing
// readable, is CapsFromNone (a Hermes from before channels has neither file).
func FileCaps(checkout string) Caps {
	none := Caps{Source: CapsFromNone}
	if checkout == "" {
		return none
	}
	if st, err := os.Stat(filepath.Join(checkout, "hermes_cli", "update_channel.py")); err != nil || !st.Mode().IsRegular() {
		return none
	}
	b, err := os.ReadFile(filepath.Join(checkout, "hermes_cli", "subcommands", "update.py"))
	if err != nil {
		return none
	}
	text := string(b)
	if !strings.Contains(text, `"--channel"`) {
		return none
	}
	return Caps{
		Channel:    true,
		InstallID:  strings.Contains(text, `"--install-id"`),
		SetChannel: strings.Contains(text, `"--set-channel"`),
		Probed:     true,
		Source:     CapsFromSource,
	}
}

// ProbeCapsOrSource is ProbeCaps, and when the help cannot be read, FileCaps
// on the checkout. A flaky probe (a slow launcher start-up) must not read as
// "this Hermes has no channels": that would send a stable-channel user to
// main. The help error stays in Err for the log.
func ProbeCapsOrSource(ctx context.Context, run execx.Runner, launcher, checkout string) Caps {
	c := ProbeCaps(ctx, run, launcher)
	if c.Probed || ctx.Err() != nil {
		return c
	}
	if f := FileCaps(checkout); f.Probed {
		f.Err = c.Err
		return f
	}
	return c
}

// ProbeChannel runs the capability probe and, when wantResolve, the install-id
// read side by side (each is a Python start-up of many seconds), then reads
// Hermes' own channel offline when the capabilities allow. Safe Update only
// reads; nothing is written.
func ProbeChannel(ctx context.Context, run execx.Runner, launcher, home, checkout string, wantResolve bool) (Caps, Resolved) {
	var id string
	var wg sync.WaitGroup
	if wantResolve {
		wg.Add(1)
		go func() { defer wg.Done(); id = ReadInstallID(ctx, run, launcher) }()
	}
	caps := ProbeCapsOrSource(ctx, run, launcher, checkout)
	wg.Wait()
	res := Resolved{Source: SourceUnknown}
	if wantResolve && caps.Channel && caps.InstallID {
		res = ResolveWithID(id, home, checkout)
	}
	return caps, res
}

// ---- Hermes' effective channel, read offline ----

// ChannelSource says where a Resolved channel came from.
type ChannelSource string

const (
	// SourceRecord: `update.installs.<id>.channel` in Hermes' config.yaml (set
	// by Settings > About, or `hermes update --set-channel`).
	SourceRecord ChannelSource = "record"
	// SourceDefault: no record; Hermes' default (stable for a checkout whose
	// origin is the official GitHub repo, else main).
	SourceDefault ChannelSource = "default"
	// SourceUnknown: could not be read (no install id, odd config, ...). Channel is "".
	SourceUnknown ChannelSource = "unknown"
	// SourceCheck: not read offline; taken from the `→ Update channel:` line
	// of a `hermes update --check` (Plan.WithCheckedChannel).
	SourceCheck ChannelSource = "check"
)

// Resolved is Hermes' effective update channel for this install.
type Resolved struct {
	Channel   string        // "stable", "main", another Hermes channel name, or "" when unknown
	Source    ChannelSource // record | default | unknown | check
	InstallID string        // 16 hex digits from `hermes update --install-id` ("" if not read)
}

// Known reports whether Channel was determined.
func (r Resolved) Known() bool { return r.Channel != "" && r.Source != SourceUnknown }

var (
	installIDRe = regexp.MustCompile(`^[0-9a-f]{16}$`)
	// A Hermes channel name (release_channels.validate_name): lower-case
	// words joined by single dashes, at most 32 characters.
	channelNameRe = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	// githubOriginRe is source_releases._GITHUB_ORIGIN.
	githubOriginRe = regexp.MustCompile(`(?i)^(?:https://github\.com/|git@github\.com:|ssh://git@github\.com/)([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+?)(?:\.git)?/?$`)
)

// ValidChannelName reports whether s looks like a Hermes channel name.
func ValidChannelName(s string) bool { return len(s) <= 32 && channelNameRe.MatchString(s) }

// ParseInstallID reads the id `hermes update --install-id` prints (the only
// 16-hex-digit line; other lines, e.g. launcher warnings, are ignored).
func ParseInstallID(out string) (string, bool) {
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); installIDRe.MatchString(l) {
			return l, true
		}
	}
	return "", false
}

// RecordChannel reads `update.installs.<id>.channel` from the text of Hermes'
// config.yaml. found=false with ok=true means "no record" (Hermes then uses
// its default). ok=false means the file has a shape this reader does not
// trust (not YAML, `update`/`installs` not a mapping, a channel that is not a
// valid name): the caller must say "unknown" instead of guessing.
func RecordChannel(configYAML []byte, id string) (channel string, found, ok bool) {
	configYAML = bytes.TrimPrefix(configYAML, []byte("\xef\xbb\xbf"))
	var doc any
	if err := yaml.Unmarshal(configYAML, &doc); err != nil {
		return "", false, false
	}
	if doc == nil {
		return "", false, true
	}
	root, isMap := asMap(doc)
	if !isMap {
		return "", false, false
	}
	upd, present := root["update"]
	if !present || upd == nil {
		return "", false, true
	}
	updMap, isMap := asMap(upd)
	if !isMap {
		return "", false, false // Hermes refuses to read/replace a malformed `update:`
	}
	inst, present := updMap["installs"]
	if !present || inst == nil {
		return "", false, true
	}
	instMap, isMap := asMap(inst)
	if !isMap {
		return "", false, false
	}
	rec, present := instMap[id]
	if !present {
		return "", false, true
	}
	recMap, isMap := asMap(rec)
	if !isMap {
		return "", false, true // channel_record(): a non-mapping record counts as none
	}
	raw, present := recMap["channel"]
	if !present || raw == nil {
		return "", false, true
	}
	s, isStr := raw.(string)
	if !isStr || !ValidChannelName(s) {
		return "", false, false
	}
	return s, true, true
}

// asMap views a decoded YAML mapping with string keys.
func asMap(v any) (map[string]any, bool) {
	switch m := v.(type) {
	case map[string]any:
		return m, true
	case map[any]any:
		out := make(map[string]any, len(m))
		for k, val := range m {
			if ks, ok := k.(string); ok {
				out[ks] = val
			}
		}
		return out, true
	}
	return nil, false
}

// OfficialOrigin reports whether the text of a checkout's .git/config has
// `[remote "origin"] url` pointing at github.com/NousResearch/hermes-agent
// (https or ssh, any case, optional .git): the same file read Hermes does.
func OfficialOrigin(gitConfig string) bool {
	gitConfig = strings.TrimPrefix(gitConfig, "\ufeff")
	in := false
	for _, line := range strings.Split(gitConfig, "\n") {
		text := strings.TrimSpace(line)
		if strings.HasPrefix(text, "[") {
			in = text == `[remote "origin"]`
		} else if k, v, _ := strings.Cut(text, "="); in && strings.TrimSpace(k) == "url" {
			m := githubOriginRe.FindStringSubmatch(strings.TrimSpace(v))
			return m != nil && strings.EqualFold(m[1], OfficialRepository)
		}
	}
	return false
}

// readGitConfig returns the text of the checkout's git config, following a
// `.git` file (linked worktree) and `commondir` like Hermes' _git_common_dir.
func readGitConfig(checkout string) (string, error) {
	dot := filepath.Join(checkout, ".git")
	st, err := os.Stat(dot)
	if err != nil {
		return "", err
	}
	gitdir := dot
	if !st.IsDir() {
		b, err := os.ReadFile(dot)
		if err != nil {
			return "", err
		}
		text := strings.TrimSpace(strings.TrimPrefix(string(b), "\ufeff"))
		if !strings.HasPrefix(text, "gitdir:") {
			return "", fmt.Errorf(".git is not a gitdir file")
		}
		gitdir = strings.TrimSpace(strings.TrimPrefix(text, "gitdir:"))
		if !filepath.IsAbs(gitdir) {
			gitdir = filepath.Join(checkout, gitdir)
		}
	}
	if b, err := os.ReadFile(filepath.Join(gitdir, "commondir")); err == nil {
		c := strings.TrimSpace(strings.TrimPrefix(string(b), "\ufeff"))
		if filepath.IsAbs(c) {
			gitdir = c
		} else {
			gitdir = filepath.Join(gitdir, c)
		}
	}
	b, err := os.ReadFile(filepath.Join(gitdir, "config"))
	return string(b), err
}

// ReadInstallID runs `<launcher> update --install-id` and returns the 16 hex
// digits it prints ("" when it cannot be read). Read-only, no network.
func ReadInstallID(ctx context.Context, run execx.Runner, launcher string) string {
	res, err := run.Run(ctx, execx.Cmd{Argv: []string{launcher, "update", "--install-id"}, Timeout: channelProbeTimeout, Env: probeEnv})
	if err != nil || res.TimedOut || res.Code != 0 {
		return ""
	}
	id, _ := ParseInstallID(res.Output)
	return id
}

// ResolveWithID is the file half of ResolveChannel, for a caller that read
// the install id itself (e.g. beside the capability probe, to save a Python
// start-up of wall time):
//
//  1. `update.installs.<id>.channel` in <home>/config.yaml (the default
//     profile's file; Safe Update runs the default profile);
//  2. no record: stable if <checkout>/.git/config has the official origin,
//     else main.
//
// Anything unexpected returns Source unknown with an empty Channel. It reads
// two files and writes nothing.
func ResolveWithID(id, home, checkout string) Resolved {
	unknown := Resolved{Source: SourceUnknown, InstallID: id}
	if home == "" {
		return unknown
	}
	if id == "" {
		return resolveByPath(home, checkout)
	}
	data, err := os.ReadFile(filepath.Join(home, "config.yaml"))
	switch {
	case err == nil:
		ch, found, ok := RecordChannel(data, id)
		if !ok {
			return unknown
		}
		if found {
			return Resolved{Channel: ch, Source: SourceRecord, InstallID: id}
		}
	case !os.IsNotExist(err):
		return unknown
	}
	text, err := readGitConfig(checkout)
	if err != nil {
		return unknown
	}
	if OfficialOrigin(text) {
		return Resolved{Channel: config.ChannelStable, Source: SourceDefault, InstallID: id}
	}
	return Resolved{Channel: config.ChannelMain, Source: SourceDefault, InstallID: id}
}

// resolveByPath is the fallback when `hermes update --install-id` could not
// be read: the record in config.yaml whose `path` is this checkout. Exactly
// one such record with a valid channel counts; no record, several, or a
// record without a channel is unknown (the caller then asks Hermes).
func resolveByPath(home, checkout string) Resolved {
	unknown := Resolved{Source: SourceUnknown}
	data, err := os.ReadFile(filepath.Join(home, "config.yaml"))
	if err != nil {
		return unknown
	}
	id, ch, ok := RecordChannelByPath(data, checkout, runtime.GOOS == "windows")
	if !ok {
		return unknown
	}
	return Resolved{Channel: ch, Source: SourceRecord, InstallID: id}
}

// RecordChannelByPath finds the `update.installs.<id>` record whose `path`
// is checkout (cleaned; compared without case when foldCase, as on Windows).
// ok only when exactly one record matches and its channel is a valid name.
func RecordChannelByPath(configYAML []byte, checkout string, foldCase bool) (id, channel string, ok bool) {
	if strings.TrimSpace(checkout) == "" {
		return "", "", false
	}
	configYAML = bytes.TrimPrefix(configYAML, []byte("\xef\xbb\xbf"))
	var doc any
	if err := yaml.Unmarshal(configYAML, &doc); err != nil {
		return "", "", false
	}
	root, isMap := asMap(doc)
	if !isMap {
		return "", "", false
	}
	upd, isMap := asMap(root["update"])
	if !isMap {
		return "", "", false
	}
	installs, isMap := asMap(upd["installs"])
	if !isMap {
		return "", "", false
	}
	want := cmpPath(checkout, foldCase)
	matches := 0
	for k, v := range installs {
		rec, isMap := asMap(v)
		if !isMap {
			continue
		}
		p, isStr := rec["path"].(string)
		if !isStr || strings.TrimSpace(p) == "" || cmpPath(p, foldCase) != want {
			continue
		}
		matches++
		id = k
		channel, _ = rec["channel"].(string)
	}
	if matches != 1 || !installIDRe.MatchString(id) || !ValidChannelName(channel) {
		return "", "", false
	}
	return id, channel, true
}

// cmpPath cleans a path for comparison: separators, trailing separators,
// and (foldCase) letter case.
func cmpPath(p string, foldCase bool) string {
	p = filepath.Clean(strings.TrimSpace(p))
	if foldCase {
		p = strings.ToLower(p)
	}
	return p
}

// ResolveChannel reads Hermes' effective update channel for the install
// without asking the network and without writing anything: the install id
// from `<launcher> update --install-id` (the caller checks ParseCaps first),
// then ResolveWithID.
func ResolveChannel(ctx context.Context, run execx.Runner, launcher, home, checkout string) Resolved {
	return ResolveWithID(ReadInstallID(ctx, run, launcher), home, checkout)
}

// ---- argv per mode ----

// baseUpdateArgv is what every run passes to `hermes update` (rule 6: no --gateway).
var baseUpdateArgv = []string{"update", "--yes", "--keep-stash"}

// Plan is how a run calls Hermes for one mode on one Hermes build.
type Plan struct {
	Mode     string   // configured mode: follow | stable | main
	Caps     Caps     // capability probe result
	Resolved Resolved // Hermes' own channel (follow only; zero otherwise)

	// Release is true on a release channel (stable, or follow resolving to a
	// non-main channel): Hermes' own `--check` is the only judge and main's
	// mirror / GitHub-compare chain is not used. False = the main path.
	Release bool
	// Effective is the channel the run acts on: "main", "stable", another
	// name, or "" while Hermes' choice is still unknown (Pending).
	Effective string
	// Pending: mode follow, Hermes has channels, but the offline read failed.
	// The bare `update --check` runs first; WithCheckedChannel completes the plan.
	Pending bool

	UpdateArgv []string // after the launcher
	CheckArgv  []string // after the launcher
	// Stop is a pre-flight stop reason (mode stable on a Hermes without
	// release channels). When set, the argv are nil.
	Stop string
	// Reason is the one-line explanation for the log.
	Reason string
}

func mainPlan(p Plan, reason string) Plan {
	p.Release, p.Pending, p.Effective, p.Reason = false, false, config.ChannelMain, reason
	p.UpdateArgv = append(append([]string(nil), baseUpdateArgv...), "--branch", "main")
	p.CheckArgv = []string{"update", "--check", "--branch", "main"}
	return p
}

func releasePlan(p Plan, effective, reason string) Plan {
	p.Release, p.Pending, p.Effective, p.Reason = true, false, effective, reason
	p.UpdateArgv = append([]string(nil), baseUpdateArgv...)
	p.CheckArgv = []string{"update", "--check"}
	return p
}

// BuildPlan maps a mode, the capabilities and (follow) Hermes' own channel to
// the argv of the run:
//
//	main    update --yes --keep-stash --branch main   (check: update --check --branch main)
//	        works on every Hermes; byte-identical to Safe Update before channels.
//	stable  base + --channel stable                   (check: update --check --channel stable)
//	        needs --channel in `update --help`, else Stop is set.
//	follow  without --channel: as main. With it: resolved main => as main;
//	        any other channel => base argv with no channel flag (check: bare
//	        update --check), so Hermes applies its own channel; unresolved =>
//	        Pending (bare check first).
//
// Safe Update never passes --set-channel.
func BuildPlan(mode string, caps Caps, res Resolved) Plan {
	p := Plan{Mode: mode, Caps: caps}
	switch mode {
	case config.ChannelMain:
		return mainPlan(p, "update.channel is main: --branch main on every Hermes version")
	case config.ChannelStable:
		if !caps.Channel {
			why := "this Hermes has no release channels"
			if !caps.Probed {
				why = "could not read `hermes update --help` (" + caps.Err + ") to check for release channels"
			}
			p.Stop = "update.channel is stable, but " + why + " (stable needs Hermes v0.21.6 or newer with release channels). " +
				"Set update.channel: main or follow in safe-update.yaml, or update Hermes first with `hermes update`. Nothing was changed."
			p.Reason = p.Stop
			return p
		}
		p = releasePlan(p, config.ChannelStable, "update.channel is stable: --channel stable")
		p.UpdateArgv = append(p.UpdateArgv, "--channel", config.ChannelStable)
		p.CheckArgv = append(p.CheckArgv, "--channel", config.ChannelStable)
		return p
	}
	// follow (and, defensively, anything else)
	p.Mode = config.ChannelFollow
	p.Resolved = res
	switch {
	case !caps.Channel:
		return mainPlan(p, "follow on a Hermes without release channels: --branch main (the pre-channel behaviour)")
	case res.Known() && res.Channel == config.ChannelMain:
		return mainPlan(p, "follow: Hermes' channel is main ("+string(res.Source)+"): --branch main")
	case res.Known():
		return releasePlan(p, res.Channel, "follow: Hermes' channel is "+res.Channel+" ("+string(res.Source)+"): no channel flag, Hermes decides")
	}
	p = releasePlan(p, "", "follow: Hermes' channel could not be read offline; asking `update --check`")
	p.Pending = true
	return p
}

// WithCheckedChannel completes a Pending plan with the channel named on the
// first line of Hermes' bare `update --check` (`→ Update channel: X`): main
// becomes exactly the main plan, anything else stays a release plan.
func (p Plan) WithCheckedChannel(channel string) Plan {
	if !p.Pending || channel == "" {
		return p
	}
	p.Resolved = Resolved{Channel: channel, Source: SourceCheck, InstallID: p.Resolved.InstallID}
	if channel == config.ChannelMain {
		return mainPlan(p, "follow: `update --check` reports channel main: --branch main")
	}
	return releasePlan(p, channel, "follow: `update --check` reports channel "+channel+": no channel flag, Hermes decides")
}

// BuildUpdateArgv is the argv (after the launcher) of `hermes update` for a
// mode; nil when the mode cannot run on this Hermes (BuildPlan(...).Stop).
func BuildUpdateArgv(mode string, caps Caps, res Resolved) []string {
	return BuildPlan(mode, caps, res).UpdateArgv
}

// BuildCheckArgv is the matching `hermes update --check` argv.
func BuildCheckArgv(mode string, caps Caps, res Resolved) []string {
	return BuildPlan(mode, caps, res).CheckArgv
}

// ---- rollback guard helper ----

var derivedVersionRe = regexp.MustCompile(`^v?(\d+\.\d+\.\d+)\+(\d+)\.g[0-9a-f]+`)

// AheadOfRelease reads how many commits a checkout is past a release from the
// version `hermes --version` derives (e.g. "v0.22.0+38.g1f6c02c" is 38 commits
// past v0.22.0). ok=false when the version is not of that shape or is based
// on another release.
func AheadOfRelease(current, releaseVersion string) (n int, ok bool) {
	m := derivedVersionRe.FindStringSubmatch(strings.TrimSpace(current))
	if m == nil || strings.TrimPrefix(releaseVersion, "v") != m[1] {
		return 0, false
	}
	if _, err := fmt.Sscanf(m[2], "%d", &n); err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// ---- switching the channel (the `channel set` command) ----

const (
	// channelCheckTimeout bounds the read-only release check made before a
	// switch to stable (it asks GitHub; no checkout change).
	channelCheckTimeout = 120 * time.Second
	// setChannelTimeout bounds `hermes update --set-channel` (Python start-up
	// plus one config write under Hermes' lock).
	setChannelTimeout = 60 * time.Second
)

// CheckStable runs `<launcher> update --check --channel stable` (the argv
// BuildCheckArgv gives): read-only, it asks GitHub which release stable
// selects and how the checkout compares. err is a failure to run it at all;
// a non-zero rc is in res.Code (Unresolved comes with rc 1).
func CheckStable(ctx context.Context, run execx.Runner, launcher string) (ChannelCheck, execx.Result, error) {
	argv := append([]string{launcher}, BuildCheckArgv(config.ChannelStable, Caps{Channel: true, Probed: true}, Resolved{})...)
	res, err := run.Run(ctx, execx.Cmd{Argv: argv, Timeout: channelCheckTimeout, Env: probeEnv})
	if err != nil {
		return ChannelCheck{}, res, err
	}
	if res.TimedOut {
		return ChannelCheck{}, res, fmt.Errorf("timed out after %s", channelCheckTimeout)
	}
	return ParseChannelCheck(res.Output), res, nil
}

// SetChannelResult is what `hermes update --set-channel` did.
type SetChannelResult struct {
	Code   int    // exit code (0 = Hermes wrote the record; 2 = Hermes refused)
	Output string // everything Hermes printed
	Done   string // the "Update channel for <id>: <name>" line ("" when not printed)
	Name   string // the channel in that line
}

// SetChannel switches the channel through Hermes' own command; Safe Update
// never edits Hermes' config.yaml. It is the only place the tool passes
// --set-channel, and only for the explicit `channel set` command.
func SetChannel(ctx context.Context, run execx.Runner, launcher, name string) (SetChannelResult, error) {
	res, err := run.Run(ctx, execx.Cmd{Argv: []string{launcher, "update", "--set-channel", name}, Timeout: setChannelTimeout, Env: probeEnv})
	if err != nil {
		return SetChannelResult{}, err
	}
	if res.TimedOut {
		return SetChannelResult{}, fmt.Errorf("timed out after %s", setChannelTimeout)
	}
	out := SetChannelResult{Code: res.Code, Output: res.Output}
	if m := SetChannelDoneRe.FindStringSubmatch(res.Output); m != nil {
		out.Done, out.Name = strings.TrimSpace(m[0]), m[2]
	}
	return out, nil
}
