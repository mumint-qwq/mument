#!/usr/bin/env python3
"""Exercise a built mument binary against temporary files on loopback."""
from contextlib import contextmanager
import json
import os
from pathlib import Path
import random
import re
import signal
import socket
import sqlite3
import struct
import subprocess
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
import zlib


def png_chunk(kind, data):
    return struct.pack(">I", len(data)) + kind + data + struct.pack(">I", zlib.crc32(kind + data))


def synthetic_png():
    width, height = 512, 512
    generator = random.Random(7)
    pixels = b"".join(b"\0" + generator.randbytes(width * 3) for _ in range(height))
    header = struct.pack(">IIBBBBB", width, height, 8, 2, 0, 0, 0)
    return (b"\x89PNG\r\n\x1a\n" + png_chunk(b"IHDR", header)
            + png_chunk(b"IDAT", zlib.compress(pixels)) + png_chunk(b"IEND", b""))


def run(binary, result_dir, root, baseline=None, expected_commit=None):
    source = root / "source"
    originals = source / "A"
    originals.mkdir(parents=True)
    (source / "B").mkdir()
    (originals / "fixture.png").write_bytes(synthetic_png())

    with socket.socket() as server:
        server.bind(("127.0.0.1", 0))
        port = server.getsockname()[1]
    config = {
        "http": {"listen": "127.0.0.1", "port": port},
        "server": {
            "database": {"path": str(root / "state.sqlite")},
            "cacheDir": str(root / "cache"),
            "disableUpdateCheck": True,
            "numImageProcessors": 2,
            "previewMemoryBudgetMB": 128,
            "sources": [{"path": str(source), "name": "smoke"}],
        },
        "auth": {"methods": {"noauth": True}},
    }
    config_file = root / "config.yaml"
    config_file.write_text(json.dumps(config))
    env = {key: value for key, value in os.environ.items() if not key.startswith("FILEBROWSER_")}
    env["GOMAXPROCS"] = "2"
    base = f"http://127.0.0.1:{port}"
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

    def request(path, method="GET", data=None):
        req = urllib.request.Request(base + path, data=data, method=method,
                                     headers={"Content-Type": "application/json"})
        with opener.open(req, timeout=15) as response:
            return response.status, response.read()

    def get_preview(path):
        query = urllib.parse.urlencode({"source": "smoke", "path": path, "size": "small"})
        return request("/api/resources/preview?" + query)

    def ledger_rows():
        uri = (root / "cache" / "image-preview-ledger.sqlite").as_uri() + "?mode=ro"
        with sqlite3.connect(uri, uri=True) as database:
            return database.execute("SELECT asset,variant,digest,generated_at FROM results").fetchall()

    @contextmanager
    def server(program, phase):
        with (result_dir / f"native-{phase}-server.log").open("wb") as log:
            proc = subprocess.Popen([str(program), "--config", str(config_file), "--no-input"],
                                    cwd=root, stdin=subprocess.DEVNULL, stdout=log, stderr=log, env=env)
            try:
                until = time.monotonic() + 30
                while time.monotonic() < until:
                    if proc.poll() is not None:
                        raise RuntimeError(f"{phase} process exited before health check")
                    try:
                        if request("/health")[0] == 200:
                            break
                    except urllib.error.URLError:
                        time.sleep(0.1)
                else:
                    raise TimeoutError(f"{phase} health check timed out")
                yield
            finally:
                if proc.poll() is None:
                    proc.send_signal(signal.SIGTERM)
                    try:
                        status = proc.wait(timeout=15)
                    except subprocess.TimeoutExpired:
                        proc.kill()
                        proc.wait()
                        raise
                    if status != 0:
                        raise RuntimeError(f"{phase} server shutdown exited {status}")

    if baseline:
        with server(baseline, "ab-first"):
            status, legacy = get_preview("/A/fixture.png")
            assert status == 200 and legacy.startswith(b"\x89PNG")
            assert not (root / "cache" / "image-preview-ledger.sqlite").exists()

    with server(binary, "c-first"):
        status, html = request("/")
        assert status == 200 and b"/assets/" in html, "embedded frontend HTML missing"
        if expected_commit:
            marker = f'name="quantum-source-commit" content="{expected_commit}"'.encode()
            assert marker in html, "served source marker does not match the candidate"
        asset = re.search(rb'<script[^>]+src="(/[^"]+/assets/[^"]+\.js)"', html)
        assert asset, "compiled JS entry missing from served HTML"
        status, bundle = request(asset[1].decode())
        assert status == 200 and len(bundle) > 100, "embedded JS request failed"
        status, initial = get_preview("/A/fixture.png")
        assert status == 200 and initial.startswith(b"\x89PNG")
        if baseline:
            assert initial == legacy
        before = ledger_rows()
        assert len(before) == 1, "expected a generated PNG publication"
        move = {"action": "move", "items": [{
            "fromSource": "smoke", "fromPath": "/A/fixture.png",
            "toSource": "smoke", "toPath": "/B/fixture.png",
        }]}
        status, moved = request("/api/resources", "PATCH", json.dumps(move).encode())
        assert status == 200 and len(json.loads(moved)["succeeded"]) == 1
        assert not (originals / "fixture.png").exists()
        assert (source / "B" / "fixture.png").exists()
        status, reread = get_preview("/B/fixture.png")
        assert status == 200 and reread == initial
        assert ledger_rows() == before, "the move republished the thumbnail"
        print("Native HTTP smoke passed: embedded frontend, generated PNG and move reuse")

    if baseline:
        with server(baseline, "ab-return"):
            status, legacy_after = get_preview("/B/fixture.png")
            assert status == 200 and legacy_after == initial
            assert ledger_rows() == before, "A/B modified the ledger"
        with server(binary, "c-return"):
            status, restored = get_preview("/B/fixture.png")
            assert status == 200 and restored == initial
            assert ledger_rows() == before, "return to C republished the thumbnail"
        print("Native A/B → C → A/B → C passed using the same files, index and cache")


if __name__ == "__main__":
    import argparse

    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", type=Path)
    parser.add_argument("output", type=Path)
    parser.add_argument("--baseline", type=Path)
    parser.add_argument("--expected-commit")
    args = parser.parse_args()
    artifact = args.binary.resolve(strict=True)
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=True)
    baseline = args.baseline.resolve(strict=True) if args.baseline else None
    with tempfile.TemporaryDirectory(prefix="mument-007c-", dir=output) as temporary:
        run(artifact, output, Path(temporary), baseline, args.expected_commit)
