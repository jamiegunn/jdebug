package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	core "github.com/jamiegunn/jdebug/core"
)

// II.4 regression: the remote-artifacts.tsv row must carry a REAL pod column
// (cleanup runs `kubectl exec <pod>` on it — an empty pod strands the staged
// jattach binary in production), and the dedup key must include ns+pod so a
// SECOND pod's staged binary is also recorded.
func TestRecordArtifactWritesPodColumnAndDedupsPerPod(t *testing.T) {
	dir := t.TempDir()
	cfg := core.Config{DumpsRoot: dir}
	cfg.Target.Namespace = "prod"
	cfg.Target.Pod = "payments-aaa"
	cfg.Target.Container = "app"

	recordArtifact(cfg, true, "/tmp/jattach", "jattach")
	recordArtifact(cfg, true, "/tmp/jattach", "jattach") // same pod → dedup
	cfg.Target.Pod = "payments-bbb"
	recordArtifact(cfg, true, "/tmp/jattach", "jattach") // second pod → NEW row

	b, err := os.ReadFile(filepath.Join(dir, "remote-artifacts.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 rows (one per pod, deduped within a pod), got %d: %q", len(lines), lines)
	}
	for _, l := range lines {
		f := strings.Split(l, "\t")
		if len(f) != 6 {
			t.Fatalf("schema is owned\\tns\\tpod\\tcontainer\\tpath\\tnote (6 cols), got %d: %q", len(f), l)
		}
		if f[2] == "" {
			t.Fatalf("pod column empty — cleanup would exec into \"\" and strand the binary: %q", l)
		}
	}
	if !strings.Contains(string(b), "payments-aaa") || !strings.Contains(string(b), "payments-bbb") {
		t.Fatalf("both pods must be recorded: %q", string(b))
	}
}
