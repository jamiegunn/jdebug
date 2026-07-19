package main

import (
	"strings"
	"testing"
	"time"
)

func localModel() model {
	m := readyModel()
	m.mode = 2
	m.local = probe{OK: true, When: time.Now().Add(time.Hour)} // fresh → no re-probe
	return m
}

// On a host running several JVMs, `p` must list them and pin the chosen pid —
// not silently debug whichever java is first.
func TestJVMPickerListsAndPins(t *testing.T) {
	saved := jvmsFn
	defer func() { jvmsFn = saved }()
	jvmsFn = func(kit string, tg target) ([]string, string) {
		return []string{"4200     /usr/bin/java -jar api.jar", "4310     /usr/bin/java -jar worker.jar"}, ""
	}
	out := press(t, localModel(), "p").(model)
	if out.scr != scPicker || out.pick.kind != pickJVM {
		t.Fatalf("p must open the JVM picker, got scr=%v kind=%v", out.scr, out.pick.kind)
	}
	picked := press(t, out, "2").(model)
	if picked.t.JVMPid != "4310" {
		t.Fatalf("picking the 2nd JVM must pin its pid, got %q", picked.t.JVMPid)
	}
	if picked.scr != scMenu {
		t.Fatalf("after picking a JVM the flow returns to the menu, got %v", picked.scr)
	}
}

// When the listing fails (unreachable SSH, no /proc), fall back to typing a pid
// — same "can't enumerate → type it" principle as the k8s RBAC paths.
func TestJVMPickerTypedFallbackOnError(t *testing.T) {
	saved := jvmsFn
	defer func() { jvmsFn = saved }()
	jvmsFn = func(kit string, tg target) ([]string, string) { return nil, "ssh: connect timed out" }
	out := press(t, localModel(), "p").(model)
	if out.scr != scInput || out.input.then != inputJVMPid {
		t.Fatalf("a failed JVM listing must drop to typed pid input, got scr=%v then=%v", out.scr, out.input.then)
	}
	typed := press(t, out, "9", "9", "9", "enter").(model)
	if typed.t.JVMPid != "999" {
		t.Fatalf("a typed pid must apply, got %q", typed.t.JVMPid)
	}
	// "auto" clears the pin back to auto-detect
	back := press(t, localModel(), "p")
	cleared := press(t, back, "a", "u", "t", "o", "enter").(model)
	if cleared.t.JVMPid != "" {
		t.Fatalf("'auto' must clear the pin, got %q", cleared.t.JVMPid)
	}
}

// A pinned pid must actually reach jdebug-local — in the local child env AND in
// the SSH remote-script env — or the pick does nothing.
func TestJVMPidThreadsIntoExecEnv(t *testing.T) {
	env := targetEnv(target{JVMPid: "4310", Actuator: "http://localhost:8080/actuator"})
	found := false
	for _, e := range env {
		if e == "JVM_PID=4310" {
			found = true
		}
	}
	if !found {
		t.Fatalf("JVM_PID must be exported to the local child, got %v", env)
	}
	for _, e := range targetEnv(target{}) {
		if strings.HasPrefix(e, "JVM_PID=") {
			t.Fatal("no JVM_PID must be set when the pin is empty")
		}
	}
	w := localWords("/k", target{SSH: "ops@vm1", JVMPid: "4310", Actuator: "http://localhost:8080/actuator"}, "threads")
	if !strings.Contains(w[2], "JVM_PID=") || !strings.Contains(w[2], "4310") {
		t.Fatalf("JVM_PID must reach the remote host, got:\n%s", w[2])
	}
}

func TestHostChangeClearsJVMPin(t *testing.T) {
	t.Setenv("JDEBUG_CONFIG_DIR", t.TempDir())
	saved := localHealthFn
	defer func() { localHealthFn = saved }()
	localHealthFn = func(string, target) bool { return true }
	m := readyModel()
	m.mode = 2
	m.t.JVMPid = "4310"
	out, _ := m.applySSHHost("ops@other-host")
	if out.(model).t.JVMPid != "" {
		t.Fatal("changing host must clear the JVM pin — a pid means nothing on a different host")
	}
}

// Staging jattach on bare metal (local or SSH) must go through the vendored,
// checksum-verified stager — never a runtime download.
func TestJattachStagingUsesVerifiedStager(t *testing.T) {
	m := readyModel()
	m.mode = 2
	m.kit = "/opt/kit"

	_, w := m.stageJattachWords()
	joined := strings.Join(w, " ")
	if !strings.Contains(joined, "capture/stage-jattach.sh") || !strings.Contains(joined, " local") {
		t.Fatalf("local staging must route to the verified stager, got %v", w)
	}
	for _, bad := range []string{"curl", "wget", "github.com", "download"} {
		if strings.Contains(joined, bad) {
			t.Fatalf("staging must not download at runtime (found %q): %v", bad, w)
		}
	}

	m.t.SSH = "ops@vm1:2222"
	_, w = m.stageJattachWords()
	joined = strings.Join(w, " ")
	if !strings.Contains(joined, "capture/stage-jattach.sh") || !strings.Contains(joined, "ssh") ||
		!strings.Contains(joined, "ops@vm1:2222") {
		t.Fatalf("ssh staging must route to the stager with the host, got %v", w)
	}
	for _, bad := range []string{"curl", "wget", "github.com"} {
		if strings.Contains(joined, bad) {
			t.Fatalf("ssh staging must not download (found %q): %v", bad, w)
		}
	}
}

func TestBareMetalHeaderShowsJVMPin(t *testing.T) {
	m := readyModel()
	m.mode = 2
	if h := ansiStrip(m.headerLocal(true)); !strings.Contains(h, "jvm auto") {
		t.Fatalf("an unpinned bare-metal header must read 'jvm auto':\n%s", h)
	}
	m.t.JVMPid = "4310"
	if h := ansiStrip(m.headerLocal(true)); !strings.Contains(h, "jvm 4310") {
		t.Fatalf("a pinned header must show the pid:\n%s", h)
	}
}

// localWords is the seam that makes bare metal work on this host OR over SSH.
func TestLocalWordsThisHost(t *testing.T) {
	w := localWords("/opt/kit", target{}, "threads", "--confirm")
	if len(w) != 4 || w[0] != "sh" || !strings.HasSuffix(w[1], "/jdebug-local") ||
		w[2] != "threads" || w[3] != "--confirm" {
		t.Fatalf("this-host words must exec the script directly, got %v", w)
	}
}

func TestLocalWordsOverSSH(t *testing.T) {
	w := localWords("/opt/kit", target{SSH: "ops@vm1", Actuator: "http://localhost:8080/actuator"}, "heap", "--confirm")
	if len(w) != 3 || w[0] != "sh" || w[1] != "-c" {
		t.Fatalf("ssh words must be a single `sh -c`, got %v", w)
	}
	cmd := w[2]
	for _, want := range []string{
		"ssh -o BatchMode=yes", "-o ConnectTimeout=8", "'ops@vm1'", "sh -s --",
		"'heap'", "'--confirm'", "jdebug-local", "JDEBUG_SSH_BACK", "ACTUATOR_BASE",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("ssh command missing %q:\n%s", want, cmd)
		}
	}
	// keys/agent only — never a password path
	for _, forbidden := range []string{"sshpass", "PasswordAuthentication=yes", "-o PubkeyAuthentication=no"} {
		if strings.Contains(cmd, forbidden) {
			t.Errorf("ssh must stay key/agent-only, found %q", forbidden)
		}
	}
	// the script is piped to the remote's stdin (`sh -s`), so nothing is installed there
	if !strings.Contains(cmd, "cat '/opt/kit/jdebug-local'") {
		t.Errorf("the script must be piped to the remote, got:\n%s", cmd)
	}
}

func TestSSHBaseParsesPort(t *testing.T) {
	if got := sshBase("ops@vm1:2222"); !strings.Contains(got, "-p '2222'") || !strings.Contains(got, "'ops@vm1'") {
		t.Fatalf("a trailing :port must become -p, got %q", got)
	}
	if got := sshBase("ops@vm1"); strings.Contains(got, "-p ") {
		t.Fatalf("no port means no -p flag, got %q", got)
	}
	if got := sshBase("vm1"); !strings.Contains(got, "'vm1'") || strings.Contains(got, "-p") {
		t.Fatalf("a bare host must pass through unchanged, got %q", got)
	}
}

// choosing Bare metal must ask where the JVM is before landing on the menu.
func TestChooserBareMetalPromptsForHost(t *testing.T) {
	m := demoModel()
	m.mode = 0
	m.scr = scChooser
	out := press(t, m, "2").(model)
	if out.mode != 2 || out.scr != scInput || out.input.then != inputSSHHost {
		t.Fatalf("bare metal must prompt for the host, got mode=%d scr=%v then=%v",
			out.mode, out.scr, out.input.then)
	}
	// the In-pod option is gone: 3 no longer picks a mode
	if still := press(t, m, "3").(model); still.scr != scChooser {
		t.Fatal("there is no third mode any more; 3 must not pick one")
	}
}

func TestApplySSHHostSetsAndClears(t *testing.T) {
	t.Setenv("JDEBUG_CONFIG_DIR", t.TempDir())
	saved := localHealthFn
	defer func() { localHealthFn = saved }()
	localHealthFn = func(string, target) bool { return true } // never shell out

	m := demoModel()
	m.mode = 2
	out, _ := m.applySSHHost("ops@vm1")
	mm := out.(model)
	if mm.t.SSH != "ops@vm1" || mm.mode != 2 || mm.scr != scMenu {
		t.Fatalf("a host must switch to SSH bare metal on the menu, got ssh=%q scr=%v", mm.t.SSH, mm.scr)
	}
	if loadTarget().SSH != "ops@vm1" {
		t.Fatal("the SSH host must persist across sessions")
	}
	for _, blank := range []string{"", "local", "-", "this machine"} {
		o, _ := mm.applySSHHost(blank)
		if got := o.(model).t.SSH; got != "" {
			t.Fatalf("%q must mean this machine (clear SSH), got %q", blank, got)
		}
	}
}

func TestModeLabels(t *testing.T) {
	m := demoModel()
	m.mode = 1
	if got := m.modeLabel(); !strings.Contains(got, "kubernetes") {
		t.Errorf("mode 1 label: %q", got)
	}
	m.mode = 2
	if got := m.modeLabel(); !strings.Contains(got, "this host") {
		t.Errorf("local bare metal label: %q", got)
	}
	m.t.SSH = "ops@vm1"
	if got := m.modeLabel(); !strings.Contains(got, "ssh ops@vm1") {
		t.Errorf("ssh bare metal label: %q", got)
	}
}

// the saved-target round trip must carry the SSH host.
func TestTargetSSHRoundTrip(t *testing.T) {
	t.Setenv("JDEBUG_CONFIG_DIR", t.TempDir())
	saveTarget(target{Namespace: "n", Container: "app", SSH: "deploy@10.0.0.5:2200"})
	if got := loadTarget().SSH; got != "deploy@10.0.0.5:2200" {
		t.Fatalf("SSH host must survive save/load, got %q", got)
	}
}
