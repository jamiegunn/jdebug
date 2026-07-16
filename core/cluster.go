// Package core is the v2 engine of jdebug: target resolution, the capture
// pipeline, and the evidence store. It exists to make the adversarial
// review's findings unrepresentable rather than merely fixed:
//
//   - a capture cannot claim success before its validator passes (F1/F5),
//   - a destructive operation cannot run against an ambiguous target (F8),
//   - provenance lives in a manifest, not in filename conventions,
//   - the cluster is reached through one interface, so the transport is
//     swappable (kubectl today; client-go per-capability if ever needed).
//
// The package is stdlib-only on purpose: it compiles anywhere Go compiles,
// with no module downloads, and its tests run against a fake Cluster.
package core

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// Cluster is the one boundary to Kubernetes. jdebug's superpower is
// inheriting the operator's ambient kubectl — contexts, exec credential
// plugins, OIDC, RBAC — so the default implementation shells out to kubectl
// and NEVER touches kubeconfig. Every method maps 1:1 onto a kubectl
// invocation an operator could copy-paste.
type Cluster interface {
	// ExecPod runs argv inside pod/container, streaming stdout to w.
	// stderr is returned inside err (first line) when the command fails.
	ExecPod(ctx context.Context, ns, pod, container string, w io.Writer, argv ...string) error
	// PodsMatching lists pod names for a selector ("" = all pods in ns).
	PodsMatching(ctx context.Context, ns, selector string) ([]string, error)
	// CopyFromPod copies a file out of the pod (kubectl cp semantics —
	// which is exactly why the pipeline re-validates sizes afterwards).
	CopyFromPod(ctx context.Context, ns, pod, container, remotePath, localPath string) error
}

// Kubectl is the production Cluster: thin, transparent shell-outs.
type Kubectl struct {
	Bin string // "" → "kubectl"
}

func (k Kubectl) bin() string {
	if k.Bin == "" {
		return "kubectl"
	}
	return k.Bin
}

func (k Kubectl) run(ctx context.Context, w io.Writer, args ...string) error {
	cmd := exec.CommandContext(ctx, k.bin(), args...)
	var errb bytes.Buffer
	cmd.Stdout = w
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		line := strings.TrimSpace(errb.String())
		if i := strings.IndexByte(line, '\n'); i >= 0 {
			line = line[:i]
		}
		if line == "" {
			line = err.Error()
		}
		return fmt.Errorf("kubectl %s: %s", args[0], line)
	}
	return nil
}

func (k Kubectl) ExecPod(ctx context.Context, ns, pod, container string, w io.Writer, argv ...string) error {
	args := append([]string{"-n", ns, "exec", pod, "-c", container, "--"}, argv...)
	return k.run(ctx, w, args...)
}

func (k Kubectl) PodsMatching(ctx context.Context, ns, selector string) ([]string, error) {
	args := []string{"-n", ns, "get", "pods",
		"-o", `jsonpath={range .items[*]}{.metadata.name}{"\n"}{end}`}
	if selector != "" {
		args = append(args, "-l", selector)
	}
	var out bytes.Buffer
	if err := k.run(ctx, &out, args...); err != nil {
		return nil, err
	}
	var pods []string
	for _, l := range strings.Split(out.String(), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			pods = append(pods, l)
		}
	}
	return pods, nil
}

func (k Kubectl) CopyFromPod(ctx context.Context, ns, pod, container, remotePath, localPath string) error {
	return k.run(ctx, io.Discard, "-n", ns, "cp", pod+":"+remotePath, localPath, "-c", container)
}
