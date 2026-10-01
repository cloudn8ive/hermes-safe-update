// Package logx sets up the log file: log/slog text records, size-based
// rotation, and redaction of user paths (the home and local-app-data
// prefixes become `~` / `%LOCALAPPDATA%`) in messages and string attributes,
// because logs get pasted into bug reports.
package logx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"regexp"
	"strings"
)

// Redactor replaces user-specific path prefixes.
type Redactor struct {
	rules []rule
}

type rule struct {
	re   *regexp.Regexp
	repl string
}

// NewRedactor builds a redactor for the user's home and (Windows) local app
// data directory. Empty arguments are skipped. Matching is case-insensitive
// and accepts either separator; a prefix only matches at a path boundary.
func NewRedactor(home, localAppData string) *Redactor {
	r := &Redactor{}
	add := func(prefix, repl string) {
		prefix = strings.TrimRight(prefix, `\/`)
		if prefix == "" {
			return
		}
		var b strings.Builder
		b.WriteString(`(?i)`)
		for _, ch := range prefix {
			if ch == '\\' || ch == '/' {
				b.WriteString(`[\\/]`)
			} else {
				b.WriteString(regexp.QuoteMeta(string(ch)))
			}
		}
		b.WriteString(`([\\/]|$|[\s"'])`)
		r.rules = append(r.rules, rule{re: regexp.MustCompile(b.String()), repl: repl + "${1}"})
	}
	// Longest first: local app data lives under home.
	add(localAppData, "%LOCALAPPDATA%")
	add(home, "~")
	return r
}

// String redacts s.
func (r *Redactor) String(s string) string {
	if r == nil {
		return s
	}
	for _, ru := range r.rules {
		s = ru.re.ReplaceAllString(s, ru.repl)
	}
	return s
}

// NewHandler returns a slog text handler writing to w that redacts the
// message and every string attribute.
func NewHandler(w io.Writer, level slog.Leveler, r *Redactor) slog.Handler {
	inner := slog.NewTextHandler(w, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Value.Kind() == slog.KindString {
				return slog.String(a.Key, r.String(a.Value.String()))
			}
			return a
		},
	})
	return &redactHandler{Handler: inner, r: r}
}

type redactHandler struct {
	slog.Handler
	r *Redactor
}

func (h *redactHandler) Handle(ctx context.Context, rec slog.Record) error {
	rec.Message = h.r.String(rec.Message)
	return h.Handler.Handle(ctx, rec)
}

func (h *redactHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return &redactHandler{Handler: h.Handler.WithAttrs(as), r: h.r}
}

func (h *redactHandler) WithGroup(name string) slog.Handler {
	return &redactHandler{Handler: h.Handler.WithGroup(name), r: h.r}
}

// ParseLevel maps the config/flag value to a slog level.
func ParseLevel(s string) (slog.Level, error) {
	switch s {
	case "debug":
		return slog.LevelDebug, nil
	case "info", "":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return slog.LevelInfo, fmt.Errorf("unknown log level %q", s)
}

// OpenFile opens path for appending, first rotating it to path.1 (path.1 to
// path.2, ...) when it is larger than maxBytes; keep old files are kept.
// maxBytes <= 0 disables rotation.
func OpenFile(path string, maxBytes int64, keep int) (*os.File, error) {
	if st, err := os.Stat(path); err == nil && maxBytes > 0 && st.Size() > maxBytes {
		if keep < 1 {
			_ = os.Remove(path)
		} else {
			_ = os.Remove(fmt.Sprintf("%s.%d", path, keep))
			for i := keep - 1; i >= 1; i-- {
				from := fmt.Sprintf("%s.%d", path, i)
				if _, err := os.Stat(from); err == nil {
					_ = os.Rename(from, fmt.Sprintf("%s.%d", path, i+1))
				}
			}
			if err := os.Rename(path, path+".1"); err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, fmt.Errorf("rotate log %q: %w", path, err)
			}
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open log %q: %w", path, err)
	}
	return f, nil
}
