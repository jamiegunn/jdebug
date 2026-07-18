package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// The evidence store. v1 encoded provenance in filenames and session
// structure in directory layout; every consumer re-derived meaning by
// re-parsing paths. v2 keeps the human-friendly layout
// (<root>/pods/<pod>/<ts>/) but the truth lives in manifest.json — what was
// captured, by which tier, from which command, how many bytes, which
// sha256, and whether it passed validation. "Is this capture complete and
// where did it come from" becomes answerable after the fact.

// Artifact is one captured file's record.
type Artifact struct {
	Name       string    `json:"name"`
	Tier       string    `json:"tier"`
	Command    string    `json:"command,omitempty"`
	Bytes      int64     `json:"bytes"`
	SHA256     string    `json:"sha256,omitempty"`
	CapturedAt time.Time `json:"captured_at"`
	Verdict    Verdict   `json:"verdict"`
	// Path is where the file actually lives on disk (runtime-only, not
	// persisted — the manifest sits beside its files). Callers must print
	// THIS, never a path reconstructed from CapturedAt: the session dir is
	// timestamped at pipeline start, so any capture that crosses a second
	// boundary makes a reconstructed path point at nothing.
	Path string `json:"-"`
}

// Manifest is a capture session's full record.
type Manifest struct {
	Pod       string     `json:"pod"`
	StartedAt time.Time  `json:"started_at"`
	Artifacts []Artifact `json:"artifacts"`
}

// Store roots the evidence tree (v1's $JDEBUG_DUMPS).
type Store struct {
	Root string
}

// Session is one capture session's directory + manifest.
type Session struct {
	Dir string
	pod string
}

// Session opens (creating if needed) the session dir for pod at ts —
// <root>/pods/<pod>/<ts>/ — owner-only, because heap dumps can hold real
// production data.
func (s *Store) Session(pod string, ts time.Time) (*Session, error) {
	dir := filepath.Join(s.Root, "pods", pod, ts.Format("20060102T150405Z"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	sess := &Session{Dir: dir, pod: pod}
	if _, err := os.Stat(sess.manifestPath()); os.IsNotExist(err) {
		m := Manifest{Pod: pod, StartedAt: ts}
		if err := sess.write(m); err != nil {
			return nil, err
		}
	}
	return sess, nil
}

// SessionAt opens a session at an explicit directory (the $OUT_DIR
// override) — same manifest, same owner-only permissions.
func (s *Store) SessionAt(dir, pod string, ts time.Time) (*Session, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	sess := &Session{Dir: dir, pod: pod}
	if _, err := os.Stat(sess.manifestPath()); os.IsNotExist(err) {
		if err := sess.write(Manifest{Pod: pod, StartedAt: ts}); err != nil {
			return nil, err
		}
	}
	return sess, nil
}

func (s *Session) manifestPath() string { return filepath.Join(s.Dir, "manifest.json") }

// Read returns the session's manifest.
func (s *Session) Read() (Manifest, error) {
	var m Manifest
	b, err := os.ReadFile(s.manifestPath())
	if err != nil {
		return m, err
	}
	err = json.Unmarshal(b, &m)
	return m, err
}

// Append records one artifact.
func (s *Session) Append(a Artifact) error {
	m, err := s.Read()
	if err != nil {
		return err
	}
	m.Artifacts = append(m.Artifacts, a)
	return s.write(m)
}

func (s *Session) write(m Manifest) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.manifestPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.manifestPath())
}
