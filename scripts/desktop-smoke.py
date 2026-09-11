#!/usr/bin/env python3
"""Exercise complete Windows (Wine64) and Linux archives with private test wallets."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
import zipfile

from smoke import Node

ROOT = Path(__file__).resolve().parents[1]
HTTP = urllib.request.build_opener(urllib.request.ProxyHandler({}))
PASSWORD = "temporary-desktop-test-passphrase"


def wait_for(check, label, seconds=40):
    deadline = time.monotonic() + seconds
    last = None
    while time.monotonic() < deadline:
        try:
            result = check()
            if result:
                return result
        except (OSError, ValueError) as error:
            last = error
        time.sleep(.15)
    raise RuntimeError("Timed out: " + label + (" (" + str(last) + ")" if last else ""))


def request(endpoint, token, route, body=None):
    req = urllib.request.Request(endpoint["url"] + "/api/" + route,
        data=None if body is None else json.dumps(body).encode(),
        headers={"Content-Type": "application/json", "Authorization": "Bearer " + token})
    with HTTP.open(req, timeout=15) as response:
        return json.load(response)


def exercise(package, work):
    target = package["target"]
    print(target + ": extract archive and start launcher", flush=True)
    archive = ROOT / package["archive"]
    assert hashlib.sha256(archive.read_bytes()).hexdigest() == package["sha256"], "archive checksum mismatch"
    extracted = work / (target + " extracted wallet")
    extracted.mkdir()
    if target.startswith("windows"):
        with zipfile.ZipFile(archive) as z:
            z.extractall(extracted)
    else:
        with tarfile.open(archive) as tf:
            tf.extractall(extracted, filter="data")
    distribution = next(extracted.iterdir())
    data = work / (target + " wallet data")
    embedded_manifest = distribution / "resources/qday-mainnet.json"
    assert hashlib.sha256(embedded_manifest.read_bytes()).hexdigest() == package["manifestSHA256"]
    embedded = json.loads(embedded_manifest.read_text())
    assert embedded["genesis"]["timestamp"] == package["genesisTimestamp"]
    assert embedded.get("genesisMessage", "") == package["genesisMessage"]
    fixture = work / (target + " disposable genesis")
    fixture.mkdir()
    subprocess.run([str(ROOT / ".tools/go/bin/go"), "run", "./node/internal/testgenesis", str(fixture)], cwd=ROOT, check=True)
    manifest = fixture / "test-mainnet.json"
    fixture_manifest = json.loads(manifest.read_text())
    assert fixture_manifest.get("genesisMessage") == package["genesisMessage"]
    config = json.loads((distribution / "resources/distribution.json").read_text())
    assert config["network"] == "mainnet"
    assert config["seeds"] == (ROOT / "node/internal/localapp/seeds.txt").read_text().split()
    seed = Node(work / (target + " seed"), "desktop-"+target+"-seed", manifest=manifest, seed_node=True)
    env = os.environ.copy()
    if target.startswith("windows"):
        # Keep Wine's z: -> / drive mapping outside the repository. File
        # indexers must never encounter that recursive system-wide symlink.
        env.update(WINEPREFIX=str(work / (target + " wine prefix")), WINEARCH="win64", WINEDEBUG="-all")
        executable = distribution / "QDAY-Wallet.exe"
        cmd = ["/usr/lib/wine/wine64", str(executable), "--data", "Z:" + str(data).replace("/", "\\"), "--no-browser"]
        node_executable = distribution / "resources/qday-node.exe"
        embedded_arg = "Z:" + str(embedded_manifest).replace("/", "\\")
        validation_cmd = ["/usr/lib/wine/wine64", str(node_executable), "--network", embedded_arg, "--validate"]
    else:
        executable = distribution / "QDAY-Wallet"
        cmd = [str(executable), "--data", str(data), "--no-browser"]
        node_executable = distribution / "resources/qday-node"
        validation_cmd = [str(node_executable), "--network", str(embedded_manifest), "--validate"]
    validated = subprocess.run(validation_cmd, env=env, text=True, capture_output=True, timeout=30, check=True)
    assert package["genesis"] in validated.stdout
    manifest_arg = "Z:" + str(manifest).replace("/", "\\") if target.startswith("windows") else str(manifest)
    cmd += ["--network", manifest_arg, "--seeds", f"127.0.0.1:{seed.p2p}", "--upnp=false"]
    log = open(ROOT / "build" / ("desktop-" + target + ".log"), "w")
    process = None
    endpoint = None
    token = ""
    words = ""
    try:
        process = subprocess.Popen(cmd, env=env, stdout=log, stderr=log)
        def ready():
            if process.poll() is not None:
                raise RuntimeError("Launcher exited; see build/desktop-" + target + ".log")
            e = json.loads((data / "node.json").read_text())
            credential = (data / "api.token").read_text().strip()
            status = request(e, credential, "status")
            assert not status["development"] and not status["hasWallet"]
            return e, credential
        endpoint, token = wait_for(ready, target + " startup", 60)
        for resource in ("assets/hero.png", "assets/logo.png", "assets/fonts/Manrope.ttf", "app.js", "style.css"):
            with HTTP.open(endpoint["url"] + "/" + resource, timeout=15) as response:
                assert response.read() == (ROOT / "node/qday/web" / resource).read_bytes(), "stale or missing embedded resource: " + resource
        subprocess.run(cmd, env=env, stdout=log, stderr=log, timeout=15, check=True)
        assert json.loads((data / "node.json").read_text())["pid"] == endpoint["pid"], "duplicate node"
        launch = request(endpoint, token, "launch", {})["url"]
        assert token not in launch
        code = urllib.parse.parse_qs(urllib.parse.urlsplit(launch).fragment)["launch"][0]
        session = request(endpoint, "", "session", {"code": code})["token"]
        assert session != token
        try:
            request(endpoint, "", "session", {"code": code})
            raise AssertionError("Launch code replay accepted")
        except urllib.error.HTTPError as error:
            assert error.code == 401
        print(target + ": create encrypted wallet, mine and sign a transfer", flush=True)
        created = request(endpoint, session, "create", {"password": PASSWORD, "phrase": ""})
        words = created["phrase"]
        assert len(words.split()) == 24
        state = request(endpoint, session, "status")
        address = state["address"]
        assert len(address) == 64 and address.startswith("qday1p")
        def indexed_status():
            status = request(endpoint, session, "status")
            return status if status["synced"] and status["balanceReady"] else None
        wait_for(lambda: request(endpoint, session, "status")["synced"], "mainnet peer synchronization")
        peer = request(endpoint, session, "peers", {"peer": f"127.0.0.1:{seed.p2p}"})
        assert peer["saved"] and peer["connected"]
        initial_port = request(endpoint, session, "status")["p2p"]["listenAddress"]
        request(endpoint, session, "start", {"threads": 1})
        wait_for(lambda: request(endpoint, session, "status")["height"] >= 62, "mainnet CPU mining and 60-block reward maturity", 240)
        request(endpoint, session, "stop", {})
        state = wait_for(indexed_status, "wallet indexing after mining")
        assert state["balance"] != "0"
        tx = request(endpoint, session, "send", {"address": address, "amount": "1", "unit": state["unit"]})
        assert tx["transaction"]
        try:
            request(endpoint, session, "proof/verify", {"witness": "01" + "00" * 31})
            raise AssertionError("Known test solution activated mainnet")
        except urllib.error.HTTPError as error:
            assert error.code == 400
        before = state["height"]
        request(endpoint, session, "start", {"threads": 1})
        wait_for(lambda: request(endpoint, session, "status")["height"] > before and request(endpoint, session, "status")["mempoolTransactions"] == 0, "signed transfer confirmation", 60)
        request(endpoint, session, "stop", {})
        state = wait_for(indexed_status, "wallet indexing after transfer")
        assert state["height"] >= 62 and not state["qday"] and not state["proofPending"]
        print(target + ": restart through the desktop supervisor", flush=True)
        old_pid, old_session = endpoint["pid"], session
        request(endpoint, session, "restart", {})
        def after_restart():
            e = json.loads((data / "node.json").read_text())
            if e["pid"] == old_pid:
                return None
            status = request(e, token, "status")
            return e if status["balanceReady"] and status["synced"] else None
        endpoint = wait_for(after_restart, "supervised node restart", 60)
        assert process.poll() is None, "desktop launcher exited during restart"
        restarted_state = request(endpoint, token, "status")
        assert restarted_state["address"] == address and restarted_state["height"] == state["height"]
        assert restarted_state["balance"] == state["balance"]
        assert restarted_state["mode"] == "STOP" and not restarted_state["unlocked"]
        assert restarted_state["p2p"]["listenAddress"] == initial_port
        try:
            request(endpoint, old_session, "status")
            raise AssertionError("Restart retained an old browser session")
        except urllib.error.HTTPError as error:
            assert error.code == 401
        launch = request(endpoint, token, "launch", {})["url"]
        code = urllib.parse.parse_qs(urllib.parse.urlsplit(launch).fragment)["launch"][0]
        session = request(endpoint, "", "session", {"code": code})["token"]
        request(endpoint, session, "unlock", {"password": PASSWORD})
        request(endpoint, session, "restore", {"password": PASSWORD, "phrase": words, "replaceAddress": address})
        imported = request(endpoint, session, "status")
        assert imported["address"] == address and imported["mode"] == "STOP"
        assert not any(p.name.startswith(".qday-") or "backup" in p.name for p in data.iterdir())
        request(endpoint, session, "shutdown", {})
        assert process.wait(timeout=15) == 0
        assert not (data / "node.json").exists()
        print(target + ": restart same wallet, unlock and exit cleanly", flush=True)
        process = subprocess.Popen(cmd, env=env, stdout=log, stderr=log)
        def restarted():
            if process.poll() is not None:
                raise RuntimeError("Launcher exited on restart")
            e = json.loads((data / "node.json").read_text())
            credential = (data / "api.token").read_text().strip()
            status = request(e, credential, "status")
            if status["address"] == address and status["height"] == state["height"]:
                assert not status["unlocked"] and status["mode"] == "STOP" and not status["qday"]
                assert status["p2p"]["listenAddress"] == initial_port
                return e, credential
        endpoint, token = wait_for(restarted, "restart")
        try:
            request(endpoint, token, "unlock", {"password": "wrong-test-passphrase"})
            raise AssertionError("Wrong password accepted")
        except urllib.error.HTTPError as error:
            assert error.code == 400
        request(endpoint, token, "unlock", {"password": PASSWORD})
        restored = request(endpoint, token, "status")
        assert restored["unlocked"] and restored["address"] == address
        request(endpoint, token, "shutdown", {})
        assert process.wait(timeout=15) == 0
        node_log = (data / "node.log").read_text()
        assert token not in node_log and words not in node_log and PASSWORD not in node_log
        assert words.encode() not in (data / "wallet.key").read_bytes()
        return {"target": target, "result": "PASS", "sha256": package["sha256"], "runtime": "Wine64 9.0 on Linux" if target.startswith("windows") else "native Linux x86_64",
            "checks": ["complete archive extraction and checksum", "final embedded manifest hash, timestamp, inscription and genesis validation", "embedded artwork, fonts and current UI", "paths with spaces", "single instance", "one-use browser authentication", "24-word encrypted wallet", "64-character Bech32m address", "three bundled pqday.com seeds", "mainnet CPU mining and 60-block maturity on a disposable inscribed genesis", "hybrid signed transfer", "known test solution rejected", "persistent random P2P port", "persistent restart", "manual peer saved and connected", "API restart replaces native process while launcher stays alive", "restart preserves balance and port, locks keys and invalidates old browser session", "password validation", "atomic wallet import without automatic backup", "clean launcher and node shutdown", "no secrets in logs"],
            "nativeWindowsValidated": False if target.startswith("windows") else None}
    finally:
        if process and process.poll() is None:
            try:
                request(endpoint, token, "shutdown", {})
                process.wait(timeout=15)
            except Exception:
                process.terminate()
                try:
                    process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    process.kill()
        log.close()
        seed.stop(); seed.log.close()


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--targets", nargs="+", default=["linux-amd64", "windows-amd64"])
    p.add_argument("--report", type=Path, default=ROOT / "build/distribution-report.json")
    args = p.parse_args()
    packages = json.loads(args.report.read_text())["packages"]
    selected = [package for package in packages if package["target"] in args.targets]
    if len(selected) != len(args.targets):
        raise RuntimeError("Build both distributions with make dist first")
    with tempfile.TemporaryDirectory(prefix="qday desktop ") as temp:
        report = {"result": "PASS", "platforms": [exercise(package, Path(temp)) for package in selected]}
    (ROOT / "build/desktop-report.json").write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps(report, indent=2))


if __name__ == "__main__":
    main()
