package main

import (
	"context"
	"sync"
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/apperr"
	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
	"github.com/cloudn8ive/hermes-safe-update/internal/statedb"
)

// e2eSD is a session database service with two databases over Hermes' limit.
type e2eSD struct {
	mu     sync.Mutex
	dbs    []statedb.DB
	copied []string
	work   map[string]statedb.Compaction // per profile; unlisted = has work
	real   bool                          // run the real probe on the file at the path
}

func (s *e2eSD) CompactionWork(ctx context.Context, path string) statedb.Compaction {
	if s.real {
		return statedb.CompactionWork(ctx, path)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.dbs {
		if d.Path == path {
			if c, ok := s.work[d.Profile]; ok {
				return c
			}
		}
	}
	return statedb.Compaction{Verdict: statedb.Work, Reason: "scripted"}
}

func newE2ESD(home string) *e2eSD {
	return &e2eSD{dbs: []statedb.DB{
		{Profile: "default", Path: home + `\state.db`, Size: 1<<30 + 1<<28},
		{Profile: "work", Path: home + `\profiles\work\state.db`, Size: 4<<30 + 1<<29},
	}}
}

func (s *e2eSD) Find(string) []statedb.DB {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]statedb.DB(nil), s.dbs...)
}

func (s *e2eSD) Ignored(string) []string { return nil }

func (s *e2eSD) Backup(_ context.Context, req statedb.BackupReq) (statedb.BackupResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.copied = append(s.copied, req.Src.Profile)
	return statedb.BackupResult{Path: req.Dir + `\` + req.Src.Profile + "-" + req.Stamp + ".db", Size: req.Src.Size}, nil
}

func (s *e2eSD) resize(profile string, n int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.dbs {
		if s.dbs[i].Profile == profile {
			s.dbs[i].Size = n
		}
	}
}

func TestE2EStateDBOldHermesKeepsOwnCopyAndSaysSo(t *testing.T) {
	w := newWorld(t)
	w.version = "v0.22.7"
	sd := newE2ESD(w.home)
	w.sd = sd
	w.con.script(conStep{key: 'y'})
	w.cli("--plain")
	w.wantCode(apperr.OK)
	w.wantScreen("state.db", "work 4.5 GB, default 1.2 GB", "Safe Update kept its own verified copy",
		"compaction needs Hermes 0.23 or newer; this is 0.22.7", "Hermes is updated and verified")
	w.noScreen("Compact the session database")
	if len(sd.copied) != 2 || w.ran(w.launcher, "sessions") {
		t.Errorf("copied %v, optimize ran: %v", sd.copied, w.ran(w.launcher, "sessions"))
	}
}

func TestE2EStateDBCompactionThenUpdate(t *testing.T) {
	w := newWorld(t)
	w.version = "v0.23.1"
	sd := newE2ESD(w.home)
	w.sd = sd
	w.optimize = map[string]execx.Result{}
	w.optimize["sessions optimize-storage --yes"] = execx.Result{Output: "✓ Search index optimized."}
	w.optimize["-p work sessions optimize-storage --yes"] = execx.Result{Output: "✓ Search index optimized."}
	w.con.script(conStep{key: 'y'}, conStep{key: 'y', then: func(*e2eWorld) {
		sd.resize("default", 500<<20)
		sd.resize("work", 2<<30)
	}})
	w.vt = true // the question needs a console
	w.cli()
	w.wantCode(apperr.OK)
	w.wantScreen("Compact the session database(s) before updating?", "db compaction", "work 4.5 GB -> 2.0 GB",
		"Hermes is updated and verified")
	if len(sd.copied) != 2 || sd.copied[0] != "default" || sd.copied[1] != "work" {
		t.Errorf("copied %v: each one before its own compaction, none after", sd.copied)
	}
	if !w.ran(w.launcher, "-p", "work", "sessions", "optimize-storage", "--yes") {
		t.Errorf("calls: %v", w.Calls())
	}
}

func TestE2EStateDBUnattendedCopiesButNeverCompacts(t *testing.T) {
	w := newWorld(t)
	w.version = "v0.23.1"
	sd := newE2ESD(w.home)
	w.sd = sd
	w.cli("--plain", "--unattended")
	w.wantCode(apperr.OK)
	w.noScreen("Compact the session database")
	if len(sd.copied) != 2 || w.ran(w.launcher, "sessions") {
		t.Errorf("copied %v, optimize ran: %v", sd.copied, w.ran(w.launcher, "sessions"))
	}
}

func TestE2EStateDBCheckShowsOneRowAndChangesNothing(t *testing.T) {
	w := newWorld(t)
	w.version = "v0.23.1"
	sd := newE2ESD(w.home)
	w.sd = sd
	before := w.storeBytes()
	w.cli("--plain", "check")
	w.wantCode(apperr.OK)
	w.wantScreen("state.db", "work 4.5 GB, default 1.2 GB over Hermes' 1 GiB snapshot limit", "an attended update offers to compact it first")
	w.nothingChanged(before)
	if len(sd.copied) != 0 {
		t.Errorf("check copied %v", sd.copied)
	}
}
