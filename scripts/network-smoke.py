#!/usr/bin/env python3
"""Check seed release, seed freshness and mesh recovery with real mainnet rules.

All listeners are reachable on loopback. This is not an Internet NAT test.
"""
import json
from pathlib import Path
import socket
import struct
import tempfile

from smoke import Node, ROOT, fixture, wait


def main():
    report = {"scope": "six local processes; exact mainnet consensus; disposable genesis; no real router", "checks": []}
    nodes = []
    with tempfile.TemporaryDirectory(prefix="qday-network-") as temp:
        work = Path(temp)
        try:
            manifest, phrase = fixture(work)
            seed1 = Node(work/"s1", "network-seed1", manifest=manifest, seed_node=True); nodes.append(seed1)
            seed2 = Node(work/"s2", "network-seed2", seed1, manifest=manifest, seed_node=True); nodes.append(seed2)
            seed3 = Node(work/"s3", "network-seed3", seed1, manifest=manifest, seed_node=True, peers=[f"127.0.0.1:{seed2.p2p}"]); nodes.append(seed3)
            seeds = [f"localhost:{seed1.p2p}", f"localhost:{seed2.p2p}", f"localhost:{seed3.p2p}"]
            clients = []
            for name in ("a", "b", "c"):
                n = Node(work/name, "network-"+name, manifest=manifest, seeds=seeds)
                nodes.append(n); clients.append(n)
            a,b,c = clients
            a.api("create", {"password":"temporary-network-passphrase", "phrase":phrase})
            b.api("create", {"password":"temporary-network-passphrase"})
            wait(lambda: all(n.api("status")["synced"] for n in nodes), "initial mainnet synchronization", 60)
            report["initial"] = [n.api("status")["p2p"] for n in clients]
            wait(lambda: all(n.api("status")["p2p"]["regularPeers"] == 2 and n.api("status")["p2p"]["bootstrapOutbound"] == 0 for n in clients), "release initial seed links after 30 seconds", 90)
            report["released"] = [n.api("status")["p2p"] for n in clients]
            report["checks"].append("DNS seeds released after two stable ordinary peers; incoming seed mesh links retained")
            state = a.api("status")
            tx = a.api("send", {"address":b.api("status")["address"],"amount":"10","unit":state["unit"]})
            wait(lambda: all(n.api("status")["mempoolTransactions"] >= 1 for n in nodes), "transaction reaches all three seeds after bootstrap release", 60)
            report["checks"].append("all three seeds receive transactions while original bootstrap links are released")
            a.api("start", {"threads":1})
            wait(lambda: min(n.api("status")["height"] for n in nodes) >= 3, "blocks reach every client and all three seeds", 60)
            a.api("stop", {})
            wait(lambda: len({n.api("status")["height"] for n in nodes}) == 1, "all six tips converge")
            report["heightWithSeedsCurrent"] = seed1.api("status")["height"]
            report["checks"].append("all three seeds stay on the current chain after client bootstrap release")
            seed1.stop(); seed2.stop(); seed3.stop()
            wait(lambda: all(n.api("status")["p2p"]["regularPeers"] == 2 for n in clients), "mesh survives all three seeds offline")
            previous = a.api("status")["height"]
            a.api("start", {"threads":1})
            wait(lambda: min(n.api("status")["height"] for n in clients) >= previous+2, "mining with all three seeds offline", 60)
            a.api("stop", {})
            c.stop(); c.start()
            wait(lambda: c.api("status")["height"] == a.api("status")["height"] and c.api("status")["synced"], "restart reconnects through saved ordinary peers without seeds", 60)
            report["checks"].append("mesh continues mining with all three seeds offline; restarted node uses saved peers")
            # Sia's length-prefixed version prelude must be rejected before RPCs.
            with socket.create_connection(("127.0.0.1",a.p2p),timeout=3) as conn:
                conn.sendall(struct.pack("<Q",13)+struct.pack("<Q",5)+b"2.0.0")
                try:
                    assert conn.recv(32) == b"", "Sia prelude accepted"
                except ConnectionResetError:
                    pass
            report["checks"].append("real node rejects Sia prelude before exchanging protocol data")
            report["result"] = "PASS"
        except Exception as error:
            report.update(result="FAIL", error=str(error))
            raise
        finally:
            for n in nodes:
                n.stop(); n.log.close()
            (ROOT/"build/network-report.json").write_text(json.dumps(report,indent=2)+"\n")
    print(json.dumps(report,indent=2))


if __name__ == "__main__":
    main()
