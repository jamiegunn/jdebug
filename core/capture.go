package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The capture pipeline: acquire → validate → store-with-manifest.
//
// Tiers implement ONLY Acquirer. The pipeline owns validation and storage,
// so no tier can skip the checks (the v1 jattach tier did exactly that —
// findings F1/F5) and success can only be announced by the store step,
// after the validator has passed. A failed validation keeps the file on
// disk, records the verdict in the manifest, and returns an error.

// Meta describes what an acquirer produces.
type Meta struct {
	Name    string // artifact filename, e.g. "heap-jattach.hprof"
	Tier    string // provenance: "actuator" | "jattach" | "jdk" | ...
	Command string // the operator-visible command this ran (the show_cmd line)
}

// Acquirer fetches one artifact for a resolved target.
type Acquirer interface {
	Meta() Meta
	// Acquire produces the artifact at destPath (the pipeline pre-creates
	// it owner-only; kubectl-cp-style acquirers may overwrite it in place).
	// Size is the expected byte count when the source knows it (in-pod file
	// size before kubectl cp); return 0 when unknowable.
	Acquire(ctx context.Context, c Cluster, t Resolved, destPath string) (size int64, err error)
}

// Verdict is a validator's judgment of a captured file.
type Verdict struct {
	OK     bool
	Reason string // human explanation when !OK ("truncated: pod had N bytes, got M")
}

// Validator inspects a finished capture file. expected is the acquirer's
// declared size (0 = unknown).
type Validator func(path string, expected int64) Verdict

// Pipeline binds a cluster, a store and runs captures through the one path.
type Pipeline struct {
	Cluster Cluster
	Store   *Store
	// OutDir overrides the session directory for this run ($OUT_DIR in v1) —
	// captures land exactly there instead of <root>/pods/<pod>/<ts>/.
	OutDir string
}

// Run executes a non-destructive capture. Destructive captures (anything
// that pauses the JVM) must go through RunDestructive — the compiler, not a
// review checklist, enforces the difference.
func (p Pipeline) Run(ctx context.Context, acq Acquirer, t Resolved, validate Validator) (Artifact, error) {
	return p.run(ctx, acq, t, validate)
}

// RunDestructive is identical but only accepts a Confirmed target, which
// can only exist for an explicit or unambiguous pod (target.go).
func (p Pipeline) RunDestructive(ctx context.Context, acq Acquirer, t Confirmed, validate Validator) (Artifact, error) {
	return p.run(ctx, acq, t.Resolved(), validate)
}

func (p Pipeline) run(ctx context.Context, acq Acquirer, t Resolved, validate Validator) (Artifact, error) {
	if validate == nil {
		return Artifact{}, fmt.Errorf("pipeline: nil validator for %s — every capture must be validated", acq.Meta().Name)
	}
	var sess *Session
	var err error
	if p.OutDir != "" {
		sess, err = p.Store.SessionAt(p.OutDir, t.Pod, time.Now().UTC())
	} else {
		sess, err = p.Store.Session(t.Pod, time.Now().UTC())
	}
	if err != nil {
		return Artifact{}, err
	}
	m := acq.Meta()
	path := filepath.Join(sess.Dir, m.Name)
	// pre-create owner-only: heap dumps can hold real production data
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return Artifact{}, err
	}
	_ = f.Close()
	expected, aerr := acq.Acquire(ctx, p.Cluster, t, path)
	if aerr != nil {
		_ = os.Remove(path) // an acquire failure leaves nothing half-written behind
		return Artifact{}, fmt.Errorf("capture %s (tier %s): %w", m.Name, m.Tier, aerr)
	}

	v := validate(path, expected)
	art := Artifact{
		Name: m.Name, Tier: m.Tier, Command: m.Command,
		CapturedAt: time.Now().UTC(),
		Verdict:    v,
	}
	if st, err := os.Stat(path); err == nil {
		art.Bytes = st.Size()
	}
	art.SHA256, _ = fileSHA256(path)

	// The manifest records the capture either way — an invalid file kept
	// for inspection must be distinguishable from a good one forever after.
	if err := sess.Append(art); err != nil {
		return art, err
	}
	if !v.OK {
		return art, fmt.Errorf("capture %s (tier %s) FAILED validation: %s — file kept for inspection: %s",
			m.Name, m.Tier, v.Reason, path)
	}
	return art, nil
}

// --- validators -----------------------------------------------------------

// ValidateHprof: the magic catches non-dumps (login pages, JSON errors);
// the size comparison catches truncation, which magic alone cannot (a
// truncated hprof still begins with a valid header).
func ValidateHprof(path string, expected int64) Verdict {
	f, err := os.Open(path)
	if err != nil {
		return Verdict{false, "unreadable: " + err.Error()}
	}
	defer f.Close()
	head := make([]byte, 12)
	n, _ := io.ReadFull(f, head)
	if n < 12 || !strings.HasPrefix(string(head), "JAVA PROFILE") {
		return Verdict{false, "not an hprof (bad magic) — " + classifyHead(path)}
	}
	if st, err := f.Stat(); err == nil && expected > 0 && st.Size() != expected {
		return Verdict{false, fmt.Sprintf("TRUNCATED in transit: source had %d bytes, copy has %d", expected, st.Size())}
	}
	return Verdict{OK: true}
}

// ValidateThreadDump: jstack-style dumps carry the marker; anything else is
// an error page or a refused attach masquerading as a capture.
func ValidateThreadDump(path string, _ int64) Verdict {
	b, err := os.ReadFile(path)
	if err != nil {
		return Verdict{false, "unreadable: " + err.Error()}
	}
	if !strings.Contains(string(b), "Full thread dump") {
		return Verdict{false, "no 'Full thread dump' marker — " + classifyHead(path)}
	}
	return Verdict{OK: true}
}

// classifyHead names what a bad capture actually looks like, so the
// operator fixes the route instead of opening an error page in MAT.
func classifyHead(path string) string {
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		return "empty file (the capture command produced no output)"
	}
	head := string(b[:min(len(b), 512)])
	low := strings.ToLower(head)
	switch {
	case strings.Contains(low, "<html") || strings.Contains(low, "<!doctype html"):
		if strings.Contains(low, "login") || strings.Contains(low, "sign in") || strings.Contains(low, "password") {
			return "looks like an HTML login page — the endpoint is secured"
		}
		return "looks like an HTML error page"
	case strings.HasPrefix(strings.TrimSpace(head), "{"):
		return "looks like a JSON error response (Spring/actuator error)"
	default:
		return "unrecognized content"
	}
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
