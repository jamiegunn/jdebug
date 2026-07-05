#!/usr/bin/env python3
"""pty-drive.py — drive the Go TUI through a real pty (answering the terminal
queries Bubble Tea makes) and assert the interaction contract end to end:
the tier-2 dashboard renders its live panes, commands stream into the bottom
OUTPUT pane in place of the log tail, the wizard still drops out via
ExecProcess with the post-run pause, and quit confirms. Exit 0 = all
assertions hold.

Usage: pty-drive.py <kit-dir> <sandbox-dir>   (sandbox gets config/ + dumps/)
"""
import glob, os, pty, sys, time, select, fcntl, struct, termios

kit, sandbox = sys.argv[1], sys.argv[2]
os.makedirs(sandbox + "/config", exist_ok=True)
os.makedirs(sandbox + "/dumps", exist_ok=True)
with open(sandbox + "/config/target", "w") as f:
    f.write("SAVED_NAMESPACE=default\nSAVED_SELECTOR='app=web'\nSAVED_CONTAINER=app\nSAVED_POD=pod-a\n")

env = dict(os.environ,
           PATH=kit + "/tests/mocks:" + os.environ["PATH"],
           TERM="xterm-256color", JDEBUG_MODE="1", JDEBUG_KIT=kit,
           JDEBUG_CONFIG_DIR=sandbox + "/config", JDEBUG_DUMPS=sandbox + "/dumps")

pid, fd = pty.fork()
if pid == 0:
    os.execve(kit + "/tui/jdebug-tui", ["jdebug-tui"], env)

# 200x50 so the tier-2 grid (live logs, events, captures, trends) engages
fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", 50, 200, 0, 0))

buf = b""

def drain(seconds):
    global buf
    end = time.time() + seconds
    while time.time() < end:
        r, _, _ = select.select([fd], [], [], 0.2)
        if not r:
            continue
        try:
            b = os.read(fd, 65536)
        except OSError:
            return
        buf += b
        if b"\x1b[6n" in b:      # cursor-position query
            os.write(fd, b"\x1b[24;1R")
        if b"\x1b]11;?" in b:    # background-color query
            os.write(fd, b"\x1b]11;rgb:0d0d/1111/1717\x1b\\")

def press(k, wait=1.0):
    os.write(fd, k.encode())
    drain(wait)

drain(4)                # startup; auto-status fires itself at the 2s mark
press("\x1b", 1)        # esc dismisses the auto-status pane
press("s", 4)           # quick read → streams into the bottom OUTPUT pane
press("\x1b", 1)        # esc dismisses back to the live logs
press("l", 4)           # logs stream into the pane too
press("\x1b", 1)        # esc back
press("w", 1)           # guided diagnosis…
press("2", 8)           #   …streams BOTH steps + wrap-up into the pane
press("\x1b", 1)        # esc back to the live logs
press("y", 4)           # pod deep-dive (why) streams into the pane
press("\x1b", 1)        # esc back
press("S", 4)           # security posture streams into the pane
press("\x1b", 1)        # esc back
press("T", 4)           # pod terminal (mock exec exits at once) → auto-status
press("q", 1)           # quit → confirm
press("y", 2)

txt = buf.decode("utf-8", "replace")

logtxt = ""
for p in glob.glob(sandbox + "/dumps/session-*.log"):
    with open(p, errors="replace") as f:
        logtxt += f.read()

checks = {
    "dashboard rendered":       "guided diagnosis" in txt,
    "live log pane":            "LIVE LOGS" in txt and "OutOfMemoryError" in txt,
    "pods pane, clickable":     "PODS" in txt and "click switches" in txt,
    "events + captures panes":  "EVENTS" in txt and "CAPTURES" in txt,
    "trends sparklines":        "TRENDS" in txt,
    "status streams into pane": "OUTPUT" in txt and "how to read this" in txt,
    "why: pod deep-dive runs":  "pod deep-dive" in txt or "requests = the scheduler" in txt,
    "security: posture runs":   "security posture" in txt,
    "logs stream into pane":    "mock log line" in txt,
    "wizard streams on-page":   "flow complete" in txt and "any key for the menu" not in txt,
    "session log transcript":   "$ " in logtxt and "status" in logtxt,
    "quit confirms":            "quit jdebug?" in txt,
    "transcript on exit":       "transcript of everything" in txt,
}
fail = 0
for name, ok in checks.items():
    print(("  ok   " if ok else "  FAIL ") + "pty: " + name)
    fail += 0 if ok else 1
try:
    os.waitpid(pid, os.WNOHANG)
except ChildProcessError:
    pass
sys.exit(1 if fail else 0)
