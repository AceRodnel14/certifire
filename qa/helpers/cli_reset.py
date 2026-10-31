#!/usr/bin/env python3
"""F6: drive `app reset-password` through a pty so prompts are real terminal prompts.
usage: cli_reset.py "<compose cmd>" MASTERKEY USERNAME NEWPASS [CONFIRM]
Prints PASS/FAIL lines; captured terminal output is checked for echoed secrets."""

import os
import pty
import select
import shlex
import sys
import time

dc, mk, user, pw = shlex.split(sys.argv[1]), sys.argv[2], sys.argv[3], sys.argv[4]
confirm = sys.argv[5] if len(sys.argv) > 5 else pw
cmd = dc + ["exec", "app", "/app", "reset-password"]
pid, fd = pty.fork()
if pid == 0:
    os.execvp(cmd[0], cmd)
answers = [mk, user, pw, confirm]
out = b""
t0 = time.time()
sent = 0
while time.time() - t0 < 90:
    r, _, _ = select.select([fd], [], [], 1)
    if r:
        try:
            d = os.read(fd, 4096)
        except OSError:
            break
        if not d:
            break
        out += d
    if sent < len(answers) and out.rstrip().endswith((b":", b">", b"?")):
        time.sleep(0.3)
        os.write(fd, answers[sent].encode() + b"\n")
        sent += 1
        out += b"\n"
_, status = os.waitpid(pid, 0)
code = os.waitstatus_to_exitcode(status)
text = out.decode("utf-8", "replace")
# the pty echoes input only when the app leaves echo on: hidden prompts must not show master key / passwords
leaks = [s for s in (mk, pw) if s and s in text]
print("exit", code)
print("PASS no secret echoed" if not leaks else "FAIL secret echoed in terminal output")
print("---- terminal output (secrets masked) ----")
for s in (mk, pw):
    text = text.replace(s, "<hidden>")
print(text)
sys.exit(code)
