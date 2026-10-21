<img src="images/gl-gopher-antennas-transparent.png" width="33%" alt="gogl logo">

# gogl - Go GL.iNet Travel Router Management Tool

Programmatic control of GL.iNet travel routers running firmware 4.x, targeting the
**GL-SFT1200 (Opal)**.

This is a single monolithic configuration tool AND the go module that enables the functionality.

Disclaimer: This works for me — that's the entire guarantee. Built with AI in the loop, so check your own biases before you love it or hate it on principle. Use at your own risk, fork freely, and don't @ me when it explodes. (But do drop me a note if it helps — pay it forward.)

> **Status: working against real hardware.** Verified on a GL-SFT1200 (Opal)
> reporting model `sft1200`, firmware **4.3.28**. Every endpoint gogl uses reads and
> writes the live device and is confirmed against it; the full API is documented in
> [`docs/api/`](docs/api/README.md).
>
---

## The problem

A travel router is the cheapest way to get a network you completely own. Plug it in
anywhere and you have your own subnet, your own DHCP, your own DNS, and nobody else's
traffic — in a hotel room, at a customer site, in a trade show booth, or in the corner of a
desk where a test bench lives.

That last one is the interesting case. An **isolated test network** is where you want your
robots, signage players, embedded boards and devices under test. You control the addressing,
you can capture every packet, you can power-cycle the whole network without filing a ticket,
and nothing you do to it leaks onto the corporate LAN.

The problem is everything after "plug it in". Setup is a web UI: set the LAN subnet, size the
DHCP pool, enter reservations one MAC address at a time, then the SSIDs, then the channels.
It is an hour of clicking, and you get to do it again when

- a firmware upgrade factory-resets the unit,
- someone borrows the router and hands it back reconfigured,
- a second engineer needs a bench identical to yours,
- or an experiment leaves the network in a state you cannot remember your way out of.

None of that is difficult work. It is *unrecorded* work, which means it cannot be repeated,
reviewed, or handed to anyone else.

## What gogl does about it

It turns the whole router into a file.

```mermaid
flowchart LR
    R1["bench router<br/>(configured by hand, once)"] -->|gogl profile export| F["bench.json<br/>committed to git"]
    F -->|gogl profile import| R2["the same router,<br/>after a factory reset"]
    F -->|gogl profile import| R3["a second bench,<br/>identical to the first"]
```

```bash
# Capture a working bench, once.
gogl profile export > bench.json
git add bench.json && git commit -m "capture the lab bench"

# Rebuild it, any time, on any GL.iNet 4.x router.
gogl --router bench profile import bench.json --wireless
```

A profile is the LAN address and mask, the DHCP pool, every reservation, every DNS name, the
domain, the wireless identity and the radio tuning. It is deliberately *not* a router image:
it omits the unit's own MAC, serial and uptime, which is what makes it apply to a **second**
router instead of only back to the one it came from.

The file is plain JSON, so it diffs, it reviews, and it lives next to the code that depends
on it. Your bench becomes reproducible from a commit rather than from memory.

## Addresses your tests can hardcode

Addressing is the part your devices and your test harness actually depend on. A fixture that
reaches `dut-3` at `192.168.8.23`, or a signage player configured to fetch from `nas`, stops
working the moment it lands on a network that disagrees.

So pin them, by name and by address:

```bash
gogl lan dns set --domain bench.test     # once per router
gogl lan reservations add --name dut-3 --mac 10:51:07:1f:8d:1c --ip 192.168.8.23
```

Or in bulk, from a file checked in beside the tests:

```bash
gogl lan reservations import bench.hosts
```

The file is ISC DHCP host-declaration format — plain text, and readable without a tool:

```
host dut-3 {
    hardware ethernet 10:51:07:1f:8d:1c;
    fixed-address 192.168.8.23;
}
```

`import` is idempotent. Run it twice and the second run reports everything skipped and
leaves the router byte-identical, which is what makes a host file usable as a checked-in
description of a network rather than a one-shot script.

Addresses and names come from two different mechanisms on this firmware. `gogl` writes both
in one command, but the split explains a couple of things that would otherwise look odd — see
[Addresses and names are separate](#addresses-and-names-are-separate).

### Addresses and names are separate

The obvious assumption here is wrong, and it took hardware testing to find out.

GL.iNet's admin panel shows **LAN → Address Reservation** with Name, MAC and IP fields, so it
reads as though the Name becomes a DNS name. **It does not.** Tested twice on 4.3.28: a
reservation for an absent MAC never resolved, and a reservation for a *present, actively
leased* device under a new name still didn't resolve — while that device's own lease hostname
kept resolving the whole time.

So there are three separate things:

| What | Mechanism | Who controls it |
|------|-----------|-----------------|
| The address a device receives | static bind (reservation) | **gogl** — `lan reservations` |
| A name you choose | the router's **host file** | **gogl** — `lan dns` |
| A name a device announces about itself | its DHCP lease hostname | the client |

Names come from `dns.set_host`, which writes the router's `hosts(5)` file — dnsmasq answers
from it, verified by writing an entry and resolving both the bare name and an arbitrary FQDN
against the router.

gogl owns a delimited block in that file and never touches anything outside it. The file also
holds the loopback and IPv6 lines the router itself needs.

**`gogl lan reservations` writes both, and you do not have to think about it.** One host
declaration becomes a static bind *and* a host-file entry; `import`, `add`, `rm` and `clear`
each keep the two in step. What the split means in practice is only this:

- Names are batched. `dns.set_host` takes the whole file as one string, so an `import` run
  makes one write for every name rather than one write per name.
- The two can drift if something else edits them — a hand edit in the admin panel, or a run
  that died between the two writes. A binding with no name is repaired by the next `import`,
  which diffs the names independently rather than skipping an entry whose binding already
  matches. `gogl lan reservations list` and `gogl lan dns show` report what is in each.

### Set the domain first

The firmware exposes no way to set dnsmasq's domain — no endpoint among all 313 has a
`domain` field, and `/ubus`, which could do it, returns 404 on this model. So gogl carries its
own domain, stored inside its block in the host file, and writes fully-qualified names. Any
suffix works, because host-file entries are literal name-to-address mappings.

```bash
gogl lan dns set --domain bench.test
```

`.test` is the suffix to reach for on a bench: RFC 2606 reserves it for exactly this, so it
can never collide with a real domain. Avoid `.local` — it belongs to mDNS, and Avahi on your
Linux clients will fight dnsmasq for it.

**Reservation writes are refused until you do this.** A reservation alone produces an address
with no name and nothing to signal that was unintended; making the domain a precondition
forces the pairing to be deliberate.

Because the two are separate objects, "keep the name but drop the reservation" is
expressible. `gogl` does not offer it: a name that resolves to an address the router no
longer reserves is a trap rather than a feature. `rm` and `clear` remove both.

---

> **Full command reference:** [`docs/gogl-guide.md`](docs/gogl-guide.md) documents every
> area, subcommand and flag, with recipes and troubleshooting.

# Part 1: The `gogl` command

One binary. `gogl <area> <action>`, the shape git, docker and kubectl converged on.

**[`docs/gogl-guide.md`](docs/gogl-guide.md) is the complete reference** — every area,
every subcommand, every flag, with recipes and troubleshooting. This section is the tour.

## What gogl manages

Nine areas. Eight act on the router; `config` acts on your machine.

| Area | Reads | Writes |
|---|---|---|
| `lan` | address, netmask, DHCP pool, lease time, resolvers, dynamic leases | address, netmask, pool |
| `lan reservations` | static MAC-to-IP bindings, in ISC DHCP format | add, remove, import, export, clear |
| `lan dns` | the DNS domain and every managed name | domain, names, clear |
| `radio` | per-radio channel, width, hardware mode, power, and what each radio accepts | all four |
| `wifi` | per-interface SSID, encryption, hidden and enabled state | SSID, passphrase, encryption, hidden, enabled |
| `clients` | connected stations with IEEE OUI vendor lookup, online state, session age | nothing |
| `profile` | a whole network as JSON | applies one back, to this router or another |
| `system` | model, firmware, endpoint | nothing |
| `config` | gogl's own settings and named routers | writes a starter file |

Between them that covers everything you would otherwise click through on a fresh unit.
Some of it needs explaining:

**Wireless, in two halves.** `radio` and `wifi` are not an arbitrary split — they mirror how
the firmware scopes writes. `wifi.set_config` takes `device` for channel, width, hardware
mode and power, and `iface_name` for SSID, passphrase, encryption, hidden and enabled. The
areas follow the seam, so the abstraction never fights the transport.

**Band abstraction.** `--band 2.4|5|6` resolves to a device by reading the band each radio
*reports*, never a static `radio0`/`radio1` map. Nothing guarantees that ordering across
models, and a three-radio device would break a fixed map silently.

**Whole-network profiles.** `gogl profile export` captures the LAN, pool, reservations, DNS
names, domain, wireless identity and radio tuning into one JSON file; `import` applies it
back. Not a router image — it deliberately omits the unit's own MAC, serial and uptime,
which is what makes the file usable on a *second* router.

**Client state.** `clients list` reports online state, session age and vendor, and hides
stations the router merely remembers by default. That default exists because the list
carries history: a router renumbered from `192.168.2.0/24` to `192.168.8.0/24` was still
reporting a station at `192.168.2.138`.

**Named routers and a config file.** `--router bench` selects a TOML block. Passwords are
never in the file: environment, then a command the file names, then a prompt.

**Machine-readable output.** `--output json` on every read.

Deliberately absent, and why: lease time and upstream DNS servers are readable but have no
verified write endpoint; WAN, VLANs, firewall and VPN have no endpoint this project has
exercised against hardware. Building them from GL.iNet's API description alone has been
wrong four times — see [Limitations](#limitations).

## Prerequisites

- A GL.iNet router on **firmware 4.x**. GL.iNet replaced its REST API with JSON-RPC at
  firmware 4.0, and `gogl` speaks only JSON-RPC. Firmware 3.x will not work.
- The admin password from the router's first-run setup. There is no default.
- Go 1.22 or later, to build.
- Network reachability to the router's LAN address.

Verified against a GL-SFT1200 (Opal) reporting firmware **4.3.28**. Other GL.iNet 4.x models
share the API, but this one is a reduced build. Nine documented endpoints are absent on it:
three are marked so in [`docs/api/`](docs/api/README.md), where each method is tagged verified,
absent or untested, and six more come from GL.iNet's original public reference and are recorded
in [`GL_INET_4X_API_DOCUMENTATION.md`](GL_INET_4X_API_DOCUMENTATION.md).

## Quick start

**Install with Homebrew.** Releases are published to a Homebrew tap. The `brew trust`
step is required because Homebrew refuses to load formulae from a tap you have not
trusted:

```bash
brew tap gherlein/tap
brew trust gherlein/tap
brew install gogl
```

The Homebrew install is tested only on Linux.

**Quick install for Go users.** To install just the `gogl` binary, with no checkout, run:

```bash
go install github.com/gherlein/gogl/utilities/gogl@latest
```

It lands in your Go binary directory (`$GOBIN`, or `~/go/bin` by default; put it on your
`PATH`). Building from a checkout (below) is the way to get the `make` targets and run
the tests.

**Build from source.**

```bash
git clone https://github.com/gherlein/gogl
cd gogl
make build            # builds bin/gogl
make install          # installs to ~/.local/bin
```

Set a password, then look at the router:

```bash
read -rsp 'router password: ' GL_PASSWORD; export GL_PASSWORD
gogl -H 192.168.8.1 lan show
```

Save yourself the `-H` and the export:

```bash
gogl config init          # writes ~/.config/gogl/config.toml
$EDITOR ~/.config/gogl/config.toml
gogl config show
```

```toml
default = "bench"

[routers.bench]
host             = "192.168.8.1"
password_command = "pass show routers/bench"

[routers.travel]
host             = "192.168.4.1"
password_command = "pass show routers/travel"
```

Then `gogl lan show`, `gogl clients list`, `gogl radio list` — and `--router travel` to
reach the other one.

Shell completion is free:

```bash
gogl completion bash > /etc/bash_completion.d/gogl
```

## Configuration and secrets

`${XDG_CONFIG_HOME:-~/.config}/gogl/config.toml` holds everything except secrets. Named
routers, a default, an output format, and optionally a command that prints each router's
password.

**There is no `--password` flag and never will be.** A secret on the command line is visible
to every user through `ps` and lands in your shell history. Resolution order: `GL_PASSWORD`,
then `password_command`, then a prompt with echo off.

WiFi passphrases follow the same rule: `gogl wifi set --passphrase` takes no value and
prompts; `--passphrase-command` reads it from a command for scripts. Passing a value is an
error rather than silently ignored.

Full detail: [Configuration file](docs/gogl-guide.md#configuration-file) and
[Passwords and secrets](docs/gogl-guide.md#passwords-and-secrets).

## Three guards

Each refuses a well-formed request because the state is wrong, and each exits **3** so a
script can tell "I was blocked" from "it broke".

**A reservation write needs a domain first.** Otherwise you get addresses with no names and
nothing signalling that was accidental.

**Moving the LAN subnet is refused while reservations exist**, unless `--force`. The firmware
silently rewrites every reservation into the new subnet — usually what you want, entirely
unannounced. Changing only the pool is never guarded, since nothing moves.

**Wireless writes are refused over a wireless session.** Applying one would drop the session
with no address to reconnect at. `--yes` waives the prompt, never this guard.

Details and the reasoning: [The three guards](docs/gogl-guide.md#the-three-guards).

## Common tasks

### Stand up a test bench from a file

Order matters: the domain is a precondition for reservations, and moving the subnet drops
your session.

```bash
gogl lan dns set --domain bench.test
gogl lan set --ip 192.168.4.1 --mask 255.255.255.0 \
             --pool-start 192.168.4.100 --pool-end 192.168.4.149
# the router moves; reconnect at the new address
gogl -H 192.168.4.1 lan reservations import bench.hosts --dry-run
gogl -H 192.168.4.1 lan reservations import bench.hosts
```

Keep `bench.hosts` in the repo whose tests depend on those addresses. A change to the bench
then arrives as a diff in a pull request like any other change.

### Capture a bench, and rebuild it after a reset

```bash
gogl profile export > bench.json          # add --with-keys to include WiFi passphrases
git add bench.json && git commit -m "capture the lab bench"

# after a factory reset, or on a replacement unit:
gogl profile import bench.json --wireless
```

If the profile's subnet differs from the reset router's default, the import applies the
addressing and then stops — the router changes address mid-write, so nothing after that is
reachable from the same session. It prints the command to resume at the new address.

### Clone one bench onto a second router

```bash
gogl --router bench1 profile export --with-keys > bench.json
gogl --router bench2 profile import bench.json --wireless
```

Two engineers, two routers, one file: the benches agree, and disagreements show up as a
diff instead of as a bug that only reproduces on one desk.

### Set up the wireless for a bench or a site

Over ethernet — wireless writes are refused otherwise.

```bash
gogl radio list                                   # interface names and what each radio accepts
gogl wifi set --band 2.4 --ssid bench-2g
gogl wifi set --band 5   --ssid bench-5g
gogl wifi set --band 5   --passphrase             # prompts, echo off
gogl radio set --band 5  --channel 149            # move off a congested channel
```

### Find what is worth reserving

```bash
gogl clients list --reserved     # who has a reservation and who does not
gogl lan leases                  # what the router is handing out dynamically
gogl clients vendor b4:0e:cf:2a:85:6f    # offline OUI lookup, no router needed
```

### Manage names

```bash
gogl lan dns show
gogl lan dns set --domain bench.test
gogl lan dns add nas 192.168.8.13
gogl lan dns rm nas
```

More: [Recipes](docs/gogl-guide.md#recipes).

## Before you import: two things to get right

### Match the subnet

A reservation at `192.168.4.10` is meaningless on a router whose LAN is `192.168.8.0/24`.
Either move the router:

```bash
gogl lan reservations clear                       # or pass --force below
gogl lan set --ip 192.168.4.1 --mask 255.255.255.0 \
             --pool-start 192.168.4.100 --pool-end 192.168.4.149
```

or renumber the file. `gogl lan reservations import` refuses any address outside the current
subnet rather than writing something inert, and names the mismatch.

The session drops when the router moves. That is inherent, not a defect: gogl treats a lost
connection as success and prints the address to reconnect at.

### Do not copy the resolver from the network you copied the addressing from

This is the trap. If the network you modelled the bench on hands out a Pi-hole at
`192.168.4.5`, and you point the travel router at that same resolver, every client is told
to use a DNS server that does not exist on this network. It presents as "the internet is
broken", not as "DNS is misconfigured" — and on an isolated bench with no uplink at all, it
presents as nothing working for reasons that look unrelated.

Leave the router advertising **itself**: dnsmasq answers your reservation names locally and
forwards everything else to whatever resolver the WAN handed it. That works in a hotel, at a
customer site, and on a bench with no uplink. Public resolvers like `1.1.1.1` are safe too,
since they work anywhere there is an uplink.

gogl cannot write upstream DNS servers, so this is a warning about the admin panel rather
than about a command.

## Limitations

Deliberate, and documented rather than worked around.

| Not supported | Why |
|---|---|
| WAN configuration | 2 of 40 methods verified across `netmode`, `cable`, `repeater`, `tethering`, `modem`. Designed, not built: see [`TODO.md`](TODO.md) |
| Device access control | `acl` has 8 methods, none verified. Same reason |
| Wireless writes over a wireless session | Refused: applying one severs the session with no address to reconnect at |
| Lease time, upstream DNS servers | Readable; no verified write endpoint |
| Setting dnsmasq's `domain` / `local` / `expandhosts` | No endpoint exposes them, and `POST /ubus` is 404 on this model. gogl carries its own domain and writes FQDNs instead, which works for any suffix |
| DNS names via reservations | A reservation creates no DNS record on this firmware. Use `lan dns` |
| DFS channels | `wifi.get_config` offers nine 5GHz channels; the driver supports twenty-five. The missing sixteen are the DFS ones, so `dfs_support: false` reports GL.iNet's policy, not the radio's capability |
| Monitor mode, packet injection | Not reachable over the API, and not usable on this hardware: the driver advertises monitor mode, refuses an interface while the APs are up, and hung the device when one was brought up with them down |
| VLANs | The Opal exposes no VLAN configuration through its API |
| VPN, firewall, port forwarding | Out of scope |
| Firmware 3.x | Different API entirely: REST, not JSON-RPC |
| Generic OpenWrt | GL.iNet 4.x only, permanently. See [Scope](VISION.md#scope) |
| Hostnames containing `_` | Not a legal DNS label character. Rejected rather than silently rewritten to `-`, so an imported file means what it says |
| `clients` with no cached OUI data and no internet | Hard failure rather than a table of blanks that looks like every device is from an unknown vendor |

One tool would close a real gap and is not built:

- **`gogl lan reservations renumber`** — rewrite the network part of every address in a host
  file to a target subnet, preserving host parts. The alternative to re-IPing the router. A
  pure text transformation, so cheap to trust.

# Part 2: Using gogl in your Go program

```bash
go get github.com/gherlein/gogl
```

The library lives under `src/`, so the import path carries that suffix while the package
name is `gogl`:

```go
import (
    gogl "github.com/gherlein/gogl/src"
    "github.com/gherlein/gogl/src/types"
)
```

## Connecting

```go
client, err := gogl.New(gogl.Config{
    Host:     "192.168.8.1",
    Password: os.Getenv("GL_PASSWORD"),
})
if err != nil {
    log.Fatal(err)
}
defer client.Close()
```

`New` does not contact the router — the first call authenticates lazily, so constructing a
client is cheap and cannot fail on a network error. The client is safe for concurrent use.

`Config`'s zero value is safe: port 80, user `root`, TLS verification **on**. Note that the
library defaults to verifying certificates while the CLIs default to accepting self-signed
ones. That inversion is intentional: a library that is insecure at its zero value is a
hazard, and a CLI that cannot reach a self-signed device out of the box is useless. Set
`InsecureSkipVerify` explicitly if you need it.

## Services

```go
client.Network()      // LAN and DHCP configuration        (read/write)
client.Reservations() // static MAC-to-IP bindings         (read/write)
client.Hosts()        // DNS names via the host file       (read/write)
client.Wireless()     // radios and wireless interfaces    (read/write)
client.Clients()      // connected stations                (read-only)
client.System()       // model, firmware                   (read-only)
```

No method takes a `site` parameter.

Every service is an interface, so a consumer can substitute a fake without the mock server.
The interfaces are semantic rather than wire-shaped — `Network().Set` rather than
`Call("lan", "set_config", …)` — which is what lets the guards live in the library instead of
in each caller.

### Reading the network

```go
network, err := client.Network().Get(ctx)
if err != nil {
    log.Fatal(err)
}

subnet, err := network.Subnet()          // 192.168.8.0/24
fmt.Println(subnet, network.PoolSize())  // pool size in addresses
fmt.Println(network.DHCPLease)           // "12h"

inside, err := network.Contains(net.ParseIP("192.168.8.13"))
pooled, err := network.InDHCPPool(net.ParseIP("192.168.8.150"))
```

`InDHCPPool` is informational. dnsmasq honors a static lease whose address sits inside the
dynamic range and excludes it from dynamic allocation, so it is untidy rather than broken.
It *would* be a genuine conflict under ISC dhcpd, which is where the opposite intuition
comes from.

### Managing reservations

```go
reservations := client.Reservations()

created, err := reservations.Create(ctx, &types.Reservation{
    Name: "nas",
    MAC:  "AA:BB:CC:DD:EE:01",   // normalized to lowercase on write
    IP:   "192.168.8.13",
})

all, err := reservations.List(ctx)
one, err := reservations.GetByName(ctx, "nas")
one, err = reservations.GetByMAC(ctx, "aa:bb:cc:dd:ee:01")
many, err := reservations.GetByIP(ctx, "192.168.8.13")  // slice: duplicates are possible

_, err = reservations.Update(ctx, &types.Reservation{
    Name: "nas", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.20",
})

err = reservations.Delete(ctx, "aa:bb:cc:dd:ee:01")
```

MAC is the identity for `Update` and `Delete`: it is the only thing a client cannot change
about itself, and it is what dnsmasq keys the lease on.

### DNS names, and the domain

```go
hosts := client.Hosts()

// The domain is a precondition for writing reservations.
if err := hosts.SetDomain(ctx, "bench.test"); err != nil {
    log.Fatal(err)
}

// Creates "nas" and "nas.bench.test", both resolving to the address.
if err := hosts.Set(ctx, "nas", "192.168.8.13"); err != nil {
    log.Fatal(err)
}

file, err := hosts.Get(ctx)          // parsed, managed and unmanaged parts
ip, ok := file.Lookup("nas")         // either spelling works
err = hosts.Remove(ctx, "nas")
err = hosts.Clear(ctx)               // drops managed entries, keeps the domain
```

Everything outside gogl's block in the host file is preserved, including the loopback and IPv6
lines the router relies on.

### Writing the network

```go
// Moving the subnet. Refused while reservations exist; pass services.WriteForced to
// accept the firmware rewriting them into the new subnet.
err := client.Network().Set(ctx, &types.Network{
    Interface: types.InterfaceLAN,
    LANIP:     "192.168.2.1",
    Netmask:   "255.255.255.0",
    DHCPStart: "192.168.2.100",
    DHCPStop:  "192.168.2.149",
}, services.WriteGuarded)
```

The third argument is a `services.WriteMode`, named rather than a bool so the call site says
what it does: `WriteGuarded` refuses a subnet move while reservations exist, `WriteForced`
proceeds. Forcing waives the guard, never the validation.

A pool-only change is never guarded, because nothing moves — but the firmware takes all four
fields in one call, so read the current address and mask and pass them through:

```go
current, err := client.Network().Get(ctx)
if err != nil {
    return err
}
current.DHCPStart, current.DHCPStop = "192.168.2.50", "192.168.2.150"
err = client.Network().Set(ctx, current, services.WriteGuarded)
```

The pool is checked against the subnet first: the firmware accepts a pool outside it and then
silently hands out nothing.

When the subnet *does* move, expect the call to fail with a lost connection *on success* — the
router changes address mid-request. The CLI treats that as expected and tells you where to
reconnect. A pool-only change has no such problem.

### Wireless

Two scopes, mirroring how the firmware scopes writes.

```go
// Per radio: channel, width, hardware mode, transmit power.
radios, err := client.Wireless().Radios(ctx)
for _, r := range radios {
    fmt.Println(r.Band, r.Device, r.Channel, r.HTMode)
    fmt.Println(r.ChannelNumbers())    // what this radio actually offers
    fmt.Println(r.HTModeOptions())     // settable widths for its current hwmode
}

channel := 149
err = client.Wireless().SetRadio(ctx, "radio1", types.RadioChanges{Channel: &channel})

// Per interface: SSID, passphrase, encryption, hidden, enabled.
ssid := "lab-5g"
err = client.Wireless().SetInterface(ctx, "default_radio1", types.InterfaceChanges{
    SSID: &ssid,
})
```

Pointer fields are the partial-update contract: a nil field is not written, so setting an
SSID leaves the passphrase alone. `Empty()` reports whether there is anything to send.

Both setters return `ErrWirelessSession` when the calling session arrives over WiFi.
`SessionInterface` answers the same question directly:

```go
switch path, err := client.Wireless().SessionInterface(ctx); path {
case "cable":
    // safe to write
case "":
    // off-LAN: no radio here carries this session, also safe
default:
    // "2.4G" or "5G": a write would sever this session
}
```

Values are validated against what each radio advertises, so an unsupported channel is
refused with the available ones named rather than answered by a bare firmware error.

### Errors

```go
switch {
case errors.Is(err, gogl.ErrDomainNotSet):      // set a domain before writing reservations
case errors.Is(err, gogl.ErrReservationsExist): // clear them, or pass services.WriteForced
case errors.Is(err, gogl.ErrNotFound):
case errors.Is(err, gogl.ErrConflict):        // MAC already reserved
case errors.Is(err, gogl.ErrInvalidName):     // name would be unsafe in dnsmasq config
case errors.Is(err, gogl.ErrInvalidMAC):
case errors.Is(err, gogl.ErrInvalidIP):
case errors.Is(err, gogl.ErrLoginRateLimited):   // locked out; the message carries the wait
case errors.Is(err, gogl.ErrOutsideSubnet):
case errors.Is(err, gogl.ErrUnauthorized):
case errors.Is(err, gogl.ErrWirelessSession):    // wireless write over a wireless session
case errors.Is(err, gogl.ErrUnwritableContent):  // host file holds a character set_host rejects
case errors.Is(err, gogl.ErrInvalidInput):       // SSID, passphrase, channel, bandwidth
}

// ErrSessionExpired, ErrNonceExpired, ErrUnsupportedAlgorithm and
// ErrUnsupportedHashMethod are exported for inspection but handled inside the
// transport: a caller normally never sees them.

var rpcErr *gogl.RPCError
if errors.As(err, &rpcErr) {
    log.Printf("%s.%s failed with %d: %s", rpcErr.Group, rpcErr.Method, rpcErr.Code, rpcErr.Message)
}
```

### Name validation happens in the library

```go
_, err := reservations.Create(ctx, &types.Reservation{
    Name: `my"host`, MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13",
})
// err is ErrInvalidName; nothing was sent to the router
```

Two reasons, and neither is "the router will serve it as DNS" — it will not. GL.iNet writes
reservation names into its dnsmasq config file, and a known firmware defect lets a quote
character corrupt that file, taking down DHCP for the whole router until it is repaired by
hand. And the ISC DHCP host format this project exchanges is keyed by hostname, so a name
that is not a legal DNS label cannot round-trip through it.

Names are validated on the type, before any write, and rejected rather than escaped. No
consumer of the library can bypass it.

### Reaching an endpoint that isn't modelled

```go
var out map[string]any
err := client.Call(ctx, "some_group", "some_method", nil, &out)
```

`Call` is the generic escape hatch. It is also how the router's API was mapped in the first
place, since GL.iNet's official 4.x reference is no longer public.

## Project structure

```
gogl/
├── src/                    # Library (package gogl)
│   ├── client.go           # Client, Config, service accessors
│   ├── errors.go           # Sentinels, RPCError
│   ├── types/              # Reservation, Network, Client, HostFile, Wireless, LeaseTime
│   ├── services/           # Network, Reservations, Hosts, Wireless, Clients, System
│   ├── auth/               # Challenge/response login, unix crypt
│   ├── transport/          # JSON-RPC envelope, session, keepalive, retry
│   ├── mock/               # In-process router for tests
│   └── ipmath/             # IPv4 arithmetic
├── utilities/
│   ├── gogl/               # the single binary: cobra command tree
│   └── internal/
│       ├── config/         # TOML, XDG paths, password resolution
│       ├── conn/           # connection flags, secrets, version stamp
│       ├── reservations/   # ISC DHCP parse/format, import/export, add/rm/clear
│       ├── netcfg/         # network report, LAN write, wireless writes, band resolution
│       ├── clients/        # station list, IEEE OUI database
│       └── profile/        # whole-network capture and apply
├── examples/               # basic, list, reservations
├── discovery/              # probes: shape, hostfile, auth diagnosis
├── scripts/                # check-docs, api-docs generation, hardware test
└── docs/                   # gogl-guide.md, DESIGN.md, DESIGN-V2.md, api/
```

The four former utilities became importable packages under `utilities/internal/`, so their
logic and tests survived the move to one command tree. `utilities/gogl` is flag wiring over
those packages and holds no logic of its own.

## Build targets

```bash
make build      # module + utilities into bin/
make test       # go test -v -race -cover ./...
make lint       # golangci-lint
make coverage   # HTML coverage report
make examples   # build the examples
make install    # gogl into ~/.local/bin (override INSTALL_DIR)
make uninstall  # remove it, including from the legacy ~/bin
make check-docs # verify documented flags and links against the built binary
make hil-test   # hardware-in-the-loop test: writes to a real router, then restores it
make all        # lint, test, build
```

## How authentication works

Worth knowing if you are debugging a login, because it is the least obvious part of the
module.

Firmware 4.x uses challenge/response at `POST /rpc`. The password is never transmitted:

1. `challenge` returns a crypt algorithm, a salt, a nonce, and a `hash-method`.
2. The password is hashed with unix `crypt(3)` under that salt.
3. `H(username:cipher:nonce)` produces the login digest, where `H` is whatever
   `hash-method` named.
4. `login` returns a session id, passed as the first parameter of every later call.

**Step 3 is where every other client library gets it wrong.** `hash-method` selects the
login digest, separately from `alg` which selects the crypt on the password. Firmware 4.3.28
on the SFT1200 reports `sha256`; `python-glinet`, `glinet-client-go` and `ha-glinet` all
hardcode MD5 because they predate the field, and against 4.3.28 they fail with
`-32000 Access denied` — indistinguishable from a wrong password. gogl reads the field,
treats its absence as MD5 for older firmware, and refuses to guess if it sees a value it
does not implement.

The other subtlety: **the nonce lives about one second, and step 2 is deliberately slow.**
So `gogl` calls `challenge` *twice* — once to learn the salt, then again after the crypt
completes, to get a nonce that is still alive when the cheap digest runs over it. A
single-challenge implementation races against its own hashing cost and fails
intermittently, which is far worse than failing consistently.

The session then idles out after roughly 35 seconds. A background keepalive holds it open,
and any call rejected for a stale session triggers exactly one transparent re-login and
retry. Bounded at one attempt on purpose: an unbounded loop against a wrong password is a
login flood aimed at a very small SoC.

## Documentation

- [`VISION.md`](VISION.md) — requirements and per-tool specifications
- [`docs/DESIGN.md`](docs/DESIGN.md) — architecture, diagrams, interfaces, decision log
- [`docs/plan.md`](docs/plan.md) — phased implementation plan (continues in `plan-part2.md` through `plan-part4.md`)
- [`GL_INET_4X_API_DOCUMENTATION.md`](GL_INET_4X_API_DOCUMENTATION.md) — authentication,
  error codes, and every endpoint verified against real hardware
- [`docs/api/`](docs/api/README.md) — full reference for all 43 groups and 313 methods,
  generated from GL.iNet's own API description

## License

MIT

---

The Go gopher was designed by [Renée French](https://reneefrench.blogspot.com/) and is licensed under [CC BY 3.0](https://creativecommons.org/licenses/by/3.0/).
