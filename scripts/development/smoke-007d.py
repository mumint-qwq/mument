#!/usr/bin/env python3
"""Run standalone binary or Docker HTTP acceptance in a synthetic scratch tree."""

import argparse
from contextlib import contextmanager
import hashlib
import http.client
import json
import os
from pathlib import Path
import random
import re
import secrets
import signal
import socket
import sqlite3
import struct
import subprocess
import tempfile
import time
from urllib.parse import urlencode
import zlib


def png_chunk(kind, data):
    return struct.pack(">I", len(data)) + kind + data + struct.pack(">I", zlib.crc32(kind + data))


def png(width=1100, height=760, seed=7):
    rng = random.Random(seed)
    pixels = b"".join(b"\0" + rng.randbytes(width * 3) for _ in range(height))
    header = struct.pack(">IIBBBBB", width, height, 8, 2, 0, 0, 0)
    return b"\x89PNG\r\n\x1a\n" + png_chunk(b"IHDR", header) + png_chunk(b"IDAT", zlib.compress(pixels)) + png_chunk(b"IEND", b"")


class UnixHTTP(http.client.HTTPConnection):
    def __init__(self, path):
        super().__init__("localhost", timeout=12)
        self.path = path

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX)
        self.sock.settimeout(self.timeout)
        self.sock.connect(str(self.path))


def request(root, path, method="GET", body=None):
    conn = UnixHTTP(root / "http.sock")
    try:
        conn.request(method, path, None if body is None else json.dumps(body),
                     {"Content-Type": "application/json"})
        response = conn.getresponse()
        return response.status, response.read()
    finally:
        conn.close()


def ok(root, path, method="GET", body=None):
    code, data = request(root, path, method, body)
    assert code == 200, (code, path, data[:300])
    return data


def until(action, seconds=30, interval=0.1):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        try:
            if result := action():
                return result
        except (OSError, sqlite3.OperationalError, http.client.HTTPException):
            pass
        time.sleep(interval)
    raise TimeoutError("condition did not become true; inspect server log and final status")


def control(root, action=None, scope=None):
    path = "/api/settings/preview-warmup"
    if action is None:
        return json.loads(ok(root, path))
    command = {"action": action}
    if scope is not None:
        command["scope"] = scope
    return json.loads(ok(root, path, "POST", command))


def ledger(root):
    uri = (root / "cache/image-preview-ledger.sqlite").as_uri() + "?mode=ro"
    with sqlite3.connect(uri, uri=True) as db:
        return db.execute("SELECT variant,digest,generated_at,last_used_at FROM results ORDER BY digest").fetchall()


def configure(root, inside, enabled):
    (root / "config.yaml").write_text(json.dumps({
        "http": {"socket": str(inside / "http.sock")},
        "server": {
            "database": {"path": str(inside / "state.sqlite")},
            "cacheDir": str(inside / "cache"),
            "disableUpdateCheck": True,
            "numImageProcessors": 2,
            "previewMemoryBudgetMB": 128,
            "sources": [
                {"path": str(inside / "source"), "name": "library"},
                {"path": str(inside / "other"), "name": "outside"},
            ],
            "previewWarmup": {
                "enabled": enabled,
                "scopes": [{"source": "library", "directory": "Pictures"}],
            },
        },
        "auth": {"methods": {"noauth": True}, "adminPassword": secrets.token_urlsafe(24) + "aA1!"},
    }))


CGROUP_FILES = (
    "cpu.stat", "cpu.max", "io.stat", "memory.current", "memory.peak",
    "memory.max", "memory.swap.max", "memory.events",
)


@contextmanager
def server(args, root, output, phase, enabled, crash=False):
    inside = Path("/run/mument-test") if args.image else root
    configure(root, inside, enabled)
    env = {key: value for key, value in os.environ.items() if not key.startswith("FILEBROWSER_")}
    env["GOMAXPROCS"] = "2"
    name = f"mument-007d-{root.name}-{phase}"
    flags = ["--config", str(inside / "config.yaml"), "--no-input"]
    if args.image:
        command = [
            "docker", "run", "--pull", "never", "--name", name, "--network", "none",
            "--cpus", "2", "--memory", "1g", "--memory-swap", "2g", "--cgroupns", "private",
            "--user", f"{os.getuid()}:{os.getgid()}", "--env", "GOMAXPROCS=2",
            "--mount", f"type=bind,src={root},dst={inside}", args.image, *flags,
        ]
    else:
        command = [str(args.binary), *flags]
    with (output / f"{phase}-server.log").open("wb") as log:
        proc = subprocess.Popen(command, cwd=root, stdin=subprocess.DEVNULL, stdout=log, stderr=log, env=env)
        completed = False
        try:
            def healthy():
                if proc.poll() is not None:
                    raise RuntimeError(f"{phase} exited early with code {proc.returncode}")
                return request(root, "/health")[0] == 200

            until(healthy)
            if phase == "default" and args.expected_commit:
                check_candidate(args, root, name, output)
            yield name if args.image else proc.pid
            completed = True
        finally:
            force = crash and completed
            if args.image:
                stop = ["kill", "--signal", "KILL"] if force else ["stop", "-t", "15"]
                subprocess.run(["docker", *stop, name], capture_output=True, check=False)
                inspected = subprocess.run(
                    ["docker", "inspect", "--format", "{{json .State}}", name],
                    capture_output=True, text=True, check=False,
                )
                if inspected.returncode == 0:
                    state = json.loads(inspected.stdout)
                    (output / f"{phase}-container-state.json").write_text(json.dumps(state, indent=2))
                subprocess.run(["docker", "rm", name], capture_output=True, check=False)
            elif proc.poll() is None:
                proc.send_signal(signal.SIGKILL if force else signal.SIGTERM)
            try:
                proc.wait(timeout=20)
            except subprocess.TimeoutExpired:
                proc.kill()
                proc.wait()
                raise
            expected = (137 if args.image else -signal.SIGKILL) if force else 0
            if completed and proc.returncode != expected:
                raise RuntimeError(f"{phase} exited with code {proc.returncode}; expected {expected}")
            if args.image and completed:
                assert inspected.returncode == 0, inspected.stderr
                assert not state["OOMKilled"] and state["ExitCode"] == expected, state


def check_candidate(args, root, name, output):
    if args.image:
        command = ["docker", "exec", name, "filebrowser", "version"]
    else:
        command = [str(args.binary), "version"]
    info = subprocess.check_output(command, cwd=root, stderr=subprocess.STDOUT)
    (output / "candidate-version.log").write_bytes(info)
    match = re.search(rb"(?m)^\s*Commit\s*:\s*([0-9a-f]{40})\s*$", info)
    assert match and match[1].decode() == args.expected_commit, "binary commit does not match the candidate"
    html = ok(root, "/")
    marker = b'<meta name="quantum-source-commit" content="' + args.expected_commit.encode() + b'">'
    assert marker in html, "served frontend commit does not match the candidate"
    (output / "served-build-marker").write_bytes(marker + b"\n")


def check_cgroup(sample):
    counters = {}
    for line in sample.decode().splitlines():
        if line.startswith("/sys/fs/cgroup/"):
            section = line.removeprefix("/sys/fs/cgroup/")
            counters[section] = []
        else:
            counters[section].append(line)
    quota, period = map(int, counters["cpu.max"][0].split())
    assert period > 0 and quota == 2 * period, counters["cpu.max"]
    assert counters["memory.max"] == ["1073741824"], counters["memory.max"]
    assert counters["memory.swap.max"] == ["1073741824"], counters["memory.swap.max"]
    events = dict(line.split() for line in counters["memory.events"])
    assert events["oom"] == "0" and events["oom_kill"] == "0", events


def sample_runtime(args, handle, output, phase):
    if args.image:
        files = [f"/sys/fs/cgroup/{name}" for name in CGROUP_FILES]
        command = ["docker", "exec", handle, "sh", "-c",
                   'for f do printf "%s\\n" "$f"; if test -r "$f"; then cat "$f"; fi; done',
                   "sample", *files]
        sample = subprocess.run(command, capture_output=True, check=True).stdout
        (output / f"{phase}-cgroup.txt").write_bytes(sample)
        check_cgroup(sample)
    else:
        for name in ("io", "status", "stat"):
            (output / f"{phase}-proc-{name}.txt").write_bytes(Path(f"/proc/{handle}/{name}").read_bytes())


def fixtures(root):
    for rel in ("source/Pictures", "source/Pictures-old", "other", "source/Foreground"):
        (root / rel).mkdir(parents=True)
    data = png()
    for rel in ("source/Pictures/first.png", "source/Pictures-old/untouched.png", "other/untouched.png"):
        file = root / rel
        file.write_bytes(data)
        os.utime(file, (time.time() - 30,) * 2)
    (root / "source/Pictures/tiny.png").write_bytes(png(64, 64))
    (root / "source/Pictures/outside.png").symlink_to("../Pictures-old/untouched.png")


def preview(root, path, size="xlarge"):
    query = urlencode({"source": "library", "path": path, "size": size})
    return ok(root, "/api/resources/preview?" + query)


def run_smoke(args, output, root):
    fixtures(root)
    with server(args, root, output, "default", False):
        assert control(root)["reason"] == "disabled"
        time.sleep(2)
        assert ledger(root) == []
    with server(args, root, output, "enabled", True):
        until(lambda: len(ledger(root)) == 1, seconds=45)
        before = ledger(root)
        assert "/1024x1024/" in before[0][0] and before[0][-1] == 0
        assert control(root)["fullDecodes"] == 1
        data = preview(root, "/Pictures/first.png")
        assert hashlib.sha256(data).hexdigest() == before[0][1]
        after = ledger(root)
        assert after[0][:-1] == before[0][:-1] and after[0][-1] >= after[0][-2]
        assert control(root)["fullDecodes"] == 1
        assert control(root, "pause")["paused"]
        later = root / "source/Pictures/later.png"
        later.write_bytes(png(seed=11))
        os.utime(later, (time.time() - 30,) * 2)
        assert control(root, "fill", 1)["manualScope"] == 1
        assert "manualScope" not in control(root, "cancel")
        assert len(ledger(root)) == 1
    # A real restart resumes with durable version markers and the same ledger.
    with server(args, root, output, "restart", True) as handle:
        ok(root, "/api/resources?" + urlencode({"source": "library", "path": "/Pictures/"}))
        until(lambda: len(ledger(root)) == 2, seconds=45)
        state = control(root)
        assert state["fullDecodes"] == 1
        assert not state["paused"]
        assert any(row[1] == before[0][1] and row[2] == before[0][2] for row in ledger(root))
        (output / "smoke-status.json").write_text(json.dumps(state, indent=2))
        sample_runtime(args, handle, output, "restart")


def run_interruption(args, output, root):
    fixtures(root)
    data = png(seed=87)
    for index in range(8):
        photo = root / f"source/Pictures/interrupt-{index:02d}.png"
        photo.write_bytes(data)
        os.utime(photo, (time.time() - 30,) * 2)
    with server(args, root, output, "killed-during-work", True, crash=True):
        active = until(lambda: (value if (value := control(root))["active"] else None),
                       seconds=45, interval=0.01)
        (output / "interruption-status.json").write_text(json.dumps(active, indent=2))
    published = ledger(root)
    with server(args, root, output, "recover-after-kill", True):
        until(lambda: len(ledger(root)) == 9, seconds=45)
        recovered = ledger(root)
        assert all(row in recovered for row in published)
        assert all(row[-1] == 0 for row in recovered)
        (output / "recovery-status.json").write_text(json.dumps(control(root), indent=2))


def run_comparison(args, output, root, enabled):
    fixtures(root)
    data = png(seed=31)
    for index in range(24):
        for directory in ("Pictures", "Foreground"):
            photo = root / f"source/{directory}/load-{index:02d}.png"
            photo.write_bytes(data)
            os.utime(photo, (time.time() - 30,) * 2)
    phase = "load-on" if enabled else "load-off"
    with server(args, root, output, phase, enabled) as handle:
        directory_query = "/api/resources/list?" + urlencode({
            "source": "library", "path": "/Foreground/", "limit": 20,
        })
        until(lambda: request(root, directory_query)[0] == 200)
        sample_runtime(args, handle, output, phase + "-start")
        before_status = control(root)
        latency = []
        listings = []
        start = time.monotonic()
        for index in range(24):
            if index:
                time.sleep(max(0, start + index * 1.6 - time.monotonic()))
            before = time.monotonic()
            listing = ok(root, directory_query)
            listings.append(round((time.monotonic() - before) * 1000, 3))
            assert len(json.loads(listing)["items"]) == 20
            before = time.monotonic()
            preview(root, f"/Foreground/load-{index:02d}.png")
            latency.append(round((time.monotonic() - before) * 1000, 3))
        sample_runtime(args, handle, output, phase + "-end")
        status = control(root)
        seconds = time.monotonic() - start
        generated = status["generated"] - before_status["generated"]
        (output / f"{phase}.json").write_text(json.dumps({
            "elapsedSeconds": seconds, "foregroundMs": latency, "listingMs": listings,
            "backgroundGeneratedDuringLoad": generated,
            "backgroundPerSecond": generated / seconds,
            "initialBackground": 25, "remainingAtLeast": max(0, 25 - status["generated"]),
            "beforeStatus": before_status, "status": status,
        }, indent=2))
        if enabled:
            assert generated > 0, "enabled workload made no background progress"
        else:
            assert status["generated"] == 0, "disabled workload published background previews"


def main():
    parser = argparse.ArgumentParser()
    mode = parser.add_mutually_exclusive_group(required=True)
    mode.add_argument("--binary", type=lambda value: Path(value).resolve())
    mode.add_argument("--image", help="prebuilt local mument candidate tag or image ID")
    parser.add_argument("--output", required=True, type=lambda value: Path(value).resolve())
    parser.add_argument("--expected-commit", help="full SHA embedded in the binary and served HTML")
    parser.add_argument("--interrupt", action="store_true", help="kill a scratch process while the worker is active")
    parser.add_argument("--compare", action="store_true", help="collect the fixed on/off workload and runtime counters")
    args = parser.parse_args()
    if (args.image or args.expected_commit) and not re.fullmatch(r"[0-9a-f]{40}", args.expected_commit or ""):
        parser.error("provide --expected-commit with a full lowercase source SHA")
    args.output.mkdir(parents=True, exist_ok=True)
    if args.image:
        identity = subprocess.check_output(["docker", "image", "inspect", "--format", "{{.Id}}", args.image], text=True).strip()
        args.image = identity
    else:
        with args.binary.open("rb") as stream:
            identity = hashlib.file_digest(stream, "sha256").hexdigest()
    (args.output / "candidate-identity").write_text(identity + "\n")
    with tempfile.TemporaryDirectory(prefix="007d-") as scratch:
        run_smoke(args, args.output, Path(scratch))
    if args.interrupt:
        with tempfile.TemporaryDirectory(prefix="007d-") as scratch:
            run_interruption(args, args.output, Path(scratch))
    if args.compare:
        for enabled in (False, True):
            with tempfile.TemporaryDirectory(prefix="007d-") as scratch:
                run_comparison(args, args.output, Path(scratch), enabled)


if __name__ == "__main__":
    main()
