#!/usr/bin/env python3
"""pty-drive.py — drive the Go TUI through a real pty (answering the terminal
queries Bubble Tea makes) and assert the interaction contract end to end:
menu renders, a command runs via ExecProcess with its output visible, the
post-run pause appears, and quit confirms. Exit 0 = all assertions hold.

Usage: pty-drive.py <kit-dir> <sandbox-dir>   (sandbox gets config/ + dumps/)
"""
import os, pty, sys, time, select

kit, sandbox = sys.argv[1], sys.argv[2]
os.makedirs(sandbox + "/config", exist_ok=True)
os.makedirs(sandbox + "/dumps", exist_ok=True)
with open(sandbox + "/config/target", "w") as f:
    f.write("SAVED_NAMESPACE=default\nSAVED_SELECTOR=''\nSAVED_CONTAINER=app\nSAVED_POD=pod-a\n")

env = dict(os.environ,
           PATH=kit + "/tests/mocks:" + os.environ["PATH"],
           TERM="xterm-256color", JDEBUG_MODE="1", JDEBUG_KIT=kit,
           JDEBUG_CONFIG_DIR=sandbox + "/config", JDEBUG_DUMPS=sandbox + "/dumps")

pid, fd = pty.fork()
if pid == 0:
    os.execve(kit + "/tui/jdebug-tui", ["jdebug-tui"], env)

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

drain(3)                # startup + first render
press("s", 4)           # run `jdebug status` via ExecProcess (mock kubectl)
press(" ", 1.5)         # any key back to the menu
press("q", 1)           # quit → confirm
press("y", 2)

txt = buf.decode("utf-8", "replace")
checks = {
    "menu rendered":          "guided diagnosis" in txt,
    "command output visible": "how to read this" in txt,
    "post-run pause":         "any key for the menu" in txt,
    "session log path shown": "/dumps/session-" in txt,
    "quit confirms":          "quit jdebug?" in txt,
    "transcript on exit":     "transcript of everything" in txt,
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
