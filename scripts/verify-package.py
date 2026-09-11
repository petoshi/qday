#!/usr/bin/env python3
"""Verify QDAY wallet and standalone-node release archives without executing them."""
import argparse
import hashlib
import json
from pathlib import Path, PurePosixPath
import struct
import tarfile
import zipfile


ROOT = Path(__file__).resolve().parents[1]
EXPECTED_GENESIS = "d71aebcb687c2fca4d3a5819e6c632efa7d46731395970fce081f3dc57606a40"
EXPECTED_MANIFEST_SHA256 = "14d4a47a850f5ba9d81129142d8718c323d826b5f9459516374c4f0f92eaa65b"
EXPECTED_SEEDS = ["seed1.pqday.com:19771", "seed2.pqday.com:19771", "seed3.pqday.com:19771"]
TARGETS = {"linux-amd64", "linux-arm64", "windows-amd64", "macos-amd64", "macos-arm64"}
FORBIDDEN_NAMES = {"wallet.key", "api.token", "node.json", "p2p.json"}


def selected_package(report, target):
    package = next((item for item in report["packages"] if item["target"] == target), None)
    if package is None:
        raise RuntimeError("Package target is missing from the distribution report")
    return package


def safe_names(names, folder):
    for name in names:
        path = PurePosixPath(name)
        if "\\" in name or path.is_absolute() or ".." in path.parts or not path.parts or path.parts[0] != folder:
            raise RuntimeError("Unsafe archive path: " + name)


def verify_no_private_state(names):
    if any(PurePosixPath(name).name in FORBIDDEN_NAMES or name.endswith((".db", ".sqlite", ".sqlite3", ".log")) for name in names):
        raise RuntimeError("Release archive contains private or runtime state")


def verify_executable(data, target, name):
    if len(data) < 1_000_000:
        raise RuntimeError("Native executable is unexpectedly small: " + name)
    if target.startswith("linux"):
        machine = struct.unpack_from("<H", data, 18)[0] if data[:4] == b"\x7fELF" else None
        expected = 62 if target.endswith("amd64") else 183
    elif target.startswith("windows"):
        offset = struct.unpack_from("<I", data, 60)[0] if data[:2] == b"MZ" else 0
        machine = struct.unpack_from("<H", data, offset + 4)[0] if data[offset:offset + 4] == b"PE\0\0" else None
        expected = 0x8664
    else:
        machine = struct.unpack_from("<I", data, 4)[0] if data[:4] == b"\xcf\xfa\xed\xfe" else None
        expected = 0x01000007 if target.endswith("amd64") else 0x0100000c
    if machine != expected:
        raise RuntimeError("Native executable architecture mismatch: " + name)


class ReleaseArchive:
    def __init__(self, archive, target, expected_digest):
        self.archive = archive
        self.target = target
        suffix = ".zip" if target.startswith("windows") else ".tar.gz"
        if not archive.name.endswith(suffix):
            raise RuntimeError("Unexpected archive filename: " + archive.name)
        self.folder = archive.name.removesuffix(suffix)
        self.digest = hashlib.sha256(archive.read_bytes()).hexdigest()
        checksum = Path(str(archive) + ".sha256").read_text().strip().split()
        if checksum != [self.digest, archive.name] or expected_digest != self.digest:
            raise RuntimeError("Archive checksum mismatch: " + archive.name)
        if target.startswith("windows"):
            self.bundle = zipfile.ZipFile(archive)
            self.names = self.bundle.namelist()
        else:
            self.bundle = tarfile.open(archive)
            members = self.bundle.getmembers()
            if any(not (member.isfile() or member.isdir()) for member in members):
                raise RuntimeError("Release tar contains links or special files")
            self.names = [member.name for member in members]
        safe_names(self.names, self.folder)
        verify_no_private_state(self.names)

    def read(self, relative):
        name = self.folder + "/" + relative
        if name not in self.names:
            raise RuntimeError("Archive file is missing: " + name)
        if self.target.startswith("windows"):
            return self.bundle.read(name)
        stream = self.bundle.extractfile(name)
        if stream is None:
            raise RuntimeError("Archive file is missing: " + name)
        return stream.read()

    def close(self):
        self.bundle.close()


def verify_manifest(data, label):
    digest = hashlib.sha256(data).hexdigest()
    if digest != EXPECTED_MANIFEST_SHA256 or data != (ROOT / "qday-mainnet.json").read_bytes():
        raise RuntimeError(label + " mainnet manifest bytes do not match the published manifest")
    return digest


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--target", required=True, choices=sorted(TARGETS))
    parser.add_argument("--report", type=Path, default=ROOT / "build/distribution-report.json")
    args = parser.parse_args()
    package = selected_package(json.loads(args.report.read_text()), args.target)
    if package["genesis"] != EXPECTED_GENESIS or package["manifestSHA256"] != EXPECTED_MANIFEST_SHA256:
        raise RuntimeError("Distribution report contains the wrong mainnet identity")

    suffix_exe = ".exe" if args.target.startswith("windows") else ""
    wallet = ReleaseArchive(ROOT / package["archive"], args.target, package["sha256"])
    try:
        wallet_manifest = wallet.read("resources/qday-mainnet.json")
        config = json.loads(wallet.read("resources/distribution.json"))
        wallet_instructions = wallet.read("START-HERE.txt").decode("utf-8")
        launcher = wallet.read("QDAY-Wallet" + suffix_exe)
        wallet_node = wallet.read("resources/qday-node" + suffix_exe)
        verify_executable(launcher, args.target, "wallet launcher")
        verify_executable(wallet_node, args.target, "wallet node")
    finally:
        wallet.close()
    manifest_sha = verify_manifest(wallet_manifest, "Wallet")
    if config != {"format": 1, "network": "mainnet", "seeds": EXPECTED_SEEDS}:
        raise RuntimeError("Embedded distribution configuration mismatch")
    if "No Go, Python, Node.js, Docker" not in wallet_instructions:
        raise RuntimeError("Wallet start instructions are missing from the release")

    node = ReleaseArchive(ROOT / package["nodeArchive"], args.target, package["nodeSHA256"])
    try:
        node_manifest = node.read("qday-mainnet.json")
        node_instructions = node.read("START-HERE.txt").decode("utf-8")
        standalone_node = node.read("qday" + suffix_exe)
        verify_executable(standalone_node, args.target, "standalone node")
        for required in ("docs/consensus.md", "docs/protocol.md", "docs/integrations.md", "deploy/qday.service"):
            node.read(required)
    finally:
        node.close()
    verify_manifest(node_manifest, "Node")
    if standalone_node != wallet_node:
        raise RuntimeError("Standalone node does not match the node bundled with the wallet")
    if "--network" not in node_instructions or "TCP 19771" not in node_instructions:
        raise RuntimeError("Standalone-node start instructions are missing from the release")

    print(json.dumps({
        "result": "PASS",
        "target": args.target,
        "walletArchive": (ROOT / package["archive"]).name,
        "walletSHA256": package["sha256"],
        "nodeArchive": (ROOT / package["nodeArchive"]).name,
        "nodeSHA256": package["nodeSHA256"],
        "genesis": EXPECTED_GENESIS,
        "manifestSHA256": manifest_sha,
        "walletFiles": len(wallet.names),
        "nodeFiles": len(node.names),
    }, indent=2))


if __name__ == "__main__":
    main()
