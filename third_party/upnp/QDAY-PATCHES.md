# QDAY UPnP maintenance

Based on `lukechampine.com/upnp` v0.3.0, already used by Sia walletd.
The original MIT license is preserved alongside this file.

QDAY additions:

- Context-aware, bounded HTTP requests without environment proxies or redirects.
- Response size limits and same-router URL validation.
- SSDP descriptions restricted to the responding device, with a device count cap.
- Correct relative control URL resolution and default HTTP port handling.
- Full mapping inspection, typed UPnP errors and renewable lease support.

The native node uses a one-hour TCP lease and renews it every 15 minutes.
UPnP error 725 permits a permanent lease fallback for legacy routers. Shutdown
rechecks the internal address, internal port and persistent owner description
before deleting a rule. Existing rules owned by other software are preserved.

Relevant primary specification: [WANIPConnection](https://upnp.org/specs/gw/UPnP-gw-WANIPConnection-v1-Service.pdf).
Mapping success does not establish reachability through upstream NAT, VPN
filters or operating-system firewalls. QDAY does not implement hole punching.
