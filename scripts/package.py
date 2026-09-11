#!/usr/bin/env python3
"""Build self-contained Windows/Linux wallets. Requires Go, gcc and MinGW.

The downloaded wallet needs none of those tools: open QDAY-Wallet and its
bundled node serves the graphical interface in the system browser.
"""
import argparse
import hashlib
import json
import os
import pathlib
import shutil
import subprocess
import tarfile
import zipfile

ROOT = pathlib.Path(__file__).resolve().parents[1]
GO = str(ROOT / ".tools/go/bin/go") if (ROOT / ".tools/go/bin/go").exists() else "go"
VERSION = "0.5.0"


def build(target, out):
    system, arch = target.split("-")
    env = os.environ.copy()
    env.update(GOOS=system, GOARCH=arch, GOAMD64="v1", CGO_ENABLED="1")
    suffix = ".exe" if system == "windows" else ""
    flags = "-s -w"
    tags = "netgo,osusergo,sqlite_omit_load_extension"
    if system == "windows":
        env["CC"] = os.environ.get("QDAY_WINDOWS_CC", "x86_64-w64-mingw32-gcc")
    else:
        # Pure Go DNS + static SQLite avoid a distro-specific glibc dependency.
        flags += " -linkmode=external -extldflags=-static"
    print(f"Building native node: {target}", flush=True)
    subprocess.run([GO, "build", "-trimpath", "-tags", tags, "-ldflags", flags,
                    "-o", str(out/"resources"/("qday-node"+suffix)), "./node/cmd/qday"], cwd=ROOT, env=env, check=True)
    env["CGO_ENABLED"] = "0"
    gui_flags = "-s -w" + (" -H=windowsgui" if system == "windows" else "")
    print(f"Building graphical launcher: {target}", flush=True)
    subprocess.run([GO, "build", "-trimpath", "-ldflags", gui_flags,
                    "-o", str(out/("QDAY-Wallet"+suffix)), "./node/cmd/qday-wallet"], cwd=ROOT, env=env, check=True)


def package(target, args):
    if target not in ("linux-amd64", "windows-amd64"):
        raise ValueError("Supported targets: linux-amd64, windows-amd64")
    network = "mainnet"
    name = f"QDAY-Wallet-{VERSION}-{network}-{target}"
    out = args.output/name
    if out.exists():
        # Only this generated, versioned distribution folder is replaced.
        shutil.rmtree(out)
    (out/"resources").mkdir(parents=True)
    build(target, out)
    config = {"format": 1, "network": network, "seeds": args.peers}
    (out/"resources/distribution.json").write_text(json.dumps(config, indent=2)+"\n")
    if args.manifest:
        shutil.copy2(args.manifest, out/"resources/qday-mainnet.json")
    shutil.copytree(ROOT/"docs", out/"docs")
    shutil.copytree(ROOT/"licenses", out/"licenses")
    shutil.copytree(ROOT/"deploy", out/"deploy")
    for source in ("node", "core", "coreutils"):
        shutil.copy2(ROOT/source/"LICENSE", out/"licenses"/(source+"-LICENSE"))
    shutil.copy2(ROOT/"third_party/upnp/LICENSE", out/"licenses/upnp-LICENSE")
    shutil.copy2(ROOT/"third_party/upnp/QDAY-PATCHES.md", out/"licenses/upnp-QDAY-PATCHES.md")
    start = "QDAY-Wallet.exe" if target.startswith("windows") else "QDAY-Wallet"
    notice = (f"MAINNET GENESIS: {args.genesis_id}\n"
              f"GENESIS TIME: {args.genesis_timestamp}\n"
              f"MANIFEST SHA-256: {args.manifest_sha256}\n"
              "Verify the published genesis hash and release checksum.")
    if args.genesis_message:
        notice += f"\nGENESIS INSCRIPTION: {args.genesis_message}"
    instructions = f"""QDAY WALLET {VERSION} — {target}
{notice}

1. Extract the ENTIRE archive into a folder.
2. Open {start}. Your normal browser opens the graphical wallet automatically.
3. Create or restore a wallet, save the 24-word seed phrase and choose a passphrase.
4. Wait for synchronization, choose CPU threads and press MINE.

No Go, Python, Node.js, Docker or separately installed database is required.
The resources folder must remain next to {start}.
Your keys and blockchain data stay on this computer, outside this download folder.
Wallet data: Windows %APPDATA%\\qday; Linux ~/.config/qday (or XDG_CONFIG_HOME).
The mining engine runs in the native application, not in browser JavaScript.
Closing the browser leaves QDAY running. EXIT QDAY in the top right stops the complete app.
Settings provides Import seed phrase and Export seed phrase. Import replaces
the active wallet without keeping a backup; save your current seed phrase first.
The locked screen also accepts any valid QDAY seed phrase. The same phrase resets
the password; a different phrase replaces the local wallet without a backup.
Open {start} again to reopen an already running wallet.
Linux: if your file manager does not launch executables, run ./{start} from this folder.

Windows executables are currently unsigned. Windows-native release testing and
publisher signing are separate from Linux/Wine verification. Do not disable
security software to install this application.
"""
    (out/"START-HERE.txt").write_text(instructions, encoding="utf-8")
    executable = out/start
    if not target.startswith("windows"):
        executable.chmod(0o755)
        (out/"resources/qday-node").chmod(0o755)
    if target.startswith("windows"):
        archive = out.parent/(name+".zip")
        with zipfile.ZipFile(archive,"w",compression=zipfile.ZIP_DEFLATED,compresslevel=9) as z:
            for file in sorted(out.rglob("*")):
                if file.is_file(): z.write(file, file.relative_to(out.parent))
    else:
        archive = out.parent/(name+".tar.gz")
        with tarfile.open(archive,"w:gz") as tf: tf.add(out,arcname=name)
    sha = hashlib.sha256(archive.read_bytes()).hexdigest()
    pathlib.Path(str(archive)+".sha256").write_text(sha+"  "+archive.name+"\n")
    print(archive.relative_to(ROOT), flush=True)
    return {"target":target,"network":network,"genesis":args.genesis_id,
            "genesisTimestamp":args.genesis_timestamp,"manifestSHA256":args.manifest_sha256,
            "genesisMessage":args.genesis_message,"archive":str(archive.relative_to(ROOT)),
            "sha256":sha,"signed":False}


def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument("--targets",nargs="+",default=["linux-amd64","windows-amd64"])
    p.add_argument("--manifest", type=pathlib.Path, required=True, help="validated QDAY mainnet manifest")
    p.add_argument("--peers",nargs="*",default=(ROOT/"node/internal/localapp/seeds.txt").read_text().split(),help="bootstrap peers bundled in the distribution (defaults to the three pqday.com seeds)")
    p.add_argument("--output",type=pathlib.Path,default=ROOT/"build/dist",help="archive output directory")
    p.add_argument("--report",type=pathlib.Path,default=ROOT/"build/distribution-report.json",help="distribution report path")
    args=p.parse_args()
    args.output=args.output.resolve()
    if args.manifest and not args.peers: p.error("a mainnet wallet distribution requires published bootstrap peers")
    if args.manifest and not args.manifest.is_file(): p.error("mainnet manifest not found")
    if args.manifest:
        # Reconstruct and validate genesis without starting a node or creating keys.
        validation = subprocess.run([GO, "run", "./node/cmd/qday", "--network", str(args.manifest.resolve()), "--validate"], cwd=ROOT, check=True, text=True, capture_output=True)
        print(validation.stdout, end="")
        args.genesis_id = next((line.removeprefix("Genesis: ") for line in validation.stdout.splitlines() if line.startswith("Genesis: ")), "")
        if not args.genesis_id:
            raise RuntimeError("validated manifest did not report a genesis ID")
        manifest_data = json.loads(args.manifest.read_text())
        args.genesis_timestamp = manifest_data["genesis"]["timestamp"]
        args.genesis_message = manifest_data.get("genesisMessage", "")
        args.manifest_sha256 = hashlib.sha256(args.manifest.read_bytes()).hexdigest()
    report={"version":VERSION,"genesis":args.genesis_id,"genesisTimestamp":args.genesis_timestamp,
            "manifestSHA256":args.manifest_sha256,"genesisMessage":args.genesis_message,
            "packages":[package(target,args) for target in args.targets]}
    args.report.write_text(json.dumps(report,indent=2)+"\n")


if __name__=="__main__":main()
