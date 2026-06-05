package main

// Goalpost progress collection.
//
// A project opts in by adding `.goalpost/measures/` containing self-describing
// measure scripts (bash by default, python when needed). Each script prints one
// JSON object per line to stdout describing a *measure* of ground-truth progress
// toward a goal — never the agent's self-report. See .claude/skills/goalpost.
//
// md runs each script on its own schedule (60s floor, backing off to the
// script's own runtime / declared period), derives pass/fail, and appends a
// point to a MetricStore *only when the value changes* (store-on-change keeps
// history compact and makes the stored series the set of meaningful events).
//
// Collection runs in-process for the project md was launched in, so history is
// only gathered while md is open; the JSONL store persists it across restarts.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	measureFloor   = 60 * time.Second // fastest any measure is sampled
	measureTimeout = 10 * time.Minute // a hung ruler becomes an errored point
	discoverEvery  = 60 * time.Second // rescan for newly-authored measures
)

// MetricPoint is one stored measurement. Script-supplied fields are merged with
// md-stamped fields (TS, Project, Source, Status, derived Passed).
type MetricPoint struct {
	TS             time.Time `json:"ts"`
	Project        string    `json:"project,omitempty"`
	Goal           string    `json:"goal"`
	Measure        string    `json:"measure"`
	Kind           string    `json:"kind,omitempty"` // "gate" | "trend"
	Value          float64   `json:"value"`
	Target         *float64  `json:"target,omitempty"`
	HigherIsBetter bool      `json:"higher_is_better"`
	Unit           string    `json:"unit,omitempty"`
	Label          string    `json:"label,omitempty"`
	Rung           string    `json:"rung,omitempty"` // "deterministic" | "judge"
	Passed         *bool     `json:"passed,omitempty"`
	Status         string    `json:"status"` // "ok" | "errored"
	Error          string    `json:"error,omitempty"`
	Source         string    `json:"source,omitempty"`
}

// key identifies a measure series within a project.
func (p MetricPoint) key() string { return p.Goal + "\x00" + p.Measure }

// signature captures the fields whose change makes a point worth storing.
func (p MetricPoint) signature() string {
	passed := ""
	if p.Passed != nil {
		passed = fmt.Sprintf("%t", *p.Passed)
	}
	target := ""
	if p.Target != nil {
		target = fmt.Sprintf("%g", *p.Target)
	}
	return fmt.Sprintf("%s|%g|%s|%s|%s", p.Status, p.Value, passed, target, p.Error)
}

// MetricStore is the seam between the collector (producer) and the server
// (consumer). The location/backend is an implementation detail; swap in SQLite
// or a remote sink by satisfying these three methods.
type MetricStore interface {
	// Append stores the point only if it differs from the last point for its
	// series (store-on-change). Returns whether a new point was written.
	Append(p MetricPoint) (bool, error)
	// Latest returns the most-recent point per measure (forward-filled current
	// state), optionally filtered to one goal.
	Latest(goal string) ([]MetricPoint, error)
	// History returns the change-series for one measure within a time range.
	History(goal, measure string, from, to time.Time) ([]MetricPoint, error)
}

// jsonlStore is the default MetricStore: an append-only JSONL file per project.
type jsonlStore struct {
	mu      sync.Mutex
	path    string
	points  []MetricPoint          // full change-history, in memory
	last    map[string]MetricPoint // most-recent point per series
	lastsig map[string]string      // signature of last stored point per series
}

// newJSONLStore opens (creating if needed) the per-project metrics file under
// baseDir, keyed by a hash of the project path, and loads existing history.
func newJSONLStore(baseDir, project string) (*jsonlStore, error) {
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(project))
	path := filepath.Join(baseDir, hex.EncodeToString(sum[:])[:16]+".jsonl")
	s := &jsonlStore{
		path:    path,
		last:    map[string]MetricPoint{},
		lastsig: map[string]string{},
	}
	if data, err := os.ReadFile(path); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var p MetricPoint
			if json.Unmarshal([]byte(line), &p) != nil {
				continue
			}
			s.points = append(s.points, p)
			s.last[p.key()] = p
			s.lastsig[p.key()] = p.signature()
		}
	}
	return s, nil
}

func (s *jsonlStore) Append(p MetricPoint) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := p.key()
	if prev, ok := s.lastsig[k]; ok && prev == p.signature() {
		s.last[k] = p // refresh metadata/timestamp in memory, but don't persist
		return false, nil
	}
	line, err := json.Marshal(p)
	if err != nil {
		return false, err
	}
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return false, err
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return false, err
	}
	s.points = append(s.points, p)
	s.last[k] = p
	s.lastsig[k] = p.signature()
	return true, nil
}

func (s *jsonlStore) Latest(goal string) ([]MetricPoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]MetricPoint, 0, len(s.last))
	for _, p := range s.last {
		if goal == "" || p.Goal == goal {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Goal != out[j].Goal {
			return out[i].Goal < out[j].Goal
		}
		return out[i].Measure < out[j].Measure
	})
	return out, nil
}

func (s *jsonlStore) History(goal, measure string, from, to time.Time) ([]MetricPoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []MetricPoint
	for _, p := range s.points {
		if p.Goal != goal || p.Measure != measure {
			continue
		}
		if !from.IsZero() && p.TS.Before(from) {
			continue
		}
		if !to.IsZero() && p.TS.After(to) {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

// scriptMetric is the script-controllable subset of a point. Pointer fields let
// us tell "absent" from "zero" (e.g. higher_is_better defaults to true).
type scriptMetric struct {
	Goal           string          `json:"goal"`
	Measure        string          `json:"measure"`
	Kind           string          `json:"kind"`
	Value          json.RawMessage `json:"value"`
	Target         *float64        `json:"target"`
	HigherIsBetter *bool           `json:"higher_is_better"`
	Unit           string          `json:"unit"`
	Label          string          `json:"label"`
	Rung           string          `json:"rung"`
	PeriodS        int             `json:"period_s"`
}

// coerceValue accepts a number or a bool (true=1, false=0).
func coerceValue(raw json.RawMessage) (float64, bool) {
	var f float64
	if json.Unmarshal(raw, &f) == nil {
		return f, true
	}
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		if b {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

func derivePassed(kind string, value float64, target *float64, higher bool) *bool {
	if kind != "gate" {
		return nil
	}
	t := 1.0 // a gate with no threshold is a boolean: value>=1 means met
	if target != nil {
		t = *target
	}
	var p bool
	if higher {
		p = value >= t
	} else {
		p = value <= t
	}
	return &p
}

// collector runs a project's measures on independent per-measure schedules and
// writes results to the store.
type collector struct {
	project     string
	measuresDir string
	store       MetricStore

	mu      sync.Mutex
	running map[string]bool // script path -> goroutine started
}

func newCollector(project string, store MetricStore) *collector {
	return &collector{
		project:     project,
		measuresDir: filepath.Join(project, ".goalpost", "measures"),
		store:       store,
		running:     map[string]bool{},
	}
}

// start launches the discovery loop. It returns immediately; collection happens
// in background goroutines for the life of the process.
func (c *collector) start(ctx context.Context) {
	go func() {
		t := time.NewTicker(discoverEvery)
		defer t.Stop()
		c.discover(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				c.discover(ctx)
			}
		}
	}()
}

// discover starts a runner goroutine for each measure script not already running.
func (c *collector) discover(ctx context.Context) {
	entries, err := os.ReadDir(c.measuresDir)
	if err != nil {
		return // no .goalpost/measures yet; feature dormant
	}
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		path := filepath.Join(c.measuresDir, e.Name())
		c.mu.Lock()
		started := c.running[path]
		if !started {
			c.running[path] = true
		}
		c.mu.Unlock()
		if !started {
			go c.runLoop(ctx, path)
		}
	}
}

func (c *collector) runLoop(ctx context.Context, script string) {
	for {
		delay := c.runOnce(ctx, script)
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}

// runOnce executes a measure script, stores any points it emits, and returns the
// delay until the next run (floor, backed off to the declared period).
func (c *collector) runOnce(ctx context.Context, script string) time.Duration {
	runCtx, cancel := context.WithTimeout(ctx, measureTimeout)
	defer cancel()

	start := time.Now()
	cmd := interpreter(runCtx, script)
	cmd.Dir = c.project
	out, runErr := cmd.Output()
	elapsed := time.Since(start)

	points, periodHint := parseMetrics(out, c.project, script)

	if len(points) == 0 {
		// No parseable measurement: the ruler itself broke. Make it visible.
		msg := "no metrics emitted"
		if runErr != nil {
			msg = runErr.Error()
		}
		if ee, ok := runErr.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			msg = strings.TrimSpace(string(ee.Stderr))
		}
		points = []MetricPoint{{
			Goal:    "",
			Measure: strings.TrimSuffix(filepath.Base(script), filepath.Ext(script)),
			Status:  "errored",
			Error:   msg,
			Source:  relSource(c.project, script),
			TS:      time.Now(),
			Project: c.project,
		}}
	}
	for _, p := range points {
		_, _ = c.store.Append(p)
	}

	delay := measureFloor
	if elapsed > delay {
		delay = elapsed // never pile a slow measure on itself
	}
	if periodHint > 0 {
		if d := time.Duration(periodHint) * time.Second; d > delay {
			delay = d
		}
	}
	return delay
}

// parseMetrics turns a script's stdout into points, deriving Passed and stamping
// md-owned fields. Non-JSON lines are ignored. Returns the max declared period.
func parseMetrics(out []byte, project, script string) ([]MetricPoint, int) {
	var points []MetricPoint
	periodHint := 0
	now := time.Now()
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var m scriptMetric
		if json.Unmarshal([]byte(line), &m) != nil || m.Measure == "" {
			continue
		}
		value, ok := coerceValue(m.Value)
		if !ok {
			continue
		}
		higher := true
		if m.HigherIsBetter != nil {
			higher = *m.HigherIsBetter
		}
		kind := m.Kind
		if kind == "" {
			kind = "trend"
		}
		rung := m.Rung
		if rung == "" {
			rung = "deterministic"
		}
		if m.PeriodS > periodHint {
			periodHint = m.PeriodS
		}
		points = append(points, MetricPoint{
			TS:             now,
			Project:        project,
			Goal:           m.Goal,
			Measure:        m.Measure,
			Kind:           kind,
			Value:          value,
			Target:         m.Target,
			HigherIsBetter: higher,
			Unit:           m.Unit,
			Label:          m.Label,
			Rung:           rung,
			Passed:         derivePassed(kind, value, m.Target, higher),
			Status:         "ok",
			Source:         relSource(project, script),
		})
	}
	return points, periodHint
}

// interpreter picks how to run a measure script: by extension, else directly.
func interpreter(ctx context.Context, script string) *exec.Cmd {
	switch strings.ToLower(filepath.Ext(script)) {
	case ".sh":
		return exec.CommandContext(ctx, "sh", script)
	case ".py":
		return exec.CommandContext(ctx, "python3", script)
	default:
		return exec.CommandContext(ctx, script)
	}
}

func relSource(project, script string) string {
	if rel, err := filepath.Rel(project, script); err == nil {
		return filepath.ToSlash(rel)
	}
	return script
}

// goalpostStoreDir is the default central location for metrics history.
func goalpostStoreDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".md", "goalpost")
	}
	return filepath.Join(os.TempDir(), "md-goalpost")
}

// handleGoalpost serves current state (Latest) and per-measure history.
//
//	GET /api/goalpost                       -> {measures: [latest per measure]}
//	GET /api/goalpost?goal=G                -> latest, filtered to goal G
//	GET /api/goalpost?goal=G&measure=M      -> {history: [change-series for M]}
func handleGoalpost(store MetricStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		goal := r.URL.Query().Get("goal")
		measure := r.URL.Query().Get("measure")
		w.Header().Set("Content-Type", "application/json")
		if measure != "" {
			from := parseTime(r.URL.Query().Get("from"))
			to := parseTime(r.URL.Query().Get("to"))
			hist, err := store.History(goal, measure, from, to)
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"history": hist})
			return
		}
		latest, err := store.Latest(goal)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"measures": latest})
	}
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	return time.Time{}
}
