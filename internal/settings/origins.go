package settings

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/hermes"
)

// originRe finds the origins this tool handles in raw LevelDB bytes, only
// in key position (Chromium keys are "META:<origin>" and "_<origin>\x00<key>"),
// so a stored value that mentions a local URL is not taken for an origin.
// The scan is read-only and only proposes CANDIDATES (block compression may
// hide a name): the migrator's dump, run on a copy through Chromium itself,
// is what confirms an origin and counts its keys.
var originRe = regexp.MustCompile(`(?:META:|_)(file://|http://127\.0\.0\.1:[0-9]{2,5})`)

// lockFile is LevelDB's lock file: never copied, hashed or restored (it is
// empty, held open by a running app, and recreated on open).
const lockFile = "LOCK"

// scanOrigins returns every candidate origin in <lsDir>/leveldb with the
// newest modification time of a file that mentions it (its recency).
func scanOrigins(lsDir string) (map[string]time.Time, error) {
	db := filepath.Join(lsDir, "leveldb")
	entries, err := os.ReadDir(db)
	if err != nil {
		return nil, fmt.Errorf("read settings store %q: %w", db, err)
	}
	out := map[string]time.Time{}
	for _, e := range entries {
		if e.IsDir() || e.Name() == lockFile {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return nil, fmt.Errorf("stat %q: %w", e.Name(), err)
		}
		b, err := os.ReadFile(filepath.Join(db, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("read %q: %w", e.Name(), err)
		}
		seen := map[string]bool{}
		for _, m := range originRe.FindAllSubmatch(b, -1) {
			seen[string(m[1])] = true
		}
		for o := range seen {
			if info.ModTime().After(out[o]) {
				out[o] = info.ModTime()
			}
		}
	}
	return out, nil
}

// storeExists reports whether userData holds a Local Storage LevelDB.
func storeExists(lsDir string) bool {
	st, err := os.Stat(filepath.Join(lsDir, "leveldb"))
	return err == nil && st.IsDir()
}

// fileOrigin is the localStorage origin of a renderer loaded from disk.
const fileOrigin = "file://"

// fileReason says why file:// is the target when the build has no
// renderer-server.ts (RT-4F).
const fileReason = "renderer loaded from file:// (no renderer-server.ts in this build)"

// defaultPortOrigin reads DEFAULT_PORT from the installed build's
// renderer-server.ts (D9). A build without that file whose main.ts loads
// the renderer from disk (pathToFileURL(resolveRendererIndex...)) and does
// not mention renderer-server yields file:// (RT-4F). Anything else is an
// error: the origin is never guessed.
func defaultPortOrigin(checkout string) (string, error) {
	p := filepath.Join(checkout, filepath.FromSlash(hermes.RendererServerSource))
	b, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fileOriginFromMain(checkout)
		}
		return "", fmt.Errorf("read %q: %w", p, err)
	}
	m := hermes.DesktopDefaultPortRe.FindSubmatch(b)
	if m == nil {
		return "", fmt.Errorf("cannot detect the target origin: no DEFAULT_PORT in %s (Hermes changed; pass --origin)", hermes.RendererServerSource)
	}
	return "http://127.0.0.1:" + string(m[1]), nil
}

// fileOriginFromMain decides file:// for a build without renderer-server.ts.
func fileOriginFromMain(checkout string) (string, error) {
	notFound := fmt.Errorf("cannot detect the target origin: %s not found and %s does not show a file:// renderer load (Hermes changed; pass --origin)",
		hermes.RendererServerSource, hermes.DesktopMainSource)
	b, err := os.ReadFile(filepath.Join(checkout, filepath.FromSlash(hermes.DesktopMainSource)))
	if err != nil {
		return "", notFound
	}
	if hermes.RendererServerMentionRe.Match(b) || !hermes.RendererFileLoadRe.Match(b) {
		return "", notFound
	}
	return fileOrigin, nil
}

// chooseTarget applies D9: an explicit origin wins; else the build's default
// port, unless the store already holds a different 127.0.0.1 origin that is
// newer than the default one (or the default one is absent): the app is
// then using a fallback port, so migrate into that and say so.
func chooseTarget(override, def string, found map[string]time.Time) Target {
	if override != "" {
		return Target{URL: override, Reason: "--origin / settings.origin"}
	}
	if def == fileOrigin {
		// no local server in this build: there is no fallback port to prefer
		return Target{URL: fileOrigin, Reason: fileReason}
	}
	best, bestT := "", time.Time{}
	for o, t := range found {
		if o == def || o == "file://" {
			continue
		}
		if best == "" || t.After(bestT) || (t.Equal(bestT) && o > best) {
			best, bestT = o, t
		}
	}
	defT, defIn := found[def]
	if best != "" && (!defIn || bestT.After(defT)) {
		port := best[len("http://127.0.0.1:"):]
		return Target{URL: best, Reason: fmt.Sprintf("store already uses fallback port %s (newer than DEFAULT_PORT %s)", port, def[len("http://127.0.0.1:"):])}
	}
	return Target{URL: def, Reason: "DEFAULT_PORT in renderer-server.ts"}
}

// chooseSource picks the origin to migrate from: the most recently used
// origin other than the target that has keys ("" = nothing to migrate).
// After file:// -> 47891 and a later port change, that is 47891. Recency
// ties are normal after LevelDB compaction (several origins in one .ldb),
// so a tie prefers an origin that was a migration target before
// (prevTargets, from the marker), then http origins over file://, then the
// lexically smaller URL: the stale file:// never wins a tie.
func chooseSource(target string, keys map[string]int, recency map[string]time.Time, prevTargets map[string]bool) string {
	var cands []string
	for o, n := range keys {
		if o != target && n > 0 {
			cands = append(cands, o)
		}
	}
	sort.Slice(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		if ta, tb := recency[a], recency[b]; !ta.Equal(tb) {
			return ta.After(tb)
		}
		if prevTargets[a] != prevTargets[b] {
			return prevTargets[a]
		}
		if fa, fb := a == "file://", b == "file://"; fa != fb {
			return fb
		}
		return a < b
	})
	if len(cands) == 0 {
		return ""
	}
	return cands[0]
}
