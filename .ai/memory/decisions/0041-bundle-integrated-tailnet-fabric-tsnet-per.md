---
number: 41
title: "bundle-integrated tailnet fabric: tsnet per component, headscale in bundle, uniform transport"
date: "2026-11-08"
updated: "2026-11-08"
status: "accepted"
supersedes: []
adopted-at: "2026-11-08"
---

# AD-41 — bundle-integrated tailnet fabric: tsnet per component, headscale in bundle, uniform transport

## Context

Phase-1's envelope (AD-35's deferral, topology-l1 §1) fixed the bundle
to a single gateway front, HTTP, plain IPs, a trusted network;
cross-NAT connectivity was deferred as #112 with a named trigger.
The trigger has fired: deployments are now planned across mixed
public/private networks — multi-machine topology, engines and
gateways on different nets, roaming administrators — and the design
discussion in #112 (operator decisions of 2026-11-08) converged on a
self-hosted WireGuard-overlay architecture. Four operator constraints
stacked during that discussion: (a) no Tailscale cloud anywhere —
every cloud-provided function is self-hosted; (b) the overlay is
integrated INTO the bundle, never a host-level install; (c) the
looming client alone deploys everything, no external references;
(d) the transport model must be as simple and uniform as possible.

## Decision

1. **Overlay**: a Tailscale-class WireGuard network embedded
   per-component via `tsnet` — the in-process Go library with a
   gVisor userspace netstack. Hosts install and perceive nothing:
   containers need only outbound UDP (docker default) plus a
   per-component state volume for node-key persistence. No tun
   device, no NET_ADMIN, no route changes.
2. **Control plane**: `headscale` ships as a bundle component (a
   compose service; the first host by default), plus a self-hosted
   `derper` for the NAT-traversal fallback. Nothing — no traffic, no
   identity, no roster — transits Tailscale the company; only the
   protocol and the client library are borrowed.
3. **Node model — uniform**: every looming component is its own
   tailnet node (gateway-front, identityd, topologyd, and future
   components). Key mint/rotate/revoke is automated against the
   bundle's headscale; the operator's surface is the looming CLI.
   (Chosen over per-host nodes and hub models: with automation the
   roster cost is small, and one node per service keeps ACL and audit
   granularity maximal. The hub model was rejected as a bottleneck
   and single point of failure.)
4. **Transport — uniform**: ALL component↔component traffic rides
   the tailnet, same-host included. Addressing is a component's
   tailnet name only — no local-first resolver, no dual addresses,
   one transport story. The LLM data plane (gateway↔upstream) never
   touches the tailnet: upstream reachability is operator-side
   networking, out of bundle scope. The loading concern is answered
   by traffic class: the tailnet carries control-plane flows only.
5. **Third-party workloads** (postgres today, others someday) join
   through a `looming-net` sidecar: our tiny Go binary embedding
   tsnet, listening on a tailnet address and forwarding to the
   co-located workload. The sidecar holds the workload's node
   identity; the third-party image is never modified. Without this,
   the "uniform" addressing model would crack multi-host (container
   networks do not span machines) and force a legacy-network
   exception.
6. **Client access**: v1 default — humans reach the gateway over
   ordinary networks (LAN; a future reverse proxy under #110); the
   overlay is machine-fabric only. Optional `looming connect`
   installs the tailscale client on an admin device, pointed at the
   bundle's headscale, in one command. Funnel-class public ingress
   is unavailable without Tailscale's cloud, so public ingress
   reverts to a classic reverse proxy (#110) or is deferred.

## Consequences

- The phase-1 "trusted network, plain IPs" envelope is superseded for
  component traffic and client ingress; upstream links stay
  operator-side plain networking. topology-l1 §1/§5 and the L0
  context map are updated in the same PR as this AD.
- Host aggregate evolution: `Host.address` becomes a tailnet
  identity issued at join time; join tokens evolve toward headscale
  pre-authorized keys (the bootstrap flow lives in `looming setup`,
  AD-42).
- Capability boundary (operator principle, 2026-11-08): per-component
  capability ships inside looming binaries — tsnet rides as a library
  dependency, and identity issuance / control-plane API client /
  config rendering / lifecycle are ours to implement. Per-deployment
  singletons (headscale, derper) stay upstream binaries that looming
  binaries manage and version-pin, never vendor. The net sidecar is
  the worked example of the first half: no upstream equivalent
  existed, so the workload-onboarding function is our own small
  binary.
- New bundle components to version and operate: headscale, derper,
  and the looming-net sidecar; every component gains a node-key
  volume; state backup/restore must cover these keys.
- Open, each with its owner issue: in-net TLS (own certs → #110),
  in-net naming (own MagicDNS-equivalent → #111), gateway-HA node
  identities (#109).
- The e2e layer (AD-25) gains a cross-host scenario over the fabric
  once the setup flow (AD-42) lands.
