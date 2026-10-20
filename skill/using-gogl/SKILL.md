---
name: using-gogl
description: >-
  Use when driving the gogl CLI to manage a GL.iNet 4.x travel router (verified
  on the GL-SFT1200 "Opal", firmware 4.3.x) over its JSON-RPC API. Triggers on -
  gogl profile export/import, capturing or rebuilding a bench/lab network, DHCP
  reservations in ISC DHCP format, LAN subnet or DHCP pool changes, DNS names on
  the router, WiFi SSID/passphrase/channel/power changes, listing connected
  clients, MAC vendor lookup, the gogl config.toml and named routers, GL_PASSWORD
  / password_command, "wired session required", GL.iNet travel router automation.
---

# Using gogl

`gogl` manages GL.iNet 4.x travel routers over their **JSON-RPC HTTP API**. There
is **no SSH and no shell** - every change goes through an API call, so the blast
radius of a mistake is bounded by what the API can express. Its purpose is a
**reproducible network**: capture the addressing, reservations, DNS names and
wireless identity a site depends on, commit that to git, and recreate it on any
GL.iNet 4.x router.

The headline workflow:

```sh
gogl --router bench profile export > bench.json   # capture, once
git add bench.json && git commit -m "lab bench"
gogl --router bench profile import bench.json --wireless   # rebuild, any time
```

## Connecting to a router

Every command that talks to a router needs a host and a password. Supply them by
global flags, by environment, or (best) by a config file with named routers.

### Global flags (accepted on every command)

| Flag | Default | Meaning |
| --- | --- | --- |
| `-H, --host <addr>` | - | Router address (e.g. `192.168.8.1`) |
| `-p, --port <int>` | `80` | Router port (GL.iNet firmware serves plain HTTP on 80, **not** 443) |
| `--https` | off | Use HTTPS instead of HTTP |
| `--insecure` | **true** | Skip TLS cert verification (these devices ship self-signed certs) |
| `--router <name>` | - | Use a named router from the config file |
| `--output <text\|json>` | `text` | Output format; `json` for scripting |

### Authentication (no password flag by design)

A password is **never** passed on the command line (it would be visible in `ps`
and land in shell history). It is resolved in this order:

1. `GL_PASSWORD` environment variable,
2. the `password_command` named in the config file (its **first line** of stdout
   is the password; runs through `sh -c`, so a pipeline works),
3. an interactive prompt on the terminal (echo off).

Other env vars (carried over for existing scripts): `GL_USERNAME`,
`GL_ROUTER_IP`.

```sh
export GL_PASSWORD='...'            # non-interactive
gogl --host 192.168.8.1 system info
```

## The config file

Location: `$XDG_CONFIG_HOME/gogl/config.toml` (default
`~/.config/gogl/config.toml`); override the whole path with `GOGL_CONFIG`. A
missing file is fine - `gogl` runs from flags and env alone. Create a starter
with `gogl config init` (add `--force` to overwrite), then edit it. The file
below is illustrative (both routers shown filled in); the generated starter
comments out the extra router and most optional keys.

```toml
# Secrets never belong here.
default = "home"          # router used when --router is omitted (optional if only one)
output  = "text"          # "text" or "json"; overridden by --output

[routers.home]
host     = "192.168.8.1"
# port   = 80
# username = "root"
# https  = false
# insecure = false
# domain = "lab.example"               # DNS suffix; applied only by `gogl lan dns set`
# password_command = "pass show routers/home"   # prints the password on line 1

[routers.travel]
host = "192.168.8.1"
```

Router resolution: `--router <name>` wins; else `default`; else, if exactly one
router is defined, that one. With several routers and no default, `gogl` errors
and asks for `--router`.

Inspect it:

```sh
gogl config show        # where the file is and what it resolves to
gogl config routers     # list configured routers
```

## Command map

```
gogl
  system info                         model, firmware version, uptime
  clients list [-r|-w|-e]             connected stations (+ OUI vendor lookup)
  clients vendor <mac>                offline MAC -> manufacturer (no router contact)
  lan show [--show-key]               LAN address, pool, reservation counts, radios
  lan leases                          current dynamic DHCP leases (what to reserve)
  lan set ...                         write LAN address and/or DHCP pool
  lan reservations (alias: res)
      list                            reservations on the router
      add <flags | ISC decl>          add one host
      rm --name|--mac|--ip            remove one host (+ its DNS name)
      import [file]                   import ISC host declarations (idempotent)
      export                          dump reservations as ISC declarations
      clear                           remove every reservation and managed DNS name
  lan dns
      show                            domain + managed DNS names
      set --domain <suffix>           set DNS domain (required before reservations)
      add <name> <ip>                 point a name at an address
      rm <name>                       remove a name (bare or qualified)
      clear                           remove managed names, keep the domain
  wifi list|show|set                  wireless identity (SSID, passphrase, encryption, hidden, enabled)
  radio list|show|set                 radio tuning (channel, width, hwmode, power)
  profile export|import               whole-network capture and apply
  config init|show|routers            gogl's own config (the only area that acts on your machine)
```

For exact, current flags on any command, run `gogl <command> --help` - the
binary's help is authoritative.

## Reading state (safe, read-only)

```sh
gogl system info
gogl lan show                     # add --show-key to reveal WiFi passphrases
gogl lan leases                   # dynamic leases = candidates to reserve
gogl lan reservations list
gogl lan dns show
gogl clients list                 # -r mark reserved, -w wifi only, -e wired only
gogl clients vendor aa:bb:cc:dd:ee:ff   # offline, never contacts the router
gogl wifi list                    # --show-key to reveal passphrases
gogl radio list                   # --show-key to reveal passphrases
```

## Reservations (the core of a reproducible bench)

A reservation is **two writes**: a static MAC->IP bind, and a host-file DNS entry
for the name. `gogl` keeps the pair in step. The interchange format is **ISC DHCP
host-declaration** - it diffs, reviews, and lives in git.

A **DNS domain must be set first** (`gogl lan dns set --domain lab.example`): a
reservation with no resolvable name is an address nothing can find, and the
firmware's UI does not flag it.

```sh
gogl lan dns set --domain lab.example

# add by flags, or as an ISC fragment:
gogl lan reservations add --name nas --mac aa:bb:cc:dd:ee:01 --ip 192.168.8.13
gogl lan reservations add 'host nas { hardware ethernet aa:bb:cc:dd:ee:01; fixed-address 192.168.8.13; }'

gogl lan reservations export > reservations.conf          # capture to git
gogl lan reservations import reservations.conf            # idempotent; a re-run is a no-op
gogl lan reservations import reservations.conf --prune    # also delete hosts absent from the file
gogl lan reservations rm --name nas                       # also removes the DNS name
```

- `import` and `add` accept `--dry-run` (show changes, write nothing) and
  `--force` (proceed past conflicts).
- `import` is idempotent: a second run leaves the host file byte-identical.
- `rm` / `clear` take `--force` to skip the confirmation prompt.

## DNS names

On this firmware a reservation's name is just a label; DNS names live in the
router's host file (answered by dnsmasq). The domain is stored inside `gogl`'s own
block in that file, because the firmware exposes no dnsmasq domain setting.

```sh
gogl lan dns set --domain lab.example
gogl lan dns add printer 192.168.8.20
gogl lan dns rm printer              # bare or fully-qualified form both work
gogl lan dns clear                   # remove managed names, keep the domain
```

## LAN address and DHCP pool

```sh
# Resize only the pool - never refused, never drops the session:
gogl lan set --pool-start 192.168.8.100 --pool-end 192.168.8.200

# Move the whole subnet - requires all four together, drops the session:
gogl lan set --ip 192.168.9.1 --mask 255.255.255.0 \
             --pool-start 192.168.9.100 --pool-end 192.168.9.200
```

Moving the subnet is **refused while reservations exist** unless `--force`,
because the firmware silently rewrites every reservation into the new subnet.
`--guest` targets the guest interface. `--dry-run` shows the change and any
refusal without writing.

## Wireless: `wifi` (identity) vs `radio` (tuning)

- **`wifi`** sets per-interface identity: SSID, passphrase, encryption, hidden,
  enabled. A guest and a main SSID on the same radio are set independently.
- **`radio`** sets per-radio tuning: channel, width, hardware mode, transmit
  power - affecting every network on that radio.

Select the target with `--band 2.4|5|6`, or `--device radioN`, or (for wifi)
`--iface <name>`; add `--guest` for the guest interface.

```sh
gogl wifi set --band 5 --ssid lab-5g
gogl wifi set --band 5 --passphrase                 # prompts, echo off (no value on argv)
gogl wifi set --band 5 --passphrase-command "pass show wifi/lab-5g"
gogl wifi set --band 2.4 --guest --enabled=true --hidden=false

gogl radio set --band 5 --channel 149
gogl radio set --band 2.4 --channel 0 --width auto  # channel 0 = automatic
gogl radio set --device radio1 --width 80 --power Low   # power: Max|High|Medium|Low
```

**Wired-session guard (important):** `wifi set` and `radio set` are **refused over
a wireless session**, because applying the change would drop the very session with
no address to reconnect at. Run them from a **wired** connection. `--yes` waives
the confirmation prompt but **never** waives this guard. `radio set` validates
values against what the radio advertises, naming the valid options on a bad one.
Both support `--dry-run`.

## Profiles: capture and rebuild a whole network

A profile is JSON carrying what **defines** a network - LAN and pool,
reservations, DNS names and domain, wireless identity, radio tuning - and omits
what identifies a particular unit (its MAC, serial, uptime, lease state). That
omission is what lets it apply to a second router. (For a byte-exact single-device
restore, use firmware `sysupgrade -b` over SSH instead - that is out of scope for
`gogl`.)

```sh
gogl profile export > bench.json              # passphrases masked by default
gogl profile export --with-keys > bench.json  # include WiFi passphrases in cleartext

gogl profile import bench.json                # applies everything except wireless
gogl profile import bench.json --wireless     # also wireless/radio; needs a WIRED session
gogl profile import - < bench.json            # read from stdin
gogl profile import bench.json --dry-run      # show what would change
```

Import applies steps in a fixed order (domain, then network, then reservations and
names, then wireless), because each later step fails without the earlier one. If
the profile's subnet differs from the router's, the run **stops after the network
step** and prints how to resume - the router changes address mid-write, so nothing
past that point is reachable from the same session. `--force` allows a subnet move
while reservations exist.

## Conventions and safety

- **Read freely; writes are deliberate.** `show`/`list`/`export`/`info`/`leases`
  and `clients vendor` only read (vendor is fully offline). Every write command
  supports `--dry-run`.
- **Secrets never go on argv.** No `--password` flag; `wifi set --passphrase`
  prompts (or use `--passphrase-command`). This is enforced, not a convention.
- **`--yes`/`--force` skip prompts, not guards.** The wired-session guard on
  wireless writes cannot be waived.
- **Scripting:** pass `--output json` (or set `output = "json"`) and parse stdout.
- **GL.iNet defaults:** port 80, plain HTTP, self-signed TLS (hence `--insecure`
  defaults true). The default LAN is typically `192.168.8.1`.

## If `gogl` isn't installed

It builds from Go source via a Makefile: `make build` produces `./bin/gogl`.
Build and test only through the Makefile.
