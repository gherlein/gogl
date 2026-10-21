# gogl - Go GL.iNet Travel Router

A Go module for programmatic control of GL.iNet travel routers running firmware 4.x,
targeting the **GL-SFT1200 (Opal)**.

The purpose is to make a travel router's whole network **reproducible from a file** rather
than from an hour of clicking through the admin panel. That matters most when the router is
serving as an isolated test network for development or testing: the addressing your devices
and test harness depend on becomes something you commit, review and re-apply, on this router
or on a second one.

Addresses and names come from two different mechanisms: a reservation on this firmware does
not create a DNS record, so names are written separately into the router's host file. The CLI
writes both from one declaration. See [The Reservation Model](#the-reservation-model).

## Target Device

| Property | Value |
|----------|-------|
| Model | GL-SFT1200 ("Opal") |
| Firmware verified | **4.3.28** (model string `sft1200`) |
| SoC | SiFlower SF19A28 |
| OpenWrt base | 18.06 |
| Ethernet | 1 x GbE WAN, 2 x GbE LAN |
| Wireless | AC1200 dual-band (2.4 GHz 300 Mbps, 5 GHz 867 Mbps) |
| Default LAN | `192.168.8.1/24` |
| Admin user | `root` (password set during first-run setup; there is no default) |

Firmware 4.x is a hard requirement. GL.iNet replaced the older REST API with JSON-RPC at
firmware 4.0, and `gogl` speaks only JSON-RPC. Firmware 3.x devices are out of scope.

Other GL.iNet 4.x models should work, since the API is shared across the firmware line, but
the SFT1200 is the only device this module is tested against — and it is a **reduced build**.
Nine documented endpoints are absent on it. Three appear in
[`docs/api/`](docs/api/README.md), where every method is marked verified, absent or untested;
the other six are documented only in GL.iNet's original public reference, which is no longer
reachable, and are recorded in
[`GL_INET_4X_API_DOCUMENTATION.md`](GL_INET_4X_API_DOCUMENTATION.md).

## Project Resources

- **API Reference**: [`GL_INET_4X_API_DOCUMENTATION.md`](GL_INET_4X_API_DOCUMENTATION.md) -
  authentication, error codes, and every endpoint verified against a live SFT1200.
- **Full API**: [`docs/api/`](docs/api/README.md) - all 43 groups and 313 methods, generated
  from GL.iNet's own API description.
- **Architecture**: `docs/DESIGN.md` - Design with mermaid diagrams, service interfaces,
  type definitions.
- **Implementation Plan**: `docs/plan.md` - Phased plan with progress tracking.

## Critical Rules

1. **Every function MUST have a test** - No exceptions. Run `make test` to verify.
2. **Every endpoint MUST be supported in the mock server** - Tests use the mock, not real hardware.
3. **No phase advancement without 100% test coverage** - Complete and test each phase before moving on.
4. **Phases are sequential** - Follow `docs/plan.md` in order.
5. **Structured transports only** - Every operation goes through GL.iNet's JSON-RPC API, so
   the whole surface stays reachable by an in-process mock and no hardware appears in the test
   suite. **Never build a command string from user-supplied data.** Anything the API cannot
   express is a documented gap, never a shell workaround.

   This rule originally read "no SSH, no UCI, no shell", and the justification first written
   for it -- that SSH and shell are unmockable -- was false. `uci show` is text in and text
   out, easier to mock than the HTTP server this project already mocks. The rule is kept for
   the two reasons that actually hold.

   A structured API is a capability boundary: it can set a DHCP pool and cannot delete `/etc`,
   which bounds the blast radius of a bug in a tool built for unattended provisioning. And it
   keeps user data out of shell command strings, which matters here because a quote in a
   hostname can corrupt dnsmasq's configuration -- the reason `gogl` rejects such names rather
   than escaping them.

## Scope

**GL.iNet firmware 4.x only. This is not a portable OpenWrt tool and will not become one.**

The devices run OpenWrt underneath, and roughly 59% of GL.iNet's 313 API methods wrap standard
OpenWrt or Linux subsystems, so a UCI-over-`/ubus` backend was considered and specified. It was
dropped deliberately: supporting arbitrary OpenWrt hardware means two implementations of every
service, two credential paths, two sets of field-name translations, and a name that no longer
describes the tool -- in exchange for devices this project does not target. Focus is worth more.

The practical consequences, all of them simplifications:

- `src/types` may hold GL.iNet's wire shapes without apology. `IntBool` exists because the
  firmware sends `enable: 1`; `HTModes` is its capability object. These were once described here
  as vendor artifacts leaking into a neutral layer. There is no neutral layer, so they are simply
  the domain.
- The DNS domain living in a marker line inside the host file is the answer, not a workaround
  pending a better backend. GL.iNet's API exposes no dnsmasq domain setting and `POST /ubus`
  returns 404 on this hardware, so there is nothing better to wait for.
- `wan` can model GL.iNet's own concepts -- `repeater`, `tethering`, `modem`, `netmode` -- with
  no question of how vendor-specific commands would fit a portable tool.
- The name and the logo stay.

`gogl` v1 manages, on a single flat LAN with no VLANs:

- **Network configuration** - LAN address, netmask, DHCP pool. Read and **written**
  (`lan.set_config`). Writing is refused while reservations exist; see
  [Ordering Rules](#ordering-rules).
- **Static IP reservations** - MAC-to-IP bindings, created, updated, deleted individually or
  cleared wholesale.
- **DNS names** - through the router's host file (`dns.set_host`), which dnsmasq answers from.
  A reservation does **not** create a DNS record; see
  [The Reservation Model](#the-reservation-model).
- **The DNS domain** - carried by gogl inside its block in the host file, because the firmware
  exposes no dnsmasq domain setting. Required before any reservation write.
- **DHCP leases** - read-only, and the way to discover what is worth reserving.
- **Whole-network profiles** - every writable section above, captured to JSON and applied back,
  onto the same router or another one. See [Profiles](#profiles).
- **Connected clients** - with independent IEEE OUI manufacturer lookup.
- **Wireless identity** - SSID, passphrase, encryption, hidden and enabled state, per
  interface. Read and **written**.
- **Radio tuning** - channel, bandwidth, hardware mode, transmit power, per radio. Read and
  **written**. Both under the safety rules in [Wireless Writes](#wireless-writes).

Explicitly **out of scope for v1**:

- **VLANs.** The Opal exposes no VLAN configuration through its API.
- **WAN configuration and device access control.** `netmode`, `cable`, `repeater`,
  `tethering`, `modem` and `acl` have no endpoint this project has exercised against hardware.
  Designed but not built; see [`TODO.md`](TODO.md).
- **Lease time and upstream DNS servers.** Readable, with no verified write endpoint.
- **VPN, firewall, port forwarding, traffic rules.**
- **dnsmasq's own `domain` / `local` / `expandhosts` settings.** No endpoint exposes them and
  `/ubus` is unavailable. gogl writes fully-qualified names into the host file instead, which
  works for any suffix.

An earlier version of this document made "reservations are the only thing gogl writes" a
design rule, on the grounds that a bulk import should never be able to change the address the
router is managed at. That concern is real, and it is now handled by the ordering rules below
rather than by refusing to write at all.

## Ordering Rules

Two preconditions, both enforced in the library so no consumer can bypass them, and both
covering states that are tedious to recover from.

### A reservation write requires a configured domain

`Create` and `Update` return `ErrDomainNotSet` until a domain exists.

A reservation creates no DNS record on its own. Writing one before the domain is set yields an
address with no name and nothing to indicate that was unintended -- you find out later, when
something cannot resolve. Making the domain a precondition forces the pairing to be
deliberate. Reads and deletes are ungated: only writes that create addressing are.

### Moving the subnet requires no reservations, unless forced

`Network().Set` returns `ErrReservationsExist` while any reservation is present, unless called
with `WriteForced`.

The original reason was wrong and is worth recording. It was written expecting the firmware to
leave static binds alone, stranding all of them outside the new subnet. Observed on hardware
2026-07-29: the firmware silently renumbers every bind into the new subnet, preserving host
parts. Twenty-seven reservations moved from `192.168.2.x` to `192.168.8.x` with no prompt.

The guard is kept on narrower grounds. An unannounced rewrite of every reservation is a large
side effect of an address flag, and the behavior is only characterized for a same-size subnet:
a narrower netmask, where a host part no longer fits, is untested, as is a move that lands
addresses inside the new DHCP pool -- which happened to twenty of those twenty-seven. `--force`
exists because the rewrite is usually what the operator wants.

**A pool-only change is never guarded.** The subnet does not move, so the session survives and
no reservation can be renumbered by it. Guarding it would be guarding against something that
cannot happen. Only a change to the address or the netmask counts as moving the subnet.

The correct order is therefore:

1. `gogl lan dns set --domain <domain>`
2. `gogl lan reservations clear` if reservations already exist, or pass `--force` in step 3
3. `gogl lan set --ip ... --mask ... --pool-start ... --pool-end ...`
4. `gogl lan reservations import <file>`

Step 3 moves the router, so the session drops. That is inherent, not a defect. Both writing
steps take `--dry-run`, and a dry run runs every check the real write runs, including the
refusals: a preview that approves what the write would reject is worse than no preview.

`--clear` removes the managed DNS names along with the reservations, because they are one
intent stored in two tables. Clearing only the bindings would leave names resolving to
addresses the router no longer reserves, and since clearing is what unblocks step 3, those
names would end up answering for a subnet that no longer exists — exactly the stranded state
the guard exists to prevent. The domain from step 1 survives a clear.

## Wireless Writes

Reproducing a network means reproducing what devices connect *to*, not only what addresses they
receive. A signage player or robot configured for a given SSID does not care that the DHCP
reservations are right if it cannot associate. So `gogl` writes wireless identity: SSID,
passphrase, hidden flag, and enabled flag, per interface.

This was out of scope in v1 for a reason that has not gone away, so the reason becomes a
guard rather than a prohibition.

### The lock-yourself-out problem

Changing an SSID or passphrase drops every client on that radio, including the session issuing
the request. Unlike a LAN renumber, there is no new address to reconnect at: the network the
management session was using has ceased to exist under that name. Recovery means ethernet or
the reset pin.

These rules apply to **every** wireless write, tuning included. Retuning a radio drops its
clients for at least a re-association, and a DFS channel change for the minutes the radio
spends re-scanning, so the distinction between "identity" and "tuning" does not matter to
whoever loses their session.

Two rules:

1. **The session must not be arriving over WiFi.** `gogl` determines the local address it
   reaches the router from, finds that address in `client.get_list`, and reads the `iface`
   field the firmware reports for it -- `cable`, `2.4G` or `5G`. Anything but `cable` is
   refused with `ErrWirelessSession`.

   If the address is not in the client list at all, the session is arriving from off-LAN
   through a router, so the radio cannot be the path. That is allowed, and noted.

   A stricter reading would refuse only when the session arrives over the *same* radio being
   modified, since changing the 2.4G SSID from a 5G association is safe. That refinement is
   deliberately not in this version: the simple rule is the one that cannot be got wrong, and
   the cost of it is having to plug in a cable.

2. **A human must confirm.** Interactive invocations prompt `y/N` and show the before and
   after. `--yes` skips the prompt for scripted use, and a non-terminal stdout does not
   silently proceed -- unlike `--del`, where proceeding is the safe assumption. Here the
   failure mode is losing the device, so the default is to stop.

### Partial updates, and why the flags look the way they do

`wifi.set_config` leaves any field it is not given alone, and `gogl` sends only the fields
asked for. Sending the unchanged values back would work, but would turn every write into a
chance to clobber a concurrent edit from the admin panel.

That requires telling "set this to false" apart from "do not mention it", which Go's `flag`
package cannot do: both leave a `*bool` at `false`. So the wireless flags take an explicit
value -- `--set-hidden=false`, not a bare `--set-hidden`. A bare boolean flag meaning true is
exactly how `--set-enabled` ends up disabling something.

The same applies to `--set-channel=0`, where 0 is a real value meaning "choose
automatically" and so cannot double as a not-set sentinel.

### The firmware scopes writes two ways

Interface-scoped fields -- SSID, passphrase, encryption, hidden, enabled -- require
`iface_name`. Radio-scoped fields -- channel, bandwidth, hardware mode, transmit power --
require `device`. A write carrying both goes out as two calls, because that is how the
firmware separates them, and because a failure then names which half did not apply.

`gogl lan show` reports what each radio advertises as supported: its channels with the DFS ones
marked, its bandwidths, its hardware modes, its encryptions. Without that the tuning flags are
unusable, since the valid values differ per radio and per regulatory domain. `gogl` validates
against those lists before writing, so a bad channel is refused with the available ones named
rather than answered with a bare error code.

### The capability payload is not what the docs say

`htmodes` is the field to be careful with. GL.iNet's description calls it a "List of supported
bandwidths", an array of strings; the device sends an object keyed by hardware mode whose values
are the maximum channel width in MHz, plus an `auto` key holding a bool:

```json
"htmodes": {"11b/g/n": 40, "11g/n": 40, "11n": 40, "auto": true}
```

Typing it from the description made every read of `wifi.get_config` fail outright. `hwmodes` was
wrong too -- slash-joined combinations such as `11a/n/ac`, not the bare `11b`/`11g`/`11n` the
description implies -- and the encryption list contains `sae` and `sae-mixed` for WPA3 while
containing no bare `psk`.

Two consequences for the design. The settable `htmode` is a width string (`auto`, `20`, `40`,
`80`), and the narrower widths are *inferred* to be valid from the reported maximum, since the
firmware states only the maximum. And `src/mock`'s wireless fixture is a verbatim capture rather
than a composed example, with a test asserting it decodes through the same types the library
uses -- the check whose absence let a fixture and a type agree with each other while both
disagreed with the hardware.

### The passphrase is readable, and that is accepted

`wifi.get_config` and `system.get_status` both return passphrases in cleartext over plain HTTP
on port 80. Reading them requires being on the router's network and holding the admin password,
which is the same bar as opening the admin panel, so this is not treated as a defect to work
around. `gogl` does not log the raw payloads, and `gogl lan show` masks the key in its own output
unless asked for it, because a passphrase on a terminal is a passphrase in a scrollback buffer.

## Profiles

`gogl profile` captures the writable sections as a JSON profile and applies one back. It is its
own area because the work spans `lan`, `lan reservations`, `lan dns`, `wifi` and `radio` and
belongs in none of them.

### A profile is a network, not a router

It carries the LAN address and pool, reservations, DNS names and domain, wireless identity and
radio tuning. It omits the router's own MAC, serial number, uptime and lease state -- the fields
that make a full configuration dump worthless on a second unit, which is the case a profile
exists to serve. Client MAC addresses are included: a reservation is a MAC-to-IP binding and a
profile without them reproduces nothing.

Every section comes from an endpoint verified against hardware. The API exposes 110 getters and
23 are verified; a profile built on the remainder would be guesswork, and guessing from GL.iNet's
descriptions has been wrong three times in this project. Lease time, upstream DNS servers,
firewall, VPN and VLANs are absent for that reason, not because they were forgotten.

### Passphrases are omitted unless asked for

A profile is a file people commit. `gogl profile export` writes no WiFi keys;
`gogl profile export --with-keys` includes them in cleartext.

An omitted key is not an empty key. On apply, a missing key is not written at all, leaving
whatever the target already has -- so the private default is also the safe one. This depends on
`wifi.set_config` leaving unmentioned fields alone, verified on hardware 2026-07-29.

### Apply order

Fixed, and each step is where it is because doing it later fails:

1. **Domain.** Reservation writes are refused without one.
2. **Network.** Reservations must be inside the subnet before they are written.
3. **Reservations**, then **DNS names**.
4. **Wireless**, opt-in via `--wireless`. It needs a wired session and is the section least
   likely to be wanted, so a refusal there must not undo the addressing.

### A subnet move ends the run

When the profile's subnet differs from the router's, the router changes address during step 2 and
nothing after it is reachable from that session. `gogl profile` stops, says the router has moved, and
prints the command to resume at the new address.

Reporting success for a run that applied a third of a profile would be a lie, and continuing
would mean writing reservations over a connection that no longer exists. A pool-only difference
is not a move and runs straight through.

### Cross-model applies warn rather than fail

Addresses and names are portable. Wireless is not: interface names, radio names, channel lists
and hardware modes are per-device and per-regulatory-domain. A profile from another model warns,
and any interface or radio the target lacks is reported and skipped rather than failing the load.

## Concurrency Requirements

- **Session ID**: Use `atomic.Value` for thread-safe storage and updates.
- **Re-authentication**: Use `sync.Mutex` so that a burst of calls hitting an expired
  session triggers exactly one re-login, not one per call.
- **Keepalive**: A single background goroutine issues `alive`; it must be cancellable via
  the client's context and must not outlive `Close()`.
- **Rate limiting**: Implement semaphore-based request limiting. The SFT1200 is a small
  SoC and will drop requests under load.
- **Connection pooling**: Configure `http.Transport` appropriately.

## Architecture Overview

The module path is `github.com/gherlein/gogl`; the library source lives under
`src/`, so the root package is imported as `github.com/gherlein/gogl/src`
(package name is `gogl`). `examples/` and `utilities/` remain at the repo top level.

Keeping the library under `src/` rather than at the root leaves `examples/` and `utilities/`
as peers of it, so a reader sees the three surfaces — library, consumers, CLI — at one level.

```
gogl/
├── src/               # Library source (package gogl + sub-packages)
│   ├── client.go      # Main client, session lifecycle, request handling
│   ├── types/         # All domain types (Network, Reservation, Client, ...)
│   ├── errors.go      # Sentinel errors, RPCError type
│   ├── services/      # Service implementations
│   │   ├── network.go     # LAN address, DHCP server settings
│   │   ├── reservation.go # Static MAC-to-IP bindings
│   │   ├── client.go      # Connected clients
│   │   └── system.go      # Model, firmware version, uptime
│   ├── mock/          # Mock JSON-RPC server for testing
│   │   ├── server.go
│   │   ├── handlers.go
│   │   ├── fixtures/
│   │   └── scenarios/
│   ├── auth/          # Challenge/response login, unix crypt hashing
│   ├── transport/     # JSON-RPC envelope, sid injection, retry on expiry
│   └── internal/      # Internal helpers
├── examples/          # Runnable example programs (consumers)
└── utilities/         # gogl/ (cobra tree) and internal/ (its packages)
```

### What the service layer does and does not have

- **No site concept.** A GL.iNet router has nothing equivalent to a controller managing many
  sites, so no method takes a `site` parameter.
- **A separate `Hosts()` service, not DNS-via-reservations.** A reservation does not create a
  DNS record on this firmware — see [The Reservation Model](#the-reservation-model) — so names
  live in their own service over the router's host file. `Reservations()` and `Hosts()` are two
  services precisely because the firmware stores the two things separately; the CLI writes both
  from one declaration so callers do not have to.
- **No device service.** The router is the only device. `System()` reports it.
- **No event stream.** Firmware 4.x exposes no WebSocket or push channel; every read is a poll.

## Key Technical Details

- **Endpoint**: A single JSON-RPC 2.0 endpoint at `POST /rpc`. There are no per-resource
  paths and no API versions in the URL.
- **Auth**: Challenge/response. The password is never transmitted, in cleartext or
  otherwise; only a digest derived from it.
- **Session**: A `sid` returned by `login`, passed as the first element of every
  subsequent call's `params` array.
- **Scheme**: GL.iNet routers serve the admin interface over **HTTP on port 80** by
  default, rather than HTTPS on 443. So `gogl` defaults to HTTP port 80 and
  requires `--https` to use TLS.
- **TLS**: Where HTTPS is available it uses a self-signed certificate, so the CLI's
  `--insecure` defaults to **true** — a tool that cannot reach the device out of the box is
  useless. The **library** inverts this: `Config`'s zero value verifies certificates, because
  a library that is insecure at its zero value is a hazard. `InsecureSkipVerify` must be set
  deliberately.

### Authentication Flow

```
POST /rpc
{"jsonrpc":"2.0","id":1,"method":"challenge","params":{"username":"root"}}

<- {"jsonrpc":"2.0","id":1,"result":{"alg":6,"salt":"<salt>","nonce":"<nonce>"}}
```

`alg` selects the unix crypt algorithm:

| `alg` | Algorithm |
|-------|-----------|
| `1` | MD5 |
| `5` | SHA-256, 5000 rounds |
| `6` | SHA-512, 5000 rounds |

Firmware has been observed returning `alg` as both a JSON number and a JSON string, so it
must be decoded tolerantly. An unrecognized `alg` is a hard error, never a fallback to a
weaker algorithm.

The login digest is then computed in two steps:

1. `cipher = crypt(password, "$" + alg + "$" + salt + "$")` - standard unix crypt,
   equivalent to `openssl passwd -6 -salt <salt> <password>`. The full crypt output is
   used, including the `$6$<salt>$` prefix.
2. `hash = md5_hex(username + ":" + cipher + ":" + nonce)`

```
POST /rpc
{"jsonrpc":"2.0","id":2,"method":"login","params":{"username":"root","hash":"<hash>"}}

<- {"jsonrpc":"2.0","id":2,"result":{"sid":"<sid>","username":"root"}}
```

**The nonce is valid for roughly one second.** The transport MUST issue `challenge`
immediately before `login` and MUST NOT cache a challenge across calls. Because unix
crypt with 5000 rounds of SHA-512 is deliberately slow, the implementation should compute
the expensive `cipher` step first, then re-issue `challenge` to obtain a fresh nonce, then
compute the cheap MD5 and log in. Otherwise the crypt cost alone can expire the nonce.
The `cipher` value depends only on the password and salt, so it can be cached for the
lifetime of the client; the nonce and the resulting `hash` cannot.

### Authenticated Calls

Every operation is the `call` method, with the `sid`, a functional group, a method name,
and an argument object:

```
POST /rpc
{"jsonrpc":"2.0","id":3,"method":"call","params":["<sid>","<group>","<method>",{...}]}
```

### Session Lifetime

The `sid` expires after approximately 35 seconds of inactivity, and every successful call
resets the timer. Two mechanisms keep it alive:

1. A background goroutine issues `alive` at an interval safely under the expiry window
   (default 20 seconds).
2. Any call that fails with an access-denied error triggers exactly one transparent
   re-login and one retry. A second failure is returned to the caller.

Because sessions expire on a timescale shorter than a human typing a command, the CLI
utilities log in at the start of each invocation and do not attempt to persist sessions
between runs.

### API Discovery

GL.iNet's official 4.x API reference at `dev.gl-inet.com/router-4.x-api` is no longer
publicly reachable. The authentication flow above is verified, but **exact functional
group and method names must be captured from a live SFT1200 before the typed services are
frozen.**

Phase 0 of `docs/plan.md` is therefore a discovery phase:

1. The transport exposes a generic, untyped escape hatch:
   ```go
   func (c *Client) Call(ctx context.Context, group, method string, args, out any) error
   ```
2. Every request and response observed against the live device is recorded verbatim into
   `src/mock/fixtures/`.
3. `GL_INET_4X_API_DOCUMENTATION.md` is written from those recordings, not from
   third-party documentation.
4. Only then are the typed service interfaces written against the recorded fixtures.

The groups expected to matter for v1 scope, to be confirmed during discovery, cover LAN
and DHCP settings, static lease management, connected clients, and system information.
Do not hard-code a group or method name into a service until it appears in a recorded
fixture. The generic `Call` remains public after Phase 0, both as an escape hatch and
because it is how future discovery happens.

## The Reservation Model

This section originally asserted that one reservation provides both the DHCP binding **and**
the DNS record, atomically. **That was tested against a GL-SFT1200 on firmware 4.3.28 and is
false.** The corrected model follows; the original claim is recorded because a good deal of
the design was built on it.

### What a reservation actually is

The Opal runs dnsmasq, and GL.iNet's admin panel presents **LAN → Address Reservation** with
Name, MAC and IP fields. The natural reading is that the Name becomes a dnsmasq hostname.
It does not.

```
Reservation{Name: "myserver", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.10"}
```

...yields a device that always receives `192.168.8.10`. It does **not** yield a name that
resolves.

Two experiments established this:

1. A reservation for a MAC no device was using. The name resolved neither bare nor with the
   `.lan` suffix.
2. A reservation for a **present, actively leased** device under a *different* name. The new
   name still did not resolve, while that device's original lease hostname continued to
   resolve throughout.

### Where DNS names actually come from

The router answers from **DHCP leases**. The hostname in a lease is the one the client
announced about itself. So:

| | Source | Controlled by |
|---|--------|---------------|
| Address a device receives | static bind (reservation) | **gogl** |
| Name that resolves | DHCP lease hostname | **the client** |

### Consequences for this project

- **gogl reproduces both addresses and names, through two mechanisms.** Addresses come from
  static binds; names come from the router's host file via `dns.get_host` / `dns.set_host`.
  An earlier version of this document scoped the project to addresses only, on the belief that
  the firmware offered no way to write a name. It does, and `Hosts()` is that way.
- **A reservation's Name is only a label.** It identifies the entry in the admin panel and keys
  it in an exported host file, which is genuinely useful — but nothing resolves it. The
  resolvable name is a separate write.
- **The two can drift, so the CLI writes both.** One host declaration becomes a static bind and
  a host-file entry, and `import` diffs the two independently so a binding whose name went
  missing is repaired rather than skipped.
- **Keeping a name while dropping its binding is expressible, and refused.** A name resolving
  to an address the router no longer reserves is a trap. `rm` and `clear` remove both.
- **Names often work anyway, and that is not gogl's doing.** Most devices announce a sensible
  hostname over DHCP, so `nas.lan` frequently resolves. gogl neither causes that nor can
  arrange it for a device that stays silent.

### Name Validation

The name still gets strict DNS-label validation, for two reasons that survive the correction:

GL.iNet writes the name into its dnsmasq configuration file, and a known firmware defect lets
a character such as `"` corrupt that file, breaking DHCP for the entire router until it is
repaired by hand. And the ISC DHCP host-declaration format this project exchanges is keyed by
hostname, so a name that is not a legal DNS label cannot round-trip through it.

Validation rejects rather than escapes:

- Permitted characters: `[a-zA-Z0-9-]`, plus `.` as a label separator.
- Must not begin or end with `-` or `.`.
- Each label at most 63 characters; total at most 253.
- Must not be empty.

This lives in the library, not the CLI, so no consumer can bypass it.

**Note that `_` is rejected**, though some DHCP tooling permits it. A host file containing
`my_server` is **rejected** with the offending character named, rather than silently rewritten
to `my-server`. Renaming is a decision about what your hosts are called — not one to make
silently on the operator's behalf.

## Type Patterns

The original rule here was that permissive "decodes from either shape" types get added only
when a recorded fixture proves the need. **A fixture has since proved it**, so `src/types`
carries `FlexString`: `clients.online_time` is documented as a string and firmware 4.3.28
sends a number, which made every `clients.get_list` decode fail. `IntBool` exists for the
same class of reason — the firmware sends `enable: 1`, not `true`.

The rule stands as written; it simply turned out to be satisfiable. Do not add a new flexible
type on the strength of GL.iNet's API description alone, which has now been wrong four
separate times in ways only hardware revealed.

One conversion is required in the other direction. dnsmasq expresses lease time as a duration
string (`12h`, `1d`) or as bare seconds, while callers want a `time.Duration`. A `LeaseTime`
type handles both directions:

```go
// LeaseTime is a DHCP lease duration. It unmarshals from a dnsmasq duration
// string ("12h", "1d", "infinite") or a bare number of seconds, and marshals
// to the dnsmasq duration form.
type LeaseTime time.Duration
```

`infinite` maps to a sentinel value rather than an overflowing duration.

## Mock Server

The mock server must:

- Implement the full JSON-RPC envelope, including `challenge`, `login`, `alive`, and `call`.
- Reproduce the real challenge/response flow with a configurable `alg`, so that the crypt
  implementation is exercised for MD5, SHA-256, and SHA-512.
- Enforce nonce expiry, so that a client which caches a challenge fails against the mock
  exactly as it would against hardware.
- Enforce session expiry, so that keepalive and transparent re-login are exercised.
- Support all endpoints with realistic responses, loaded from fixtures recorded from a
  live device.
- Support error scenarios: bad password, expired nonce, expired session, malformed
  request, and JSON-RPC error objects.

## Reproducing a Network From a File

This is the reason `gogl` exists: a travel router's addressing and naming should come from a
file you commit, not from an hour in the admin panel. The primary case is a bench — an
isolated network for development or testing, where the addresses are the contract between the
devices under test and the harness that drives them.

### The Interchange Format Is the ISC DHCP File

Reservations exchange as ISC DHCP host declarations, which carry exactly the three fields an
Opal reservation needs, keyed by hostname:

```
host myserver {
    hardware ethernet aa:bb:cc:dd:ee:01;
    fixed-address 192.168.8.10;
}
```

`gogl lan reservations export` emits this and `import` parses it, under identical rules, so a
round trip is lossless. The format is diffable, version-controllable and hand-editable, which
is the whole argument for it — a bench description belongs in the repo whose tests depend on
those addresses.

Hostnames get strict DNS-label validation, so a name that is not a legal label is rejected
rather than silently converted. See [Name Validation](#name-validation).

For a whole network rather than just its addressing, `gogl profile export` captures the LAN,
pool, reservations, names, domain, wireless identity and radio tuning as one JSON file. See
[Profiles](#profiles).

### End-to-End Workflow

This example targets a bench on `192.168.4.0/24`.

```bash
# Capture, once, from a router configured the way you want it:
gogl lan reservations export > bench.hosts
git add bench.hosts && git commit -m "capture bench addressing"

# Rebuild, any time:
gogl lan dns set --domain bench.test
gogl lan set --ip 192.168.4.1 --mask 255.255.255.0 \
             --pool-start 192.168.4.100 --pool-end 192.168.4.149
# the router moves; reconnect at the new address
gogl -H 192.168.4.1 lan reservations import bench.hosts
```

Use `gogl lan show` beforehand to confirm the subnet and pool, and
`gogl lan reservations import --dry-run` to see exactly what would change before it changes.

The result is a router that hands out the same addresses to the same MAC addresses and
answers the same names, every time it is rebuilt.

### What a Host Declaration Becomes

| In the file | On the router |
|---------------------|----------------------|
| Each `host {}` block, address | One static bind: a MAC-to-IP binding |
| Each `host {}` block, hostname | One host-file entry, answering both bare and qualified |

Each declaration therefore becomes **two** writes, to two independent tables the firmware does
not join for you. `gogl lan reservations` performs both and reports them as one row, which is
the honest presentation: they are one intent, and either can be present without the other.

The network's *shape* — subnet and pool boundaries — is not in a host file, so set it with
`gogl lan set`, or capture the whole thing in a profile instead. Lease time and upstream DNS
servers stay whatever the router has; see the warning below for why that is what you want.

### The Router's Own Configuration

`gogl lan show` reports the subnet, pool and lease time. `gogl lan set` writes the first two,
via `--ip`, `--mask`, `--pool-start` and `--pool-end`. Lease time and upstream DNS servers are
read-only: no endpoint on this firmware sets them.

Two things to get right, because both cause failures that are hard to read from the symptom:

**Match the subnet to the addressing you intend to load.** A reservation at `192.168.4.10` is
meaningless on a router whose LAN is `192.168.8.0/24`. Set the LAN address first, with
`gogl lan set --ip 192.168.4.1 --mask 255.255.255.0 --pool-start ... --pool-end ...` or in the
admin panel under LAN → Router IP Address. The management session drops when the router moves,
and you reconnect at the new address; gogl treats a connection loss after the request as
success, because that is what a successful renumber looks like from this end.

`gogl lan set` refuses the change while any reservation exists unless forced, and
`gogl lan reservations import` refuses any address outside the current subnet rather than
writing something inert. Between them there is no order of operations that leaves a
reservation stranded, and neither failure is silent.

**Do not carry over the resolver from the network you modelled the bench on.** This is the
trap. If that network hands out a Pi-hole at `192.168.4.5`, and you point this router at the
same resolver, every client is told to use a DNS server that does not exist here. It presents
as "the internet is broken," not as "DNS is misconfigured" — and on an isolated bench with no
uplink, as nothing working for reasons that look unrelated. Leave the router advertising
**itself**: dnsmasq answers your reservation names locally and forwards everything else to
whatever resolver the WAN handed it. That works in a hotel, at a customer site, and on a bench
with no uplink at all. Public resolvers like `1.1.1.1` are safe to set if you want them.

### Future Work

- **`gogl lan reservations renumber`** - rewrite the network part of every address in an ISC
  DHCP file to a target subnet, preserving host parts, emitting a new file rather than editing
  in place. A pure text transformation with no device access, which makes it cheap to build and
  easy to trust. This is the alternative to re-IPing the router: instead of moving the router to
  the file's subnet, move the file to the router's.

Renumbering the router itself is no longer future work — `gogl lan set` does it. It stayed out
of the reservations commands deliberately, so that losing contact with the device is never a
side effect of importing a host file.

`renumber` is not built.

## Common CLI Conventions

### Connection Flags

Global, on every command.

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--host` | `-H` | `$GL_ROUTER_IP` | Router host address |
| `--port` | `-p` | `80` | Router port |
| `--https` | n/a | `false` | Use HTTPS instead of HTTP |
| `--insecure` | n/a | `true` | Skip TLS certificate verification; these devices ship self-signed certificates |
| `--router` | n/a | the config's `default` | Named router from the config file |
| `--output` | n/a | `text` | `text` or `json` |

The port default is 80, not 443, because that is what GL.iNet firmware serves. Passing
`--https` without changing `--port` is almost certainly wrong, and warns. Passing
`--insecure=false` without `--https` is an error: over plain HTTP there is no certificate to
verify, so the flag could only mislead.

`--insecure` defaulting to true is a CLI-only choice; the library's `Config` zero value
verifies. See [Key Technical Details](#key-technical-details).

There is no `--site` flag. GL.iNet routers have no sites.

### Environment Variables

| Variable | Required | Description |
|----------|----------|-------------|
| `GL_PASSWORD` | Yes | Router admin password |
| `GL_USERNAME` | No | Router admin username (default `root`) |
| `GL_ROUTER_IP` | No | Router host address (fallback if `-H` not given) |

### Output Conventions

- Data goes to stdout. Status, progress, and warnings go to stderr.
- Addresses sort numerically by IP (uint32 conversion), never lexically.
- MAC addresses are lowercase and colon-separated on output; case-insensitive on input.

## The `gogl` Command

One binary, `gogl <area> <action>`. Nine areas; eight act on the router and `config` acts on
the operator's machine.

This replaced four separate binaries — one each for reservations, the network report, the
client list and profiles — whose names were held in step with an external project's. That
naming constraint was dropped rather than allowed to decline a better command tree; four
binaries also meant four flag parsers, four help texts and four copies of the connection
flags. The ISC DHCP host-declaration format stays, on its own merits.

The complete reference -- every area, subcommand and flag -- is
[`docs/gogl-guide.md`](docs/gogl-guide.md). What follows is the specification: the rules the
tree must satisfy, not a restatement of its help text.

### Verb vocabulary

Held strictly, so a verb means one thing everywhere.

| Verb | Means |
|------|-------|
| `list` | many items |
| `show` | one thing, in detail |
| `set` | write fields on a thing |
| `add` / `rm` | collection membership |
| `clear` | empty a collection |
| `import` / `export` | file in, file out |

Forbidden: `get` (ambiguous with `show`), `delete` (use `rm`), `create` (use `add`), `update`
(use `set`).

This is why `lan dns set --domain X` is a field write and `lan dns add nas 192.168.8.13` is a
member add. A tree with two words for one action is a tree nobody can guess at.

### Area boundaries

The areas are not arbitrary groupings. Three of them follow seams the firmware itself draws:

**`radio` versus `wifi`** mirrors `wifi.set_config`'s two scopes: `device` for channel,
width, hardware mode and transmit power; `iface_name` for SSID, passphrase, encryption,
hidden and enabled. Following the API's own seam means the abstraction never fights the
transport.

**`--guest` is a flag on `lan` and `wifi`, not an area.** `lan.get_config_list` returns `lan`
and `guest` as interface variants, and guest SSIDs are further entries in `wifi`'s `ifaces`
array. One concept, two facets, no duplicated verbs.

**`clients` is its own area**, not part of `lan`. A station arrives over cable, 2.4GHz or
5GHz, and the useful view is all of them together. Describing a 5GHz station as a LAN thing
would be a lie.

`config` is the only area that does not touch the router. Everything else acts on the device;
that rule is what keeps `access` (the router's own security) distinct from `config` (which
router, which credential command).

### Band abstraction

`--band 2.4|5|6` resolves to a device and interface by reading the band each radio
**reports**. Never a static `radio0`/`radio1` map: nothing guarantees that ordering across
models, and a device with three radios would break a fixed map silently.

Two radios reporting one band is an explicit error asking for `--device`, not a guess.
`--device` and `--iface` override resolution for exactly that case.

Spellings an operator will reach for -- `2`, `2.4`, `2g`, `2.4GHz` -- all resolve. Requiring
the firmware's exact `2G` would be a needless trap.

### Output and exit codes

`--output text|json` on every read. Text output is for people: aligned columns, a header row,
and an empty result that explains itself rather than printing nothing. JSON is for machines:
stable shapes, `[]` rather than `null` for an empty list.

| Code | Meaning |
|------|---------|
| `0` | Success |
| `1` | Failure |
| `2` | Usage error |
| `3` | Refused by a guard |

Code 3 is the one that earns its place. A guard refusing a write because the state is wrong
is a different situation from a router that cannot be reached, and a script may reasonably
retry one and not the other.

### Secrets never reach the command line

No `--password` flag, ever. A secret on argv is visible to every user through `ps` and is
recorded in shell history.

Router password: `GL_PASSWORD`, then a `password_command` named in the config file, then a
prompt with echo off read from `/dev/tty`.

WiFi passphrases follow the same rule. `--passphrase` takes **no value** and prompts;
`--passphrase-command` covers scripted use. Passing a value is an error naming the mistake,
not a silent ignore -- the value is already in the operator's history by then, and saying so
is the only useful response.

This rule was violated once, by the v1 wireless utility's `--set-key 'value'`, in the same
binary that enforced it for the router password. The lesson recorded: a rule stated in one
place and not enforced in the adjacent one is not a rule.

### Configuration file

`${XDG_CONFIG_HOME:-~/.config}/gogl/config.toml`, with `GOGL_CONFIG` overriding the path.

A missing file is not an error. Requiring a config file to run one command would be hostile,
and the tool must work from flags and the environment alone.

Named routers with an optional default, plus `output`. Precedence: flag, then file, then
environment -- and a flag counts as given only if the operator typed it, so `--port 80` is
distinguishable from omitting `--port`. That distinction requires asking the flag parser what
changed rather than comparing against zero values.

Validation happens at load, not at connection time: a malformed file should be reported by
`gogl config show`, not discovered when a write is already in flight.

### Every command is one process, and one login

Each invocation performs a full challenge/response login: two `challenge` calls and a
`login`. The firmware counts those, and its brute-force protection will lock the account out.

OBSERVED 2026-07-30: a script making ~70 separate invocations locked the router out after the
first, and the remaining calls cascaded into failures that hid the cause. A denial on the
`challenge` call is now reported as probable rate limiting, because challenge carries only a
username and cannot mean a wrong password.

The consequence for tooling: prefer one `profile import` over many individual writes, and
pace scripted loops. A session cache shared across invocations would remove the problem
entirely and is not built.

## Commands

```bash
make test        # Run all tests against the in-process mock; no hardware
make lint        # Run linter
make build       # Build the module and bin/gogl
make coverage    # Generate coverage report
make install     # Install gogl to ~/.local/bin (override INSTALL_DIR)
make uninstall   # Remove it, including from the legacy ~/bin
make check-docs  # Verify documented flags and links against the built binary
make hil-test    # Hardware-in-the-loop test: writes to a real router, then restores it
make api-docs    # Regenerate docs/api/ from GL.iNet's API description
```

`UTILITIES := gogl`

One binary. It was four — one each for the client list, the network report, reservations and
profiles. Their logic became importable packages under `utilities/internal/` so the tests
survived the move; `utilities/gogl` is flag wiring over those packages and holds no logic of
its own.

`make hil-test` is deliberately outside `make test`. The latter runs against a mock with no
hardware, and mixing the two would make a green suite mean two different things.

## Reference Implementations

Study these for patterns. None is authoritative; the recorded fixtures from a live
SFT1200 are.

- `github.com/tomtana/python-glinet` - Python JSON-RPC client. The most complete public
  implementation of the challenge/login flow, including the `alg` map and the keepalive
  thread.
- `github.com/ryanrishi/glinet-client-go` - Go client for firmware 4.x, MIT licensed.
  Useful for the crypt digest implementation in Go specifically.
- `github.com/metril/ha-glinet` - Home Assistant integration for firmware 4.x. Useful for
  observed group and method names, to be confirmed against fixtures rather than trusted.
