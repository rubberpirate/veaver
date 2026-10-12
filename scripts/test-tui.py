#!/usr/bin/env python3
"""Exercise the TUI against an Observe daemon without applying a policy."""
import fcntl
import json
import os
from pathlib import Path
import pty
import select
import struct
import subprocess
import termios
import time
import urllib.request

root = Path(__file__).resolve().parents[1]
state = os.environ["LINKWEAVE_TEST_STATE_DIR"]
url = os.environ.get("LINKWEAVE_TEST_URL", "http://127.0.0.1:8787")
token = (Path(state) / "dashboard-token").read_text().strip()
opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))


def status():
    request = urllib.request.Request(url + "/api/status", headers={"Authorization": "Bearer " + token})
    with opener.open(request, timeout=5) as response:
        return json.load(response)


before = status()["config"]
assert before["mode"] == "observe", "Use a dedicated Observe daemon for this test"
master, slave = pty.openpty()
original = termios.tcgetattr(slave)
fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 32, 100, 0, 0))
process = subprocess.Popen([str(root / "build/linkweave"), "tui", "--state-dir", state, "--url", url],
                           stdin=slave, stdout=slave, stderr=slave)
output = bytearray()


def wait_for(text, timeout=8):
    output.clear()
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if select.select([master], [], [], 0.1)[0]:
            output.extend(os.read(master, 65536))
            if text.encode() in output:
                return
        if process.poll() is not None:
            raise AssertionError(f"TUI exited: {process.returncode}: {output.decode(errors='replace')}")
    raise AssertionError(f"Missing screen text: {text}: {output.decode(errors='replace')}")


def press(keys, text):
    os.write(master, keys)
    wait_for(text)


try:
    wait_for("Live NIC counters")
    press(b"2", "DIAGNOSTICS")
    press(b"3", "ACTIVITY + JOURNAL")
    press(b"4", "POLICY")
    # Enter by itself must never apply a configuration.
    press(b"\r", "POLICY")
    assert status()["config"] == before
    press(b"b", "Draft changed")
    press(b"+", "Draft changed")
    press(b"a", "REVIEW  Enter apply")
    press(b"\x1b[B", "REVIEW  Enter apply")
    press(b"\x1b", "Review canceled")
    assert status()["config"] == before, "Review/navigation changed the daemon configuration"
    fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 12, 40, 0, 0))
    press(b"1", "COMPACT")
    os.write(master, b"q")
    assert process.wait(timeout=5) == 0
    assert termios.tcgetattr(slave) == original, "Terminal settings were not restored"
    print("TUI passed: live data, views, draft/review, arrow keys, cancel, resize, quit, terminal restore; no policy applied")
finally:
    if process.poll() is None:
        process.terminate()
        process.wait(timeout=5)
    os.close(master)
    os.close(slave)
