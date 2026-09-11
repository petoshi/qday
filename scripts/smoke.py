#!/usr/bin/env python3
"""Exercise two real QDAY processes over P2P and the native wallet HTTP API.

Uses disposable genesis blocks with the exact mainnet rules; never logs tokens, passphrases or seeds.
Leaves a concise result and node logs in build/smoke-report.json / smoke-*.log.
"""
import json
import os
import pathlib
import shutil
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.request
from decimal import Decimal

ROOT = pathlib.Path(__file__).resolve().parents[1]
OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))
GO = str(ROOT / ".tools/go/bin/go") if (ROOT / ".tools/go/bin/go").exists() else (shutil.which("go") or "go")
NODE = ROOT / "build" / ("qday.exe" if os.name == "nt" else "qday")


def port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def wait(fn, description, seconds=25):
    end = time.monotonic() + seconds
    last = None
    while time.monotonic() < end:
        try:
            value = fn()
            if value:
                return value
        except (OSError, urllib.error.URLError, ValueError) as exc:
            last = str(exc)
        time.sleep(0.15)
    raise RuntimeError(f"Timed out: {description}: {last}")


class Node:
    def __init__(self, directory, name, peer=None, manifest=None, seeds=None, seed_node=False, peers=None):
        self.directory = directory
        self.http, self.p2p = port(), port()
        self.log = open(ROOT / "build" / f"smoke-{name}.log", "w")
        self.args = [str(NODE), "--network", str(manifest), "--upnp=false", "--seeds", "", "--peers", "", "--data", str(directory),
                     "--http", f"127.0.0.1:{self.http}", "--p2p", f"127.0.0.1:{self.p2p}"]
        if peer:
            self.args += ["--peers", f"127.0.0.1:{peer.p2p}"]
        if peers:
            self.args += ["--peers", ",".join(peers)]
        if seeds:
            self.args += ["--seeds", ",".join(seeds)]
        if seed_node:
            self.args += ["--seed-node"]
        self.start()

    def start(self):
        self.proc = subprocess.Popen(self.args, stdout=self.log, stderr=self.log)
        wait(lambda: (self.directory / "api.token").exists(), "node token creation")
        self.token = (self.directory / "api.token").read_text().strip()
        wait(lambda: self.api("status"), "HTTP startup")

    def api(self, path, body=None):
        data = None if body is None else json.dumps(body).encode()
        request = urllib.request.Request(f"http://127.0.0.1:{self.http}/api/{path}", data=data,
                  headers={"Authorization": "Bearer " + self.token, "Content-Type": "application/json"})
        try:
            with OPENER.open(request, timeout=15) as response:
                return json.load(response)
        except urllib.error.HTTPError as exc:
            raise ValueError(exc.read().decode()) from None

    def stop(self):
        if self.proc.poll() is None:
            self.proc.terminate()
            try:
                self.proc.wait(timeout=10)
            except subprocess.TimeoutExpired:
                self.proc.kill()
                self.proc.wait()


def fixture(directory):
    subprocess.run([GO, "run", "./node/internal/testgenesis", str(directory)], cwd=ROOT, check=True)
    return directory / "test-mainnet.json", json.loads((directory / "test-owner.json").read_text())["phrase"]


def main():
    report = {"network": "mainnet parameters / disposable genesis", "checks": []}
    nodes = []
    with tempfile.TemporaryDirectory(prefix="qday-smoke-") as temp:
        work = pathlib.Path(temp)
        try:
            manifest, phrase = fixture(work)
            a = Node(work/"a", "a", manifest=manifest); nodes.append(a)
            b = Node(work/"b", "b", a, manifest=manifest); nodes.append(b)
            a.api("create", {"password": "temporary-test-passphrase", "phrase": phrase})
            b.api("create", {"password": "temporary-test-passphrase"})
            wait(lambda: a.api("status")["synced"] and b.api("status")["synced"], "mainnet synchronization", 45)
            initial = a.api("status")
            assert initial["blockReward"] == "8" and initial["blockIntervalSeconds"] == 60
            assert initial["maturityBlocks"] == 60 and initial["initialDifficulty"] == "1048575"
            assert initial["proofFee"] == "1" and not initial["development"]
            report["checks"].append("exact mainnet rules, separate wire magic and same-genesis handshake")
            destination = b.api("status")["address"]
            a.api("send", {"address": destination, "amount": "10", "unit": initial["unit"]})
            wait(lambda: b.api("status")["pending"] == "10", "transaction propagated into peer mempool")
            report["checks"].append("funded hybrid transaction propagates before mining")
            a.api("start", {"threads": 1})
            wait(lambda: a.api("status")["height"] >= 3, "native mainnet CPU block production", 60)
            wait(lambda: b.api("status")["balance"] == "10", "recipient transfer confirmation", 60)
            a.api("stop", {})
            report["checks"].append("CPU mining and transaction confirmation on second node")
            try:
                a.api("proof/verify", {"witness": "01" + "00" * 31})
                raise AssertionError("known test witness accepted by mainnet")
            except ValueError:
                pass
            assert not a.api("status")["proofPending"] and not b.api("status")["qdayHeight"]
            report["checks"].append("known test canary solution cannot activate mainnet")
            wait(lambda: a.api("status")["height"] == b.api("status")["height"], "final sync")
            saved = b.api("status")
            b.stop(); b.start()
            assert not b.api("status")["unlocked"]
            b.api("unlock", {"password": "temporary-test-passphrase"})
            restored = b.api("status")
            assert restored["balance"] == saved["balance"]
            assert restored["genesis"] == saved["genesis"]
            report["checks"].append("restart preserves chain, balance and encrypted wallet")
            report.update(result="PASS", final_height=restored["height"])
        except Exception as exc:
            report.update(result="FAIL", error=str(exc))
            raise
        finally:
            for n in nodes:
                n.stop(); n.log.close()
            (ROOT/"build/smoke-report.json").write_text(json.dumps(report, indent=2)+"\n")
    print(json.dumps(report, indent=2))


if __name__ == "__main__":
    main()
