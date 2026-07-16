package core

import (
	"context"
	"errors"
	"fmt"
)

// Target is what the operator asked for; Resolved is a target the cluster
// agreed exists. Confirmed is the only currency destructive operations
// accept — and it can only be minted for an explicit or unambiguous pod.
// That encodes the F8 rule ("never pause a guessed replica") in the type
// system instead of in an environment variable four scripts must remember
// to export.

type Target struct {
	Namespace string
	Selector  string // "" = any pod in the namespace
	Pod       string // "" = resolve from selector
	Container string
}

// Resolved is a Target whose Pod is known to exist right now.
type Resolved struct {
	Target
	// Explicit records whether the operator named the pod themselves
	// (vs. the resolver picking the first match).
	Explicit bool
	// Matches is every pod the selector matched — surfaced so callers can
	// tell the operator what else was there (the sick-pod trap).
	Matches []string
}

// Confirmed wraps a Resolved that is safe to aim a destructive operation
// at. It is unexported-by-construction: the only way to obtain one is
// Resolved.Confirm, which enforces the unambiguity rule.
type Confirmed struct {
	r Resolved
}

// Resolved returns the underlying resolved target.
func (c Confirmed) Resolved() Resolved { return c.r }

var (
	// ErrNoPods — nothing matched; a target problem, not a tier problem.
	ErrNoPods = errors.New("no pod matched")
	// ErrAmbiguous — several pods matched and the operation needs exactly one.
	ErrAmbiguous = errors.New("several pods match — name the pod explicitly")
)

// Resolve turns a Target into a Resolved one. With an explicit Pod it
// verifies nothing (kubectl will — and its error is clearer); otherwise it
// lists matches and picks the first, recording the alternatives.
func Resolve(ctx context.Context, c Cluster, t Target) (Resolved, error) {
	if t.Pod != "" {
		return Resolved{Target: t, Explicit: true, Matches: []string{t.Pod}}, nil
	}
	pods, err := c.PodsMatching(ctx, t.Namespace, t.Selector)
	if err != nil {
		return Resolved{}, err
	}
	if len(pods) == 0 {
		return Resolved{}, fmt.Errorf("%w (namespace=%s selector=%q)", ErrNoPods, t.Namespace, t.Selector)
	}
	r := Resolved{Target: t, Explicit: false, Matches: pods}
	r.Pod = pods[0]
	return r, nil
}

// Confirm mints the token destructive operations require. It succeeds only
// when the pod was named explicitly or the match was unambiguous — pausing
// a production JVM must never hit a guessed replica.
func (r Resolved) Confirm() (Confirmed, error) {
	if !r.Explicit && len(r.Matches) > 1 {
		return Confirmed{}, fmt.Errorf("%w: %v", ErrAmbiguous, r.Matches)
	}
	return Confirmed{r: r}, nil
}
