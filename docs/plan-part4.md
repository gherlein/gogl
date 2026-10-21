# gogl Implementation Plan — Part 4: Phases 7, 8 and 9

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> ## Status: executed, and superseded in part
>
> This plan was written before any of it ran against hardware, and several of its premises
> turned out to be wrong. It is kept as the record of how the project was built, not as a description
> of what it now does. **For current behavior read [`../README.md`](../README.md) and
> [`DESIGN.md`](DESIGN.md); for the verified API surface read
> [`../GL_INET_4X_API_DOCUMENTATION.md`](../GL_INET_4X_API_DOCUMENTATION.md).**
>
> What changed, and where the plan is now wrong:
>
> 1. **A static bind does not create a DNS record.** The plan assumed the reservation's name
>    became a resolvable name. On firmware 4.3.28 it is a label and nothing more. DNS names come
>    from the router's host file via `dns.get_host` / `dns.set_host`, which this plan never
>    mentions. Anything below that treats a reservation as carrying a name is wrong.
> 2. **Reservations are no longer the only write.** `NetworkService.Set` writes the LAN address
>    and DHCP pool, and `HostsService` writes names and the DNS domain. Two ordering guards
>    replace the old "read-only everything else" rule.
> 3. **Endpoint names were guessed.** `dhcp.*` does not exist on this device; the real groups are
>    `lan.*` and `dns.*`. Every group and method constant below should be read as provisional.
> 4. **Wireless was out of scope and now is not.** `gogl wifi set` writes SSIDs, passphrases,
>    encryption, hidden and enabled state, and `gogl radio set` writes channel, bandwidth,
>    hardware mode and transmit power -- guarded so that no wireless write happens over a
>    wireless session.
> 5. **Field shapes taken from the vendored API description were wrong more than once.**
>    `dns.set_host` rejects `(`, `)` and `=` anywhere in the file, which broke every host-file
>    write; `wifi.get_config` sends `htmodes` as an object rather than an array, which broke
>    every wireless read. Both were typed from the description rather than a capture. Fixtures
>    are now verbatim captures, with tests asserting they decode through the library's own types.
> 6. **The four utilities became one binary.** Everything below names `goglps`, `goglmac`,
>    `goglnet` or `goglcfg`. Those binaries no longer exist: the CLI is a single `gogl` with a
>    `gogl <area> <action>` tree, and their logic moved to importable packages under
>    `utilities/internal/`. Read every utility name below as the area that replaced it --
>    `gogl lan reservations`, `gogl clients`, `gogl lan`/`wifi`/`radio`, and `gogl profile`.
>    See [`DESIGN-V2.md`](DESIGN-V2.md).
>
> The plan's *method* held up: mock first, transport second, one transparent retry, no hardware
> in the test suite. It was the API assumptions that did not survive contact.

---
Continuation of [`plan-part3.md`](plan-part3.md). Task numbering resumes at 21.
**Global Constraints from [`plan.md`](plan.md) apply to every task here.**

---

## Phase 7: goglmac

### Task 21: IEEE OUI database

**Files:**
- Create: `utilities/goglmac/oui.go`
- Test: `utilities/goglmac/oui_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `type OUIDatabase map[string]string`; `LoadOUI(cacheDir string, fetch fetcher) (OUIDatabase, error)`; `(OUIDatabase).Lookup(mac string) string`; `ParseOUI(io.Reader) (OUIDatabase, error)`; `CachePath() (string, error)`. Task 22 calls `LoadOUI` and `Lookup`.

Same behavior as `gofimac`: download, 30-day freshness, stale-cache fallback, and a hard
failure when there is no cache and no network. That last case is a documented limitation
rather than a bug — the realistic path is that you ran the tool once at home.

- [ ] **Step 1: Write the failing test**

`utilities/goglmac/oui_test.go`:

```go
package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sampleOUI = `
OUI/MA-L                                                    Organization
company_id                                                  Organization
                                                            Address

AC-DE-48   (hex)		Private
ACDE48     (base 16)		Private

00-1B-63   (hex)		Apple, Inc.
001B63     (base 16)		Apple, Inc.
				1 Infinite Loop
				Cupertino CA 95014
				US

D4-9A-20   (hex)		Dell Inc.
D49A20     (base 16)		Dell Inc.
`

func TestParseOUI(t *testing.T) {
	db, err := ParseOUI(strings.NewReader(sampleOUI))
	if err != nil {
		t.Fatalf("ParseOUI error: %v", err)
	}
	if len(db) != 3 {
		t.Fatalf("parsed %d entries, want 3", len(db))
	}
	if got := db["00:1b:63"]; got != "Apple, Inc." {
		t.Errorf("db[00:1b:63] = %q, want %q", got, "Apple, Inc.")
	}
	if got := db["d4:9a:20"]; got != "Dell Inc." {
		t.Errorf("db[d4:9a:20] = %q, want %q", got, "Dell Inc.")
	}
}

func TestLookup(t *testing.T) {
	db, err := ParseOUI(strings.NewReader(sampleOUI))
	if err != nil {
		t.Fatalf("ParseOUI error: %v", err)
	}

	tests := []struct {
		mac  string
		want string
	}{
		{"00:1b:63:aa:bb:cc", "Apple, Inc."},
		{"00:1B:63:AA:BB:CC", "Apple, Inc."},
		{"d4:9a:20:11:22:33", "Dell Inc."},
		{"ff:ff:ff:11:22:33", "unknown"},
	}
	for _, tt := range tests {
		if got := db.Lookup(tt.mac); got != tt.want {
			t.Errorf("Lookup(%s) = %q, want %q", tt.mac, got, tt.want)
		}
	}
}

// A locally-administered MAC will never appear in the IEEE database, so
// reporting "unknown" would be misleading: it is a randomized address, and a
// poor choice to reserve an address for.
func TestLookupRandomized(t *testing.T) {
	db := OUIDatabase{}
	for _, mac := range []string{"02:11:22:33:44:55", "06:aa:bb:cc:dd:ee", "0a:00:00:00:00:01", "de:ad:be:ef:00:01"} {
		if got := db.Lookup(mac); got != "randomized" {
			t.Errorf("Lookup(%s) = %q, want randomized", mac, got)
		}
	}
}

func TestLookupGloballyAdministeredIsNotRandomized(t *testing.T) {
	db := OUIDatabase{}
	// Second-least-significant bit of the first octet clear means globally
	// administered.
	if got := db.Lookup("00:1b:63:aa:bb:cc"); got != "unknown" {
		t.Errorf("Lookup = %q, want unknown", got)
	}
}

func TestLookupMalformed(t *testing.T) {
	db := OUIDatabase{}
	for _, mac := range []string{"", "zz", "00:1b"} {
		if got := db.Lookup(mac); got != "unknown" {
			t.Errorf("Lookup(%q) = %q, want unknown", mac, got)
		}
	}
}

// A fresh cache must be used without a download.
func TestLoadOUIUsesFreshCache(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ouiFilename), []byte(sampleOUI), 0o644); err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	fetched := false
	db, err := LoadOUI(dir, func() (string, error) {
		fetched = true
		return "", errors.New("should not be called")
	})
	if err != nil {
		t.Fatalf("LoadOUI error: %v", err)
	}
	if fetched {
		t.Error("LoadOUI downloaded despite a fresh cache")
	}
	if db.Lookup("00:1b:63:aa:bb:cc") != "Apple, Inc." {
		t.Error("cache was not used")
	}
}

func TestLoadOUIDownloadsWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	db, err := LoadOUI(dir, func() (string, error) { return sampleOUI, nil })
	if err != nil {
		t.Fatalf("LoadOUI error: %v", err)
	}
	if db.Lookup("d4:9a:20:11:22:33") != "Dell Inc." {
		t.Error("downloaded data was not parsed")
	}
	// The download must be cached for next time.
	if _, err := os.Stat(filepath.Join(dir, ouiFilename)); err != nil {
		t.Errorf("download was not cached: %v", err)
	}
}

func TestLoadOUIRefreshesStaleCache(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ouiFilename)
	if err := os.WriteFile(path, []byte("AC-DE-48   (hex)\t\tStale Corp\n"), 0o644); err != nil {
		t.Fatalf("seed cache: %v", err)
	}
	stale := time.Now().Add(-(maxCacheAge + 24*time.Hour))
	if err := os.Chtimes(path, stale, stale); err != nil {
		t.Fatalf("age cache: %v", err)
	}

	db, err := LoadOUI(dir, func() (string, error) { return sampleOUI, nil })
	if err != nil {
		t.Fatalf("LoadOUI error: %v", err)
	}
	if db.Lookup("00:1b:63:aa:bb:cc") != "Apple, Inc." {
		t.Error("stale cache was not refreshed")
	}
}

// Download failure with a stale cache present is a warning, not a failure.
func TestLoadOUIFallsBackToStaleCache(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ouiFilename)
	if err := os.WriteFile(path, []byte(sampleOUI), 0o644); err != nil {
		t.Fatalf("seed cache: %v", err)
	}
	stale := time.Now().Add(-(maxCacheAge + 24*time.Hour))
	if err := os.Chtimes(path, stale, stale); err != nil {
		t.Fatalf("age cache: %v", err)
	}

	db, err := LoadOUI(dir, func() (string, error) { return "", errors.New("no network") })
	if err != nil {
		t.Fatalf("LoadOUI should fall back to a stale cache, got: %v", err)
	}
	if db.Lookup("00:1b:63:aa:bb:cc") != "Apple, Inc." {
		t.Error("stale cache was not used as a fallback")
	}
}

// No cache and no network is a hard failure, matching gofimac.
func TestLoadOUIFailsWithNoCacheAndNoNetwork(t *testing.T) {
	if _, err := LoadOUI(t.TempDir(), func() (string, error) {
		return "", errors.New("no network")
	}); err == nil {
		t.Error("LoadOUI succeeded with no cache and no network, want error")
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./utilities/goglmac/ -v`
Expected: FAIL, `undefined: ParseOUI`.

- [ ] **Step 3: Write the implementation**

`utilities/goglmac/oui.go`:

```go
package main

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// OUIURL is the IEEE's canonical database.
	OUIURL = "https://standards-oui.ieee.org/oui/oui.txt"

	ouiFilename = "oui.txt"
	appName     = "goglmac"

	// maxCacheAge matches gofimac: refresh monthly, which is often enough for
	// manufacturer names and rare enough not to hammer the IEEE.
	maxCacheAge = 30 * 24 * time.Hour

	unknownManufacturer    = "unknown"
	randomizedManufacturer = "randomized"

	ouiPrefixOctets = 3
	// localAdminBit is the second-least-significant bit of the first octet. When
	// set, the address is locally administered and will never be in the IEEE
	// database.
	localAdminBit = 0x02
)

// fetcher returns the OUI file's contents. Injected so tests never touch the
// network.
type fetcher func() (string, error)

// OUIDatabase maps a lowercase colon-separated 3-octet prefix to a manufacturer.
type OUIDatabase map[string]string

// CachePath returns the OUI cache directory, honouring XDG_DATA_HOME. No root
// access is required.
func CachePath() (string, error) {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("locate home directory: %w", err)
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, appName), nil
}

// LoadOUI returns the database, downloading it when the cache is missing or
// older than maxCacheAge. A download failure with any cache present is a warning
// and the cache is used; a download failure with no cache is fatal.
func LoadOUI(cacheDir string, fetch fetcher) (OUIDatabase, error) {
	if fetch == nil {
		fetch = downloadOUI
	}
	path := filepath.Join(cacheDir, ouiFilename)

	info, statErr := os.Stat(path)
	cached := statErr == nil
	fresh := cached && time.Since(info.ModTime()) < maxCacheAge

	if fresh {
		return parseFile(path)
	}

	body, fetchErr := fetch()
	if fetchErr != nil {
		if cached {
			fmt.Fprintf(os.Stderr, "warning: OUI download failed (%v); using cached data from %s\n",
				fetchErr, info.ModTime().Format(time.RFC3339))
			return parseFile(path)
		}
		return nil, fmt.Errorf("OUI download failed and no cached copy exists: %w", fetchErr)
	}

	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return nil, fmt.Errorf("create OUI cache directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return nil, fmt.Errorf("write OUI cache: %w", err)
	}
	return ParseOUI(strings.NewReader(body))
}

func parseFile(path string) (OUIDatabase, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open OUI cache: %w", err)
	}
	defer f.Close()
	return ParseOUI(f)
}

func downloadOUI() (string, error) {
	resp, err := http.Get(OUIURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s returned %s", OUIURL, resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// ParseOUI reads the IEEE format, extracting only the "(hex)" lines. The
// "(base 16)" lines and the address blocks that follow carry no information the
// lookup needs.
func ParseOUI(r io.Reader) (OUIDatabase, error) {
	db := make(OUIDatabase)
	scanner := bufio.NewScanner(r)
	// OUI lines are short, but the file has long address lines; a generous
	// buffer avoids a scanner error on them.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		idx := strings.Index(line, "(hex)")
		if idx < 0 {
			continue
		}

		prefix := strings.TrimSpace(line[:idx])
		organization := strings.TrimSpace(line[idx+len("(hex)"):])
		if prefix == "" || organization == "" {
			continue
		}

		normalized := strings.ToLower(strings.ReplaceAll(prefix, "-", ":"))
		if strings.Count(normalized, ":") != ouiPrefixOctets-1 {
			continue
		}
		db[normalized] = organization
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read OUI data: %w", err)
	}
	return db, nil
}

// Lookup returns the manufacturer for mac's OUI prefix. A locally-administered
// address returns "randomized" rather than "unknown", because it is not a
// missing entry: such an address will never be registered, and is a poor choice
// to reserve an address for.
func (db OUIDatabase) Lookup(mac string) string {
	octets := strings.Split(strings.ToLower(strings.TrimSpace(mac)), ":")
	if len(octets) < ouiPrefixOctets {
		return unknownManufacturer
	}

	prefix := strings.Join(octets[:ouiPrefixOctets], ":")
	if organization, ok := db[prefix]; ok {
		return organization
	}

	var first uint8
	if _, err := fmt.Sscanf(octets[0], "%02x", &first); err == nil && first&localAdminBit != 0 {
		return randomizedManufacturer
	}
	return unknownManufacturer
}
```

- [ ] **Step 4: Run the tests, then commit**

Run: `go test ./utilities/goglmac/ -v -race`
Expected: PASS, all eleven tests.

```bash
git add utilities/goglmac/oui.go utilities/goglmac/oui_test.go
git commit -m "feat(goglmac): add IEEE OUI database with XDG cache"
```

### Task 22: goglmac

**Files:**
- Create: `utilities/goglmac/main.go`
- Create: `utilities/goglmac/operations.go`
- Create: `utilities/goglmac/format.go`
- Test: `utilities/goglmac/operations_test.go`
- Test: `utilities/goglmac/format_test.go`

**Interfaces:**
- Consumes: `OUIDatabase` (Task 21), `conn.Flags` (Task 19), `ClientService` (Task 18), `ReservationService` (Task 17), `ipmath.ToUint32` (Task 7).
- Produces: the `goglmac` binary and `type Entry`. Leaf.

Counterpart to `gofimac`. Its role in the workflow is discovering the MAC addresses that go
into a host file.

- [ ] **Step 1: Write the failing operations test**

`utilities/goglmac/operations_test.go`:

```go
package main

import (
	"testing"

	"github.com/gherlein/gogl/src/types"
)

func testClients() []types.Client {
	return []types.Client{
		{MAC: "d4:9a:20:00:00:02", IP: "192.168.8.20", Name: "laptop", Online: true, IsWired: false, Band: "5g"},
		{MAC: "00:1b:63:00:00:01", IP: "192.168.8.13", Name: "nas", Online: true, IsWired: true},
		{MAC: "02:aa:bb:cc:dd:ee", Online: true, IsWired: false},
	}
}

func testDB() OUIDatabase {
	return OUIDatabase{"00:1b:63": "Apple, Inc.", "d4:9a:20": "Dell Inc."}
}

// Entries sort numerically by IP, with address-less clients last.
func TestBuildEntriesSortsByIP(t *testing.T) {
	got := buildEntries(testClients(), nil, testDB(), filterAll)
	if len(got) != 3 {
		t.Fatalf("built %d entries, want 3", len(got))
	}
	if got[0].IP != "192.168.8.13" {
		t.Errorf("first IP = %q, want 192.168.8.13", got[0].IP)
	}
	if got[1].IP != "192.168.8.20" {
		t.Errorf("second IP = %q, want 192.168.8.20", got[1].IP)
	}
	if got[2].IP != "" {
		t.Errorf("third entry should have no IP, got %q", got[2].IP)
	}
}

func TestBuildEntriesLooksUpManufacturer(t *testing.T) {
	got := buildEntries(testClients(), nil, testDB(), filterAll)
	if got[0].Manufacturer != "Apple, Inc." {
		t.Errorf("Manufacturer = %q, want Apple, Inc.", got[0].Manufacturer)
	}
	if got[2].Manufacturer != "randomized" {
		t.Errorf("locally-administered MAC Manufacturer = %q, want randomized", got[2].Manufacturer)
	}
}

func TestBuildEntriesFilters(t *testing.T) {
	wired := buildEntries(testClients(), nil, testDB(), filterWired)
	if len(wired) != 1 || wired[0].Name != "nas" {
		t.Errorf("filterWired gave %v, want just nas", wired)
	}

	wifi := buildEntries(testClients(), nil, testDB(), filterWiFi)
	if len(wifi) != 2 {
		t.Errorf("filterWiFi gave %d entries, want 2", len(wifi))
	}
}

func TestBuildEntriesMarksReserved(t *testing.T) {
	reservations := []types.Reservation{{Name: "nas", MAC: "00:1b:63:00:00:01", IP: "192.168.8.13"}}
	got := buildEntries(testClients(), reservations, testDB(), filterAll)

	if !got[0].Reserved {
		t.Error("nas should be marked reserved")
	}
	if got[1].Reserved {
		t.Error("laptop should not be marked reserved")
	}
}

// A client with no reported name shows "unknown" rather than an empty column.
func TestBuildEntriesUnknownName(t *testing.T) {
	got := buildEntries(testClients(), nil, testDB(), filterAll)
	if got[2].Name != "unknown" {
		t.Errorf("Name = %q, want unknown", got[2].Name)
	}
}
```

- [ ] **Step 2: Write the failing format test**

`utilities/goglmac/format_test.go`:

```go
package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func testEntries() []Entry {
	return []Entry{
		{MAC: "00:1b:63:00:00:01", IP: "192.168.8.13", Name: "nas", Manufacturer: "Apple, Inc.", IsWired: true, Online: true, Reserved: true},
		{MAC: "02:aa:bb:cc:dd:ee", Name: "unknown", Manufacturer: "randomized", Online: true},
	}
}

func TestFormatText(t *testing.T) {
	var buf bytes.Buffer
	if err := formatText(&buf, testEntries(), false); err != nil {
		t.Fatalf("formatText error: %v", err)
	}
	out := buf.String()

	for _, want := range []string{"00:1b:63:00:00:01", "192.168.8.13", "nas", "Apple, Inc.", "randomized"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	// A client with no address shows a dash, not an empty column.
	if !strings.Contains(out, "-") {
		t.Errorf("address-less client not marked with a dash:\n%s", out)
	}
}

func TestFormatTextWithReservedColumn(t *testing.T) {
	var buf bytes.Buffer
	if err := formatText(&buf, testEntries(), true); err != nil {
		t.Fatalf("formatText error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "reserved") || !strings.Contains(out, "dynamic") {
		t.Errorf("reserved column missing:\n%s", out)
	}
}

func TestFormatJSONIsArray(t *testing.T) {
	var buf bytes.Buffer
	if err := formatJSON(&buf, testEntries()); err != nil {
		t.Fatalf("formatJSON error: %v", err)
	}

	var decoded []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("output is not a JSON array: %v", err)
	}
	if len(decoded) != 2 {
		t.Fatalf("decoded %d entries, want 2", len(decoded))
	}
	if decoded[0]["manufacturer"] != "Apple, Inc." {
		t.Errorf("manufacturer = %v", decoded[0]["manufacturer"])
	}
	// Zero-valued fields are omitted.
	if _, present := decoded[1]["ip"]; present {
		t.Error("empty ip should be omitted from JSON")
	}
}

func TestFormatJSONEmptyIsEmptyArray(t *testing.T) {
	var buf bytes.Buffer
	if err := formatJSON(&buf, nil); err != nil {
		t.Fatalf("formatJSON error: %v", err)
	}
	if got := strings.TrimSpace(buf.String()); got != "[]" {
		t.Errorf("empty output = %q, want []", got)
	}
}
```

- [ ] **Step 3: Run both to verify they fail**

Run: `go test ./utilities/goglmac/ -run 'TestBuild|TestFormat' -v`
Expected: FAIL, `undefined: buildEntries`.

- [ ] **Step 4: Write operations.go**

`utilities/goglmac/operations.go`:

```go
package main

import (
	"net"
	"sort"
	"strings"

	"github.com/gherlein/gogl/src/internal/ipmath"
	"github.com/gherlein/gogl/src/types"
)

// Entry is one row of goglmac output. JSON tags define the -j contract; the
// field set is narrower than gofimac's because the router reports less than a
// UniFi controller does.
type Entry struct {
	MAC          string `json:"mac"`
	IP           string `json:"ip,omitempty"`
	Name         string `json:"hostname"`
	Manufacturer string `json:"manufacturer"`
	IsWired      bool   `json:"is_wired"`
	Online       bool   `json:"online"`
	Reserved     bool   `json:"reserved,omitempty"`
	RXBytes      uint64 `json:"rx_bytes,omitempty"`
	TXBytes      uint64 `json:"tx_bytes,omitempty"`
	Signal       *int   `json:"signal,omitempty"`
	Band         string `json:"band,omitempty"`
}

// filter selects which clients to report.
type filter func(types.Client) bool

func filterAll(types.Client) bool      { return true }
func filterWired(c types.Client) bool  { return c.IsWired }
func filterWiFi(c types.Client) bool   { return !c.IsWired }

// buildEntries joins clients with the OUI database and the reservation table,
// sorted numerically by address with address-less clients last.
//
// The manufacturer always comes from our own OUI lookup, never from any value
// the router reports: on an OpenWrt 18.06 base the router's own table is likely
// to be years stale.
func buildEntries(clients []types.Client, reservations []types.Reservation, db OUIDatabase, keep filter) []Entry {
	reserved := make(map[string]bool, len(reservations))
	for _, r := range reservations {
		reserved[strings.ToLower(r.MAC)] = true
	}

	entries := make([]Entry, 0, len(clients))
	for _, c := range clients {
		if !keep(c) {
			continue
		}
		entries = append(entries, Entry{
			MAC:          strings.ToLower(c.MAC),
			IP:           c.IP,
			Name:         c.Hostname(),
			Manufacturer: db.Lookup(c.MAC),
			IsWired:      c.IsWired,
			Online:       c.Online,
			Reserved:     reserved[strings.ToLower(c.MAC)],
			RXBytes:      c.RXBytes,
			TXBytes:      c.TXBytes,
			Signal:       c.Signal,
			Band:         c.Band,
		})
	}

	sort.SliceStable(entries, func(i, j int) bool {
		left, right := net.ParseIP(entries[i].IP), net.ParseIP(entries[j].IP)
		// Clients without an address sort last: they are the least useful rows
		// and would otherwise collide at position zero.
		switch {
		case left == nil && right == nil:
			return entries[i].MAC < entries[j].MAC
		case left == nil:
			return false
		case right == nil:
			return true
		}
		return ipmath.ToUint32(left) < ipmath.ToUint32(right)
	})

	return entries
}
```

- [ ] **Step 5: Write format.go**

`utilities/goglmac/format.go`:

```go
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"
)

const noAddress = "-"

func formatText(w io.Writer, entries []Entry, showReserved bool) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)

	for _, e := range entries {
		ip := e.IP
		if ip == "" {
			ip = noAddress
		}
		if showReserved {
			state := "dynamic"
			if e.Reserved {
				state = "reserved"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", e.MAC, ip, e.Name, e.Manufacturer, state)
			continue
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", e.MAC, ip, e.Name, e.Manufacturer)
	}

	return tw.Flush()
}

func formatJSON(w io.Writer, entries []Entry) error {
	// A nil slice must marshal as [] rather than null, so a consumer piping this
	// into jq never has to special-case the empty result.
	if entries == nil {
		entries = []Entry{}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(entries)
}
```

- [ ] **Step 6: Write main.go**

`utilities/goglmac/main.go`:

```go
// Command goglmac lists clients connected to a GL.iNet router, with independent
// IEEE OUI manufacturer lookup. Read-only.
//
// Its practical role is discovering the MAC addresses to put into a host file
// for goglps.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/gherlein/gogl/utilities/internal/conn"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "goglmac:", err)
		os.Exit(1)
	}
}

func run() error {
	var flags conn.Flags
	fs := flag.NewFlagSet("goglmac", flag.ExitOnError)
	flags.Register(fs)

	wifi := fs.Bool("wifi", false, "list only WiFi clients")
	fs.BoolVar(wifi, "w", false, "list only WiFi clients (shorthand)")
	wired := fs.Bool("wired", false, "list only wired clients")
	fs.BoolVar(wired, "e", false, "list only wired clients (shorthand)")
	all := fs.Bool("all", false, "list all clients (default)")
	fs.BoolVar(all, "a", false, "list all clients (shorthand)")

	asJSON := fs.Bool("json", false, "output JSON instead of text")
	fs.BoolVar(asJSON, "j", false, "output JSON instead of text (shorthand)")
	showReserved := fs.Bool("reserved", false, "mark which clients have a reservation")
	fs.BoolVar(showReserved, "r", false, "mark which clients have a reservation (shorthand)")

	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}

	if *wifi && *wired {
		return errors.New("--wifi and --wired are mutually exclusive")
	}
	keep := filterAll
	switch {
	case *wifi:
		keep = filterWiFi
	case *wired:
		keep = filterWired
	}

	cacheDir, err := conn.OUICacheDir()
	if err != nil {
		return err
	}
	db, err := LoadOUI(cacheDir, nil)
	if err != nil {
		return err
	}

	client, err := flags.Connect()
	if err != nil {
		return err
	}
	defer client.Close()

	ctx := context.Background()
	clients, err := client.Clients().List(ctx)
	if err != nil {
		return err
	}

	// Only fetch reservations when they will be shown; it is an extra round trip
	// against a small SoC.
	var reservations []types.Reservation
	if *showReserved {
		reservations, err = client.Reservations().List(ctx)
		if err != nil {
			return err
		}
	}

	entries := buildEntries(clients, reservations, db, keep)

	if *asJSON {
		return formatJSON(os.Stdout, entries)
	}
	return formatText(os.Stdout, entries, *showReserved)
}
```

Add the `types` import. Add `OUICacheDir` to `utilities/internal/conn/conn.go` as a thin
wrapper so both `goglmac` and any future consumer resolve the cache the same way:

```go
// OUICacheDir returns the directory for cached IEEE OUI data, honouring
// XDG_DATA_HOME per the XDG Base Directory Specification. No root access needed.
func OUICacheDir() (string, error) {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("locate home directory: %w", err)
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "goglmac"), nil
}
```

Add `"path/filepath"` to that file's imports, and delete `CachePath` from
`utilities/goglmac/oui.go` since `OUICacheDir` replaces it. Remove its test if one was
written.

- [ ] **Step 7: Run the tests and build**

Run: `go test ./utilities/... -v -race && make build`
Expected: PASS, and `bin/goglmac` exists.

- [ ] **Step 8: Verify against a real router**

```bash
./bin/goglmac
./bin/goglmac --wired
./bin/goglmac -j -r
```

Expected: the connected clients with plausible manufacturer names. First run downloads
`oui.txt` (a few MB) and reports progress on stderr.

- [ ] **Step 9: Commit**

```bash
git add utilities/goglmac/ utilities/internal/conn/conn.go
git commit -m "feat(goglmac): add client listing with independent OUI lookup"
```

---

## Phase 8: goglps

The only utility that writes. Its file format is the interchange contract with `gofips`.

### Task 23: ISC DHCP parser

**Files:**
- Create: `utilities/goglps/parse.go`
- Test: `utilities/goglps/parse_test.go`

**Interfaces:**
- Consumes: `types.Reservation`, `types.ValidateName`, `types.NormalizeMAC` (Task 6).
- Produces: `ParseHosts(io.Reader) ([]Declaration, []error)`; `type Declaration struct { Reservation types.Reservation; Line int }`. Tasks 25, 26, 27 all use it.

Rules are identical to `gofips` so files move between the two without conversion, with one
exception: names are validated more strictly. See Task 6.

- [ ] **Step 1: Write the failing test**

`utilities/goglps/parse_test.go`:

```go
package main

import (
	"strings"
	"testing"
)

const wellFormed = `# gofips fixed IP assignments
# exported from UDM at 192.168.4.1

host myserver {
    hardware ethernet aa:bb:cc:dd:ee:01;
    fixed-address 192.168.4.10;
}

host printer {
    hardware ethernet AA:BB:CC:DD:EE:02;
    fixed-address 192.168.4.11;
}
`

func TestParseHosts(t *testing.T) {
	got, errs := ParseHosts(strings.NewReader(wellFormed))
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(got) != 2 {
		t.Fatalf("parsed %d declarations, want 2", len(got))
	}

	if got[0].Reservation.Name != "myserver" {
		t.Errorf("first name = %q, want myserver", got[0].Reservation.Name)
	}
	// MACs normalize to lowercase on input regardless of how they were written.
	if got[1].Reservation.MAC != "aa:bb:cc:dd:ee:02" {
		t.Errorf("second MAC = %q, want lowercase", got[1].Reservation.MAC)
	}
	if got[0].Reservation.IP != "192.168.4.10" {
		t.Errorf("first IP = %q", got[0].Reservation.IP)
	}
}

// Whitespace is flexible on input: tabs, no indentation, and the whole block on
// one line all parse.
func TestParseHostsToleratesWhitespace(t *testing.T) {
	inputs := []string{
		"host a {\nhardware ethernet aa:bb:cc:dd:ee:01;\nfixed-address 10.0.0.1;\n}\n",
		"host a {\n\t\thardware ethernet aa:bb:cc:dd:ee:01;\n\t\tfixed-address 10.0.0.1;\n}\n",
		"  host a  {  hardware ethernet aa:bb:cc:dd:ee:01;  fixed-address 10.0.0.1;  }  \n",
	}
	for i, in := range inputs {
		got, errs := ParseHosts(strings.NewReader(in))
		if len(errs) != 0 {
			t.Errorf("input %d: errors %v", i, errs)
			continue
		}
		if len(got) != 1 || got[0].Reservation.Name != "a" {
			t.Errorf("input %d: got %v", i, got)
		}
	}
}

// Non-host directives are ignored, so a real dhcpd.conf can be fed in directly.
func TestParseHostsIgnoresOtherDirectives(t *testing.T) {
	const input = `
option domain-name "example.org";
default-lease-time 600;

subnet 192.168.4.0 netmask 255.255.255.0 {
    range 192.168.4.100 192.168.4.200;
}

host myserver {
    hardware ethernet aa:bb:cc:dd:ee:01;
    fixed-address 192.168.4.10;
}
`
	got, errs := ParseHosts(strings.NewReader(input))
	if len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if len(got) != 1 || got[0].Reservation.Name != "myserver" {
		t.Errorf("got %v, want just myserver", got)
	}
}

// Errors carry line numbers, since the point is to find the problem in a file.
func TestParseHostsReportsLineNumbers(t *testing.T) {
	const input = `host good {
    hardware ethernet aa:bb:cc:dd:ee:01;
    fixed-address 192.168.4.10;
}

host bad {
    hardware ethernet not-a-mac;
    fixed-address 192.168.4.11;
}
`
	_, errs := ParseHosts(strings.NewReader(input))
	if len(errs) != 1 {
		t.Fatalf("got %d errors, want 1: %v", len(errs), errs)
	}
	if !strings.Contains(errs[0].Error(), "line 7") {
		t.Errorf("error %q does not name line 7", errs[0])
	}
}

func TestParseHostsRejects(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"missing mac", "host a {\n fixed-address 10.0.0.1;\n}\n", "hardware ethernet"},
		{"missing address", "host a {\n hardware ethernet aa:bb:cc:dd:ee:01;\n}\n", "fixed-address"},
		{"missing semicolon", "host a {\n hardware ethernet aa:bb:cc:dd:ee:01\n fixed-address 10.0.0.1;\n}\n", "semicolon"},
		{"unclosed block", "host a {\n hardware ethernet aa:bb:cc:dd:ee:01;\n fixed-address 10.0.0.1;\n", "unclosed"},
		{"bad ip", "host a {\n hardware ethernet aa:bb:cc:dd:ee:01;\n fixed-address 999.1.1.1;\n}\n", "IPv4"},
		{"ipv6 address", "host a {\n hardware ethernet aa:bb:cc:dd:ee:01;\n fixed-address fe80::1;\n}\n", "IPv4"},
		{"no hostname", "host {\n hardware ethernet aa:bb:cc:dd:ee:01;\n fixed-address 10.0.0.1;\n}\n", "hostname"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, errs := ParseHosts(strings.NewReader(tt.input))
			if len(errs) == 0 {
				t.Fatalf("parsed without error, want one mentioning %q", tt.want)
			}
			if !strings.Contains(errs[0].Error(), tt.want) {
				t.Errorf("error %q does not mention %q", errs[0], tt.want)
			}
		})
	}
}

// The one place a gofips file may not import unchanged: an underscore is legal
// on UniFi but is not a legal DNS label character, so it is rejected rather than
// silently rewritten.
func TestParseHostsRejectsUnderscoreName(t *testing.T) {
	const input = "host my_server {\n hardware ethernet aa:bb:cc:dd:ee:01;\n fixed-address 10.0.0.1;\n}\n"
	_, errs := ParseHosts(strings.NewReader(input))
	if len(errs) == 0 {
		t.Fatal("accepted an underscore in a hostname")
	}
	if !strings.Contains(errs[0].Error(), "_") {
		t.Errorf("error %q does not name the offending character", errs[0])
	}
}

// All errors are collected, not just the first, so one run finds every problem.
func TestParseHostsCollectsAllErrors(t *testing.T) {
	const input = `host bad1 {
    hardware ethernet nope;
    fixed-address 10.0.0.1;
}
host bad2 {
    hardware ethernet aa:bb:cc:dd:ee:02;
    fixed-address 999.9.9.9;
}
host bad3 {
    hardware ethernet aa:bb:cc:dd:ee:03;
    fixed-address 10.0.0.3;
`
	_, errs := ParseHosts(strings.NewReader(input))
	if len(errs) < 3 {
		t.Errorf("got %d errors, want at least 3: %v", len(errs), errs)
	}
}

func TestParseHostsEmpty(t *testing.T) {
	got, errs := ParseHosts(strings.NewReader("# only a comment\n"))
	if len(errs) != 0 {
		t.Errorf("errors: %v", errs)
	}
	if len(got) != 0 {
		t.Errorf("parsed %d declarations, want 0", len(got))
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./utilities/goglps/ -v`
Expected: FAIL, `undefined: ParseHosts`.

- [ ] **Step 3: Write the implementation**

`utilities/goglps/parse.go`:

```go
package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strings"

	"github.com/gherlein/gogl/src/types"
)

// Declaration is one parsed host block, with the line its block opened on so
// that errors point somewhere useful.
type Declaration struct {
	Reservation types.Reservation
	Line        int
}

// ParseHosts reads ISC DHCP host declarations, returning every declaration it
// could parse and every error it found. Errors are collected rather than
// returned on the first failure, so one run surfaces every problem in a file.
//
// Directives other than host blocks are ignored, so a real dhcpd.conf can be
// fed in unmodified.
func ParseHosts(r io.Reader) ([]Declaration, []error) {
	var (
		declarations []Declaration
		errs         []error
	)

	scanner := bufio.NewScanner(r)
	lineNo := 0

	// State for the block currently being read.
	var (
		inBlock   bool
		blockLine int
		name      string
		mac       string
		address   string
	)

	finishBlock := func() {
		if name == "" {
			errs = append(errs, fmt.Errorf("line %d: host block has no hostname", blockLine))
			return
		}
		if mac == "" {
			errs = append(errs, fmt.Errorf("line %d: host %q has no hardware ethernet statement", blockLine, name))
			return
		}
		if address == "" {
			errs = append(errs, fmt.Errorf("line %d: host %q has no fixed-address statement", blockLine, name))
			return
		}

		if err := types.ValidateName(name); err != nil {
			errs = append(errs, fmt.Errorf("line %d: %w", blockLine, err))
			return
		}
		normalizedMAC, err := types.NormalizeMAC(mac)
		if err != nil {
			errs = append(errs, fmt.Errorf("line %d: host %q: %w", blockLine, name, err))
			return
		}
		ip := net.ParseIP(address)
		if ip == nil || ip.To4() == nil {
			errs = append(errs, fmt.Errorf("line %d: host %q: %q is not a valid IPv4 address", blockLine, name, address))
			return
		}

		declarations = append(declarations, Declaration{
			Reservation: types.Reservation{Name: name, MAC: normalizedMAC, IP: ip.String(), Enabled: true},
			Line:        blockLine,
		})
	}

	resetBlock := func() {
		inBlock, name, mac, address = false, "", "", ""
	}

	for scanner.Scan() {
		lineNo++
		line := stripComment(scanner.Text())

		// A single line may hold a whole block, so walk it token group by token
		// group rather than assuming one statement per line.
		for line != "" {
			line = strings.TrimSpace(line)
			if line == "" {
				break
			}

			if !inBlock {
				rest, opened, blockName, err := openBlock(line, lineNo)
				if err != nil {
					errs = append(errs, err)
					line = ""
					continue
				}
				if !opened {
					// Not a host block: some other dhcpd.conf directive.
					line = ""
					continue
				}
				inBlock, blockLine, name = true, lineNo, blockName
				line = rest
				continue
			}

			if strings.HasPrefix(line, "}") {
				finishBlock()
				resetBlock()
				line = strings.TrimPrefix(line, "}")
				continue
			}

			statement, rest, err := takeStatement(line, lineNo)
			if err != nil {
				errs = append(errs, err)
				line = ""
				continue
			}
			applyStatement(statement, &mac, &address)
			line = rest
		}
	}

	if err := scanner.Err(); err != nil {
		errs = append(errs, fmt.Errorf("read input: %w", err))
	}
	if inBlock {
		errs = append(errs, fmt.Errorf("line %d: unclosed host block for %q", blockLine, name))
	}

	return declarations, errs
}

// stripComment removes a trailing comment. A '#' inside a value is not
// meaningful in this format, so this is safe.
func stripComment(line string) string {
	if idx := strings.Index(line, "#"); idx >= 0 {
		return line[:idx]
	}
	return line
}

// openBlock recognizes "host <name> {". It reports opened=false for any other
// directive so the caller can skip it.
func openBlock(line string, lineNo int) (rest string, opened bool, name string, err error) {
	fields := strings.Fields(line)
	if len(fields) == 0 || fields[0] != "host" {
		return "", false, "", nil
	}

	brace := strings.Index(line, "{")
	if brace < 0 {
		return "", false, "", fmt.Errorf("line %d: host block is missing its opening brace", lineNo)
	}

	name = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line[:brace]), "host"))
	if name == "" {
		return "", false, "", fmt.Errorf("line %d: host block has no hostname", lineNo)
	}
	if strings.ContainsAny(name, " \t") {
		return "", false, "", fmt.Errorf("line %d: hostname %q contains whitespace", lineNo, name)
	}

	return line[brace+1:], true, name, nil
}

// takeStatement consumes one semicolon-terminated statement.
func takeStatement(line string, lineNo int) (statement, rest string, err error) {
	semi := strings.Index(line, ";")
	if semi < 0 {
		// A closing brace on the same line means the statement was simply
		// unterminated rather than continued.
		return "", "", fmt.Errorf("line %d: statement %q is missing its terminating semicolon", lineNo, strings.TrimSpace(line))
	}
	return strings.TrimSpace(line[:semi]), line[semi+1:], nil
}

// applyStatement records the two statements this format cares about, ignoring
// any others a host block might legally contain.
func applyStatement(statement string, mac, address *string) {
	fields := strings.Fields(statement)
	switch {
	case len(fields) == 3 && fields[0] == "hardware" && fields[1] == "ethernet":
		*mac = fields[2]
	case len(fields) == 2 && fields[0] == "fixed-address":
		*address = fields[1]
	}
}
```

- [ ] **Step 4: Run the tests, then commit**

Run: `go test ./utilities/goglps/ -v -race`
Expected: PASS, all nine parser tests.

```bash
git add utilities/goglps/parse.go utilities/goglps/parse_test.go
git commit -m "feat(goglps): add ISC DHCP host declaration parser"
```

### Task 24: ISC DHCP formatter

**Files:**
- Create: `utilities/goglps/format.go`
- Test: `utilities/goglps/format_test.go`

**Interfaces:**
- Consumes: `types.Reservation` (Task 6), `types.Network` (Task 7), `ipmath.ToUint32` (Task 7), `ParseHosts` (Task 23).
- Produces: `FormatHosts(w io.Writer, res []types.Reservation, header Header) error`; `type Header struct { Host string; Network *types.Network; Date string }`. Task 25 uses both.

- [ ] **Step 1: Write the failing test**

`utilities/goglps/format_test.go`:

```go
package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/gherlein/gogl/src/types"
)

func formatFixture() ([]types.Reservation, Header) {
	return []types.Reservation{
			{Name: "printer", MAC: "aa:bb:cc:dd:ee:02", IP: "192.168.8.11"},
			{Name: "myserver", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.10"},
		}, Header{
			Host: "192.168.8.1",
			Date: "2026-07-27",
			Network: &types.Network{
				LANIP: "192.168.8.1", Netmask: "255.255.255.0",
				DHCPEnabled: true, DHCPStart: "192.168.8.100", DHCPStop: "192.168.8.249",
				DHCPLease: types.LeaseTime(12 * time.Hour), Domain: "lan",
			},
		}
}

func TestFormatHostsSortsByIP(t *testing.T) {
	res, header := formatFixture()
	var buf bytes.Buffer
	if err := FormatHosts(&buf, res, header); err != nil {
		t.Fatalf("FormatHosts error: %v", err)
	}

	out := buf.String()
	serverAt := strings.Index(out, "host myserver")
	printerAt := strings.Index(out, "host printer")
	if serverAt < 0 || printerAt < 0 {
		t.Fatalf("output missing a host block:\n%s", out)
	}
	// .10 must precede .11: numeric order, not the input order.
	if serverAt > printerAt {
		t.Errorf("output is not sorted by IP:\n%s", out)
	}
}

func TestFormatHostsUsesFourSpaceIndent(t *testing.T) {
	res, header := formatFixture()
	var buf bytes.Buffer
	if err := FormatHosts(&buf, res, header); err != nil {
		t.Fatalf("FormatHosts error: %v", err)
	}
	if !strings.Contains(buf.String(), "\n    hardware ethernet ") {
		t.Errorf("hardware ethernet is not indented four spaces:\n%s", buf.String())
	}
}

// The header records the subnet and pool, so a file's intended network is
// evident from the file itself.
func TestFormatHostsHeader(t *testing.T) {
	res, header := formatFixture()
	var buf bytes.Buffer
	if err := FormatHosts(&buf, res, header); err != nil {
		t.Fatalf("FormatHosts error: %v", err)
	}

	out := buf.String()
	for _, want := range []string{"# goglps reservations", "192.168.8.1", "192.168.8.0/24", "192.168.8.100-192.168.8.249", "lan", "2026-07-27"} {
		if !strings.Contains(out, want) {
			t.Errorf("header missing %q:\n%s", want, out)
		}
	}
}

// Round-tripping is the property that makes the file a usable contract.
func TestFormatHostsRoundTrips(t *testing.T) {
	res, header := formatFixture()
	var buf bytes.Buffer
	if err := FormatHosts(&buf, res, header); err != nil {
		t.Fatalf("FormatHosts error: %v", err)
	}

	got, errs := ParseHosts(bytes.NewReader(buf.Bytes()))
	if len(errs) != 0 {
		t.Fatalf("re-parsing our own output failed: %v", errs)
	}
	if len(got) != 2 {
		t.Fatalf("re-parsed %d declarations, want 2", len(got))
	}
	if got[0].Reservation.Name != "myserver" || got[0].Reservation.IP != "192.168.8.10" {
		t.Errorf("round trip changed the first entry: %+v", got[0].Reservation)
	}
}

// A nameless reservation is emitted with a MAC-derived hostname plus a comment,
// because the format is keyed by hostname and cannot represent the absence of
// one.
func TestFormatHostsNamelessReservation(t *testing.T) {
	res := []types.Reservation{{MAC: "aa:bb:cc:dd:ee:09", IP: "192.168.8.30"}}
	var buf bytes.Buffer
	if err := FormatHosts(&buf, res, Header{Host: "192.168.8.1", Date: "2026-07-27"}); err != nil {
		t.Fatalf("FormatHosts error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "host aa-bb-cc-dd-ee-09") {
		t.Errorf("nameless reservation did not get a MAC-derived hostname:\n%s", out)
	}
	// The comment must warn that importing this file assigns the name for real.
	if !strings.Contains(out, "#") || !strings.Contains(strings.ToLower(out), "no dns") {
		t.Errorf("nameless reservation is not annotated:\n%s", out)
	}
}

// An empty device emits a commented example rather than nothing, so the
// expected format is discoverable.
func TestFormatHostsEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := FormatHosts(&buf, nil, Header{Host: "192.168.8.1", Date: "2026-07-27"}); err != nil {
		t.Fatalf("FormatHosts error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "# host example") {
		t.Errorf("empty output does not show a commented example:\n%s", out)
	}
	// The example must be entirely commented, so the file re-imports as empty.
	parsed, errs := ParseHosts(strings.NewReader(out))
	if len(errs) != 0 || len(parsed) != 0 {
		t.Errorf("empty output does not re-parse as empty: %d declarations, %v", len(parsed), errs)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./utilities/goglps/ -run TestFormatHosts -v`
Expected: FAIL, `undefined: FormatHosts`.

- [ ] **Step 3: Write the implementation**

`utilities/goglps/format.go`:

```go
package main

import (
	"fmt"
	"io"
	"net"
	"sort"
	"strings"

	"github.com/gherlein/gogl/src/internal/ipmath"
	"github.com/gherlein/gogl/src/types"
)

const indent = "    "

// Header is the commented preamble emitted above the declarations.
type Header struct {
	Host    string
	Network *types.Network
	Date    string
}

// FormatHosts writes reservations as ISC DHCP host declarations, sorted
// numerically by address. The output is byte-compatible with what gofips emits,
// so files move between the two tools without conversion.
func FormatHosts(w io.Writer, res []types.Reservation, header Header) error {
	if err := writeHeader(w, header); err != nil {
		return err
	}

	if len(res) == 0 {
		// Show the expected format rather than an empty file, entirely
		// commented so the file still re-imports as empty.
		_, err := fmt.Fprintf(w, `# No reservations are configured. The expected format is:
#
# host example {
#     hardware ethernet aa:bb:cc:dd:ee:ff;
#     fixed-address %s;
# }
`, exampleAddress(header.Network))
		return err
	}

	sorted := make([]types.Reservation, len(res))
	copy(sorted, res)
	sort.SliceStable(sorted, func(i, j int) bool {
		return ipmath.ToUint32(net.ParseIP(sorted[i].IP)) < ipmath.ToUint32(net.ParseIP(sorted[j].IP))
	})

	for _, r := range sorted {
		name := r.Name
		if name == "" {
			// The format is keyed by hostname and cannot represent its absence,
			// so derive one and say so: re-importing this file assigns the name
			// for real and the router starts answering DNS for it.
			name = strings.ReplaceAll(strings.ToLower(r.MAC), ":", "-")
			if _, err := fmt.Fprintf(w, "\n# router serves no DNS for this entry; importing this file will assign the name below\n"); err != nil {
				return err
			}
		} else if _, err := fmt.Fprintln(w); err != nil {
			return err
		}

		if _, err := fmt.Fprintf(w, "host %s {\n%shardware ethernet %s;\n%sfixed-address %s;\n}\n",
			name, indent, strings.ToLower(r.MAC), indent, r.IP); err != nil {
			return err
		}
	}
	return nil
}

func writeHeader(w io.Writer, header Header) error {
	if _, err := fmt.Fprintln(w, "# goglps reservations"); err != nil {
		return err
	}
	if header.Host != "" {
		if _, err := fmt.Fprintf(w, "# exported from GL.iNet router at %s\n", header.Host); err != nil {
			return err
		}
	}

	if n := header.Network; n != nil {
		subnet := ""
		if s, err := n.Subnet(); err == nil {
			subnet = s.String()
		}
		pool := "(disabled)"
		if n.DHCPEnabled {
			pool = fmt.Sprintf("%s-%s", n.DHCPStart, n.DHCPStop)
		}
		if _, err := fmt.Fprintf(w, "# lan: %s  pool: %s  lease: %s  domain: %s\n",
			subnet, pool, n.DHCPLease.String(), n.Domain); err != nil {
			return err
		}
	}

	if header.Date != "" {
		if _, err := fmt.Fprintf(w, "# date: %s\n", header.Date); err != nil {
			return err
		}
	}
	return nil
}

// exampleAddress picks a plausible address inside the router's subnet for the
// commented example, so the placeholder is not misleading.
func exampleAddress(n *types.Network) string {
	if n == nil {
		return "192.168.8.10"
	}
	subnet, err := n.Subnet()
	if err != nil {
		return "192.168.8.10"
	}
	base := subnet.IP.To4()
	if base == nil {
		return "192.168.8.10"
	}
	return fmt.Sprintf("%d.%d.%d.10", base[0], base[1], base[2])
}
```

- [ ] **Step 4: Run the tests, then commit**

Run: `go test ./utilities/goglps/ -v -race`
Expected: PASS. `TestFormatHostsRoundTrips` is the one that matters: it proves parser and
formatter agree.

```bash
git add utilities/goglps/format.go utilities/goglps/format_test.go
git commit -m "feat(goglps): add ISC DHCP host declaration formatter"
```

### Task 25: goglps operations and --get

**Files:**
- Create: `utilities/goglps/operations.go`
- Create: `utilities/goglps/main.go`
- Test: `utilities/goglps/operations_test.go`

**Interfaces:**
- Consumes: `ParseHosts` (Task 23), `FormatHosts` (Task 24), `conn.Flags` (Task 19), `ReservationService` (Task 17), `NetworkService` (Task 16).
- Produces: `runGet(ctx, w, reservationLister, networkGetter, date string) error`; `type Plan`, `planChanges(...)`; the `goglps` binary with mode dispatch. Tasks 26 and 27 extend `main.go`.

- [ ] **Step 1: Write the failing test**

`utilities/goglps/operations_test.go`:

```go
package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/gherlein/gogl/src/types"
)

type stubReservations struct {
	list []types.Reservation
}

func (s *stubReservations) List(context.Context) ([]types.Reservation, error) { return s.list, nil }

type stubNetwork struct{ n *types.Network }

func (s stubNetwork) Get(context.Context) (*types.Network, error) { return s.n, nil }

func testLAN() *types.Network {
	return &types.Network{
		LANIP: "192.168.8.1", Netmask: "255.255.255.0",
		DHCPEnabled: true, DHCPStart: "192.168.8.100", DHCPStop: "192.168.8.249",
		Domain: "lan",
	}
}

func TestRunGet(t *testing.T) {
	res := &stubReservations{list: []types.Reservation{
		{Name: "nas", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13"},
	}}

	var buf bytes.Buffer
	if err := runGet(context.Background(), &buf, res, stubNetwork{testLAN()}, "2026-07-27"); err != nil {
		t.Fatalf("runGet error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "host nas") {
		t.Errorf("output missing the reservation:\n%s", out)
	}
	// Output must re-parse, which is the whole point of the format.
	parsed, errs := ParseHosts(strings.NewReader(out))
	if len(errs) != 0 || len(parsed) != 1 {
		t.Errorf("output does not round trip: %d declarations, %v", len(parsed), errs)
	}
}

func TestPlanChanges(t *testing.T) {
	device := []types.Reservation{
		{Name: "nas", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13"},
		{Name: "printer", MAC: "aa:bb:cc:dd:ee:02", IP: "192.168.8.99"},
		{Name: "gone", MAC: "aa:bb:cc:dd:ee:03", IP: "192.168.8.30"},
	}
	file := []types.Reservation{
		{Name: "nas", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13"},     // unchanged
		{Name: "printer", MAC: "aa:bb:cc:dd:ee:02", IP: "192.168.8.14"},  // IP changed
		{Name: "camera", MAC: "aa:bb:cc:dd:ee:04", IP: "192.168.8.15"},   // new
	}

	plan := planChanges(file, device)

	if len(plan.Skip) != 1 || plan.Skip[0].MAC != "aa:bb:cc:dd:ee:01" {
		t.Errorf("Skip = %v, want just nas", plan.Skip)
	}
	if len(plan.Update) != 1 || plan.Update[0].IP != "192.168.8.14" {
		t.Errorf("Update = %v, want printer at .14", plan.Update)
	}
	if len(plan.Create) != 1 || plan.Create[0].Name != "camera" {
		t.Errorf("Create = %v, want camera", plan.Create)
	}
	if len(plan.Prune) != 1 || plan.Prune[0].Name != "gone" {
		t.Errorf("Prune = %v, want gone", plan.Prune)
	}
}

// A name change with the same address is still an update.
func TestPlanChangesDetectsRename(t *testing.T) {
	device := []types.Reservation{{Name: "old", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13"}}
	file := []types.Reservation{{Name: "new", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13"}}

	plan := planChanges(file, device)
	if len(plan.Update) != 1 || plan.Update[0].Name != "new" {
		t.Errorf("Update = %v, want a rename to new", plan.Update)
	}
	if len(plan.Skip) != 0 {
		t.Errorf("Skip = %v, want empty", plan.Skip)
	}
}

// Running twice must be a no-op the second time; idempotence is what makes a
// host file usable as a checked-in description of a network.
func TestPlanChangesIsIdempotent(t *testing.T) {
	file := []types.Reservation{{Name: "nas", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13"}}
	plan := planChanges(file, file)

	if len(plan.Create) != 0 || len(plan.Update) != 0 || len(plan.Prune) != 0 {
		t.Errorf("applying a file to itself is not a no-op: %+v", plan)
	}
	if len(plan.Skip) != 1 {
		t.Errorf("Skip = %v, want one entry", plan.Skip)
	}
}

func TestValidateAgainstDeviceRejectsOutsideSubnet(t *testing.T) {
	file := []types.Reservation{
		{Name: "a", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.4.10"},
		{Name: "b", MAC: "aa:bb:cc:dd:ee:02", IP: "192.168.4.11"},
	}
	_, errs := validateAgainstDevice(file, testLAN())
	if len(errs) == 0 {
		t.Fatal("accepted addresses outside the LAN subnet")
	}
	joined := errsText(errs)
	if !strings.Contains(joined, "192.168.8.0/24") || !strings.Contains(joined, "2 of 2") {
		t.Errorf("subnet mismatch report is unhelpful:\n%s", joined)
	}
	// Both remedies must be named, because both are the operator's to choose.
	if !strings.Contains(joined, "admin panel") || !strings.Contains(strings.ToLower(joined), "renumber") {
		t.Errorf("report does not name both remedies:\n%s", joined)
	}
}

// An address inside the dynamic pool warns but never blocks: dnsmasq honors a
// static lease inside the range and excludes it from allocation.
func TestValidateAgainstDeviceWarnsOnPooledAddress(t *testing.T) {
	file := []types.Reservation{{Name: "a", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.150"}}
	warnings, errs := validateAgainstDevice(file, testLAN())
	if len(errs) != 0 {
		t.Fatalf("pooled address must not be an error: %v", errs)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "192.168.8.150") {
		t.Errorf("warnings = %v, want one naming the address", warnings)
	}
}

func TestValidateAgainstDeviceRejectsRouterAddress(t *testing.T) {
	file := []types.Reservation{{Name: "a", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.1"}}
	if _, errs := validateAgainstDevice(file, testLAN()); len(errs) == 0 {
		t.Error("accepted the router's own address as a reservation")
	}
}

func TestFindDuplicates(t *testing.T) {
	declarations := []Declaration{
		{Reservation: types.Reservation{Name: "a", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.10"}, Line: 1},
		{Reservation: types.Reservation{Name: "a", MAC: "aa:bb:cc:dd:ee:02", IP: "192.168.8.11"}, Line: 5},
		{Reservation: types.Reservation{Name: "c", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.12"}, Line: 9},
		{Reservation: types.Reservation{Name: "d", MAC: "aa:bb:cc:dd:ee:04", IP: "192.168.8.10"}, Line: 13},
	}
	errs := findDuplicates(declarations)
	if len(errs) != 3 {
		t.Fatalf("got %d duplicate errors, want 3 (name, MAC, IP): %v", len(errs), errs)
	}
	joined := errsText(errs)
	for _, want := range []string{"line 5", "line 9", "line 13"} {
		if !strings.Contains(joined, want) {
			t.Errorf("duplicate report missing %q:\n%s", want, joined)
		}
	}
}

func errsText(errs []error) string {
	parts := make([]string, len(errs))
	for i, err := range errs {
		parts[i] = err.Error()
	}
	return strings.Join(parts, "\n")
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./utilities/goglps/ -run 'TestRunGet|TestPlan|TestValidate|TestFindDup' -v`
Expected: FAIL, `undefined: runGet`.

- [ ] **Step 3: Write operations.go**

`utilities/goglps/operations.go`:

```go
package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"

	"github.com/gherlein/gogl/src/types"
)

// The narrow interfaces below let every operation be tested with stubs.
type reservationLister interface {
	List(context.Context) ([]types.Reservation, error)
}

type networkGetter interface {
	Get(context.Context) (*types.Network, error)
}

// Plan is the diff between a host file and the device, partitioned by action.
//
// Create, Update and Skip partition the file's declarations. Prune counts
// reservations that exist only on the device, so summing all four is
// meaningless: they count different things.
type Plan struct {
	Create []types.Reservation
	Update []types.Reservation
	Skip   []types.Reservation
	Prune  []types.Reservation
}

// runGet exports the device's reservations in ISC DHCP format.
func runGet(ctx context.Context, w io.Writer, res reservationLister, nets networkGetter, date string) error {
	reservations, err := res.List(ctx)
	if err != nil {
		return err
	}

	header := Header{Date: date}
	// The network is a convenience in the header; a router that will not report
	// it is still worth exporting reservations from.
	if network, err := nets.Get(ctx); err == nil {
		header.Network = network
		header.Host = network.LANIP
	}

	return FormatHosts(w, reservations, header)
}

// planChanges diffs a host file against the device, keyed by MAC. MAC is the
// identity because it is the only thing a client cannot change about itself and
// it is what dnsmasq keys the lease on.
func planChanges(file, device []types.Reservation) Plan {
	byMAC := make(map[string]types.Reservation, len(device))
	for _, r := range device {
		byMAC[strings.ToLower(r.MAC)] = r
	}

	inFile := make(map[string]bool, len(file))
	var plan Plan

	for _, want := range file {
		key := strings.ToLower(want.MAC)
		inFile[key] = true

		existing, present := byMAC[key]
		switch {
		case !present:
			plan.Create = append(plan.Create, want)
		case existing.IP == want.IP && existing.Name == want.Name:
			plan.Skip = append(plan.Skip, want)
		default:
			plan.Update = append(plan.Update, want)
		}
	}

	for _, existing := range device {
		if !inFile[strings.ToLower(existing.MAC)] {
			plan.Prune = append(plan.Prune, existing)
		}
	}

	return plan
}

// validateAgainstDevice checks a host file against the router's actual LAN,
// returning warnings that do not block and errors that do.
func validateAgainstDevice(file []types.Reservation, network *types.Network) (warnings []string, errs []error) {
	subnet, err := network.Subnet()
	if err != nil {
		return nil, []error{fmt.Errorf("router LAN configuration is unusable: %w", err)}
	}
	routerIP := net.ParseIP(network.LANIP)

	var outside []types.Reservation
	for _, r := range file {
		ip := net.ParseIP(r.IP)
		if ip == nil {
			errs = append(errs, fmt.Errorf("host %q: %q is not a valid address", r.Name, r.IP))
			continue
		}

		if !subnet.Contains(ip) {
			outside = append(outside, r)
			continue
		}
		if routerIP != nil && ip.Equal(routerIP) {
			errs = append(errs, fmt.Errorf("host %q: %s is the router's own address", r.Name, r.IP))
			continue
		}

		// A pooled address is untidy, not broken: dnsmasq honors a static lease
		// inside the dynamic range and excludes it from allocation. It would be
		// a genuine conflict under ISC dhcpd, which is where the contrary
		// intuition comes from.
		if pooled, err := network.InDHCPPool(ip); err == nil && pooled {
			warnings = append(warnings, fmt.Sprintf(
				"host %q at %s is inside the DHCP pool (%s-%s); dnsmasq honors it, but it is tidier outside",
				r.Name, r.IP, network.DHCPStart, network.DHCPStop))
		}
	}

	if len(outside) > 0 {
		errs = append(errs, subnetMismatch(outside, len(file), subnet.String(), network))
	}
	return warnings, errs
}

// subnetMismatch builds the report for addresses outside the router's LAN. Both
// remedies are named because both are the operator's choice: gogl never changes
// the router's LAN address, and never silently renumbers a file.
func subnetMismatch(outside []types.Reservation, total int, subnet string, network *types.Network) error {
	fileSubnet := "unknown"
	if len(outside) > 0 {
		if ip := net.ParseIP(outside[0].IP).To4(); ip != nil {
			fileSubnet = fmt.Sprintf("%d.%d.%d.0/24", ip[0], ip[1], ip[2])
		}
	}
	suggested := "unknown"
	if ip := net.ParseIP(outside[0].IP).To4(); ip != nil {
		suggested = fmt.Sprintf("%d.%d.%d.1/24", ip[0], ip[1], ip[2])
	}

	var b strings.Builder
	fmt.Fprintf(&b, "subnet mismatch\n")
	fmt.Fprintf(&b, "  host file:   %s (%d of %d entries)\n", fileSubnet, len(outside), total)
	fmt.Fprintf(&b, "  router LAN:  %s/%s\n", network.LANIP, strings.TrimPrefix(subnet, strings.Split(subnet, "/")[0]+"/"))
	fmt.Fprintf(&b, "\nResolve by either:\n")
	fmt.Fprintf(&b, "  - Setting the router's LAN address to %s in the GL.iNet admin panel\n", suggested)
	fmt.Fprintf(&b, "    (LAN -> Router IP Address), then re-running. Your management session will\n")
	fmt.Fprintf(&b, "    drop and you will need to reconnect at the new address.\n")
	fmt.Fprintf(&b, "  - Renumbering the host file into %s before re-running.\n", subnet)
	return fmt.Errorf("%s", b.String())
}

// findDuplicates reports repeated names, MACs, or addresses within a file. Each
// is fatal: a file that reserves one address twice does not describe a network.
func findDuplicates(declarations []Declaration) []error {
	var errs []error
	names := make(map[string]int, len(declarations))
	macs := make(map[string]int, len(declarations))
	ips := make(map[string]int, len(declarations))

	for _, d := range declarations {
		r := d.Reservation
		if first, seen := names[r.Name]; seen {
			errs = append(errs, fmt.Errorf("line %d: hostname %q already used at line %d", d.Line, r.Name, first))
		} else {
			names[r.Name] = d.Line
		}

		key := strings.ToLower(r.MAC)
		if first, seen := macs[key]; seen {
			errs = append(errs, fmt.Errorf("line %d: MAC %s already used at line %d", d.Line, r.MAC, first))
		} else {
			macs[key] = d.Line
		}

		if first, seen := ips[r.IP]; seen {
			errs = append(errs, fmt.Errorf("line %d: address %s already used at line %d", d.Line, r.IP, first))
		} else {
			ips[r.IP] = d.Line
		}
	}
	return errs
}

// reservationsOf strips line numbers, for handing a parsed file to the planner.
func reservationsOf(declarations []Declaration) []types.Reservation {
	out := make([]types.Reservation, len(declarations))
	for i, d := range declarations {
		out[i] = d.Reservation
	}
	return out
}
```

- [ ] **Step 4: Write main.go with --get wired up**

`utilities/goglps/main.go`:

```go
// Command goglps manages static IP reservations and their DNS names on a
// GL.iNet travel router, using ISC DHCP host declaration format.
//
// On this device a reservation is simultaneously the DHCP binding and the DNS
// record: GL.iNet writes the reservation's name into dnsmasq, which then answers
// queries for it. So there is nothing to keep in sync and no way to have the
// name without the reservation, which is why gofips's --keep-dns has no
// analogue here.
//
// The file format is identical to gofips's, so a file exported from a UniFi
// controller imports here without conversion.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/gherlein/gogl/utilities/internal/conn"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "goglps:", err)
		os.Exit(1)
	}
}

type modeFlags struct {
	get bool
	set bool
	add bool
	del bool

	name string
	mac  string
	ip   string

	force  bool
	prune  bool
	dryRun bool
}

func run() error {
	var (
		flags conn.Flags
		modes modeFlags
	)
	fs := flag.NewFlagSet("goglps", flag.ExitOnError)
	flags.Register(fs)

	fs.BoolVar(&modes.get, "get", false, "export all reservations in ISC DHCP format")
	fs.BoolVar(&modes.get, "g", false, "export all reservations (shorthand)")
	fs.BoolVar(&modes.set, "set", false, "import host declarations from a file or stdin")
	fs.BoolVar(&modes.set, "s", false, "import host declarations (shorthand)")
	fs.BoolVar(&modes.add, "add", false, "add a single host from a declaration fragment")
	fs.BoolVar(&modes.add, "a", false, "add a single host (shorthand)")
	fs.BoolVar(&modes.del, "del", false, "delete a host by --name, --mac, or --ip")
	fs.BoolVar(&modes.del, "d", false, "delete a host (shorthand)")

	fs.StringVar(&modes.name, "name", "", "hostname, with --del")
	fs.StringVar(&modes.name, "n", "", "hostname, with --del (shorthand)")
	fs.StringVar(&modes.mac, "mac", "", "MAC address, with --del")
	fs.StringVar(&modes.mac, "m", "", "MAC address, with --del (shorthand)")
	fs.StringVar(&modes.ip, "ip", "", "IP address, with --del")
	fs.StringVar(&modes.ip, "i", "", "IP address, with --del (shorthand)")

	fs.BoolVar(&modes.force, "force", false, "proceed past conflicts")
	fs.BoolVar(&modes.force, "f", false, "proceed past conflicts (shorthand)")
	fs.BoolVar(&modes.prune, "prune", false, "with --set, delete reservations absent from the file")
	fs.BoolVar(&modes.prune, "P", false, "with --set, delete reservations absent from the file (shorthand)")
	fs.BoolVar(&modes.dryRun, "dry-run", false, "show what would change without changing it")

	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}

	selected := 0
	for _, on := range []bool{modes.get, modes.set, modes.add, modes.del} {
		if on {
			selected++
		}
	}
	if selected != 1 {
		fs.Usage()
		return errors.New("exactly one of --get, --set, --add, --del is required")
	}

	client, err := flags.Connect()
	if err != nil {
		return err
	}
	defer client.Close()

	ctx := context.Background()
	date := time.Now().Format(time.DateOnly)

	switch {
	case modes.get:
		return runGet(ctx, os.Stdout, client.Reservations(), client.Network(), date)
	case modes.set:
		return runSet(ctx, client, fs.Arg(0), modes)
	case modes.add:
		return runAdd(ctx, client, fs.Arg(0), modes)
	case modes.del:
		return runDel(ctx, client, modes)
	}
	return nil
}
```

`runSet`, `runAdd` and `runDel` arrive in Tasks 26 and 27. To keep the package compiling
until then, add temporary declarations returning `errors.New("not implemented")` and delete
them as each real one lands.

- [ ] **Step 5: Run the tests, then commit**

Run: `go test ./utilities/goglps/ -v -race`
Expected: PASS.

```bash
git add utilities/goglps/operations.go utilities/goglps/main.go utilities/goglps/operations_test.go
git commit -m "feat(goglps): add planning, device validation, and --get"
```

### Task 26: goglps --set

**Files:**
- Create: `utilities/goglps/set.go`
- Test: `utilities/goglps/set_test.go`
- Modify: `utilities/goglps/main.go`

**Interfaces:**
- Consumes: `ParseHosts` (Task 23), `planChanges`, `validateAgainstDevice`, `findDuplicates`, `reservationsOf` (Task 25), `ReservationService` (Task 17).
- Produces: `runSet(ctx, client, path string, modes modeFlags) error` and `applyPlan(ctx, io.Writer, reservationWriter, Plan, bool) (Summary, error)`; `type Summary`. Task 27 reuses `reservationWriter`.

Four phases, in order, for a reason: all file validation before any device contact, all
device reads before any write, and independent per-entry writes so one failure does not abort
the rest.

- [ ] **Step 1: Write the failing test**

`utilities/goglps/set_test.go`:

```go
package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gherlein/gogl/src/types"
)

// fakeWriter records mutations so a test can assert on what was attempted.
type fakeWriter struct {
	created []types.Reservation
	updated []types.Reservation
	deleted []string
	failOn  string
}

func (f *fakeWriter) Create(_ context.Context, r *types.Reservation) (*types.Reservation, error) {
	if f.failOn == r.MAC {
		return nil, errors.New("injected create failure")
	}
	f.created = append(f.created, *r)
	return r, nil
}

func (f *fakeWriter) Update(_ context.Context, r *types.Reservation) (*types.Reservation, error) {
	if f.failOn == r.MAC {
		return nil, errors.New("injected update failure")
	}
	f.updated = append(f.updated, *r)
	return r, nil
}

func (f *fakeWriter) Delete(_ context.Context, mac string) error {
	if f.failOn == mac {
		return errors.New("injected delete failure")
	}
	f.deleted = append(f.deleted, mac)
	return nil
}

func testPlan() Plan {
	return Plan{
		Create: []types.Reservation{{Name: "camera", MAC: "aa:bb:cc:dd:ee:04", IP: "192.168.8.15"}},
		Update: []types.Reservation{{Name: "printer", MAC: "aa:bb:cc:dd:ee:02", IP: "192.168.8.14"}},
		Skip:   []types.Reservation{{Name: "nas", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13"}},
		Prune:  []types.Reservation{{Name: "gone", MAC: "aa:bb:cc:dd:ee:03", IP: "192.168.8.30"}},
	}
}

func TestApplyPlan(t *testing.T) {
	w := &fakeWriter{}
	var log bytes.Buffer

	summary, err := applyPlan(context.Background(), &log, w, testPlan(), false)
	if err != nil {
		t.Fatalf("applyPlan error: %v", err)
	}

	if len(w.created) != 1 || w.created[0].Name != "camera" {
		t.Errorf("created = %v", w.created)
	}
	if len(w.updated) != 1 || w.updated[0].IP != "192.168.8.14" {
		t.Errorf("updated = %v", w.updated)
	}
	// Without --prune, extra device reservations are counted, never deleted.
	if len(w.deleted) != 0 {
		t.Errorf("deleted = %v without --prune, want none", w.deleted)
	}
	if summary.Created != 1 || summary.Updated != 1 || summary.Skipped != 1 || summary.Pruned != 0 {
		t.Errorf("summary = %+v", summary)
	}
}

func TestApplyPlanWithPrune(t *testing.T) {
	w := &fakeWriter{}
	var log bytes.Buffer

	summary, err := applyPlan(context.Background(), &log, w, testPlan(), true)
	if err != nil {
		t.Fatalf("applyPlan error: %v", err)
	}
	if len(w.deleted) != 1 || w.deleted[0] != "aa:bb:cc:dd:ee:03" {
		t.Errorf("deleted = %v, want the pruned MAC", w.deleted)
	}
	if summary.Pruned != 1 {
		t.Errorf("Pruned = %d, want 1", summary.Pruned)
	}
}

// One failing entry must not abort the rest, and must still make the run fail
// overall.
func TestApplyPlanContinuesPastFailure(t *testing.T) {
	plan := Plan{Create: []types.Reservation{
		{Name: "a", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.11"},
		{Name: "b", MAC: "aa:bb:cc:dd:ee:02", IP: "192.168.8.12"},
		{Name: "c", MAC: "aa:bb:cc:dd:ee:03", IP: "192.168.8.13"},
	}}
	w := &fakeWriter{failOn: "aa:bb:cc:dd:ee:02"}
	var log bytes.Buffer

	summary, err := applyPlan(context.Background(), &log, w, plan, false)
	if err != nil {
		t.Fatalf("applyPlan returned a fatal error: %v", err)
	}
	if len(w.created) != 2 {
		t.Errorf("created %d, want 2: the loop stopped at the failure", len(w.created))
	}
	if summary.Errors != 1 {
		t.Errorf("Errors = %d, want 1", summary.Errors)
	}
	if !strings.Contains(log.String(), "aa:bb:cc:dd:ee:02") {
		t.Errorf("log does not name the failed entry:\n%s", log.String())
	}
}

func TestSummaryHasError(t *testing.T) {
	if (Summary{}).HasError() {
		t.Error("empty summary reports an error")
	}
	if !(Summary{Errors: 1}).HasError() {
		t.Error("summary with errors does not report one")
	}
}

// A malformed file must be rejected before any device contact, so a bad file
// never half-writes a router.
func TestValidateFileRejectsBeforeConnecting(t *testing.T) {
	const input = `host bad_name {
    hardware ethernet aa:bb:cc:dd:ee:01;
    fixed-address 192.168.8.10;
}
`
	_, errs := validateFile(strings.NewReader(input))
	if len(errs) == 0 {
		t.Fatal("accepted an invalid file")
	}
}

func TestValidateFileRejectsDuplicates(t *testing.T) {
	const input = `host a {
    hardware ethernet aa:bb:cc:dd:ee:01;
    fixed-address 192.168.8.10;
}
host b {
    hardware ethernet aa:bb:cc:dd:ee:01;
    fixed-address 192.168.8.11;
}
`
	_, errs := validateFile(strings.NewReader(input))
	if len(errs) == 0 {
		t.Fatal("accepted a duplicate MAC")
	}
	if !strings.Contains(errsText(errs), "already used") {
		t.Errorf("errors do not report the duplicate: %v", errs)
	}
}

func TestValidateFileAccepts(t *testing.T) {
	got, errs := validateFile(strings.NewReader(wellFormed))
	if len(errs) != 0 {
		t.Fatalf("rejected a valid file: %v", errs)
	}
	if len(got) != 2 {
		t.Errorf("parsed %d reservations, want 2", len(got))
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./utilities/goglps/ -run 'TestApplyPlan|TestSummary|TestValidateFile' -v`
Expected: FAIL, `undefined: applyPlan`.

- [ ] **Step 3: Write the implementation**

`utilities/goglps/set.go`:

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	gogl "github.com/gherlein/gogl/src"
	"github.com/gherlein/gogl/src/types"
)

// reservationWriter is the mutating subset of ReservationService, so applyPlan
// can be tested without a client.
type reservationWriter interface {
	Create(context.Context, *types.Reservation) (*types.Reservation, error)
	Update(context.Context, *types.Reservation) (*types.Reservation, error)
	Delete(context.Context, string) error
}

// Summary counts what a run did. File-side and device-side counts are reported
// separately because they count different things: Created, Updated and Skipped
// partition the file's declarations, while Pruned counts device-only entries.
type Summary struct {
	Created int
	Updated int
	Skipped int
	Pruned  int
	Errors  int
}

func (s Summary) HasError() bool { return s.Errors > 0 }

func (s Summary) String() string {
	return fmt.Sprintf("%d host declarations: %d created, %d updated, %d skipped; %d pruned; %d errors",
		s.Created+s.Updated+s.Skipped, s.Created, s.Updated, s.Skipped, s.Pruned, s.Errors)
}

// validateFile parses and validates a host file with no device contact, so a
// malformed file can never half-write a router.
func validateFile(r io.Reader) ([]types.Reservation, []error) {
	declarations, errs := ParseHosts(r)
	if len(errs) > 0 {
		return nil, errs
	}
	if dupes := findDuplicates(declarations); len(dupes) > 0 {
		return nil, dupes
	}
	return reservationsOf(declarations), nil
}

// runSet imports host declarations. Phases run in a deliberate order: all file
// validation, then all device reads, then writes.
func runSet(ctx context.Context, client *gogl.Client, path string, modes modeFlags) error {
	// Phase 1: offline validation.
	input := io.Reader(os.Stdin)
	if path != "" {
		f, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("open %s: %w", path, err)
		}
		defer f.Close()
		input = f
	}

	file, errs := validateFile(input)
	if len(errs) > 0 {
		for _, err := range errs {
			fmt.Fprintln(os.Stderr, "error:", err)
		}
		return fmt.Errorf("%d validation error(s); nothing was written", len(errs))
	}

	// Phase 2: read device state, once, so the diff is against one snapshot.
	network, err := client.Network().Get(ctx)
	if err != nil {
		return err
	}
	device, err := client.Reservations().List(ctx)
	if err != nil {
		return err
	}

	// Phase 3: reconcile against the device.
	warnings, errs := validateAgainstDevice(file, network)
	for _, w := range warnings {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}
	if len(errs) > 0 {
		for _, err := range errs {
			fmt.Fprintln(os.Stderr, "error:", err)
		}
		return errors.New("device validation failed; nothing was written")
	}

	plan := planChanges(file, device)

	if modes.dryRun {
		return printPlan(os.Stdout, plan, modes.prune)
	}

	// Phase 4: write, per entry.
	summary, err := applyPlan(ctx, os.Stderr, client.Reservations(), plan, modes.prune)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, summary)
	if summary.HasError() {
		return fmt.Errorf("%d entr(ies) failed", summary.Errors)
	}
	return nil
}

// applyPlan performs the writes. A per-entry failure is logged and counted, and
// the loop continues: one bad entry must not abandon the rest of a bulk import.
func applyPlan(ctx context.Context, log io.Writer, w reservationWriter, plan Plan, prune bool) (Summary, error) {
	var summary Summary

	for _, r := range plan.Skip {
		fmt.Fprintf(log, "skip    %s %s %s\n", r.Name, r.MAC, r.IP)
		summary.Skipped++
	}

	for i := range plan.Create {
		r := plan.Create[i]
		if _, err := w.Create(ctx, &r); err != nil {
			fmt.Fprintf(log, "error   %s %s %s: %v\n", r.Name, r.MAC, r.IP, err)
			summary.Errors++
			continue
		}
		fmt.Fprintf(log, "create  %s %s %s\n", r.Name, r.MAC, r.IP)
		summary.Created++
	}

	for i := range plan.Update {
		r := plan.Update[i]
		if _, err := w.Update(ctx, &r); err != nil {
			fmt.Fprintf(log, "error   %s %s %s: %v\n", r.Name, r.MAC, r.IP, err)
			summary.Errors++
			continue
		}
		fmt.Fprintf(log, "update  %s %s %s\n", r.Name, r.MAC, r.IP)
		summary.Updated++
	}

	if !prune {
		for _, r := range plan.Prune {
			fmt.Fprintf(log, "extra   %s %s %s (on router, absent from file; use --prune to delete)\n", r.Name, r.MAC, r.IP)
		}
		return summary, nil
	}

	for _, r := range plan.Prune {
		if err := w.Delete(ctx, r.MAC); err != nil {
			fmt.Fprintf(log, "error   %s %s: %v\n", r.Name, r.MAC, err)
			summary.Errors++
			continue
		}
		fmt.Fprintf(log, "prune   %s %s %s\n", r.Name, r.MAC, r.IP)
		summary.Pruned++
	}

	return summary, nil
}

func printPlan(w io.Writer, plan Plan, prune bool) error {
	for _, r := range plan.Create {
		fmt.Fprintf(w, "create  %s %s %s\n", r.Name, r.MAC, r.IP)
	}
	for _, r := range plan.Update {
		fmt.Fprintf(w, "update  %s %s %s\n", r.Name, r.MAC, r.IP)
	}
	for _, r := range plan.Skip {
		fmt.Fprintf(w, "skip    %s %s %s\n", r.Name, r.MAC, r.IP)
	}
	for _, r := range plan.Prune {
		action := "extra"
		if prune {
			action = "prune"
		}
		fmt.Fprintf(w, "%s   %s %s %s\n", action, r.Name, r.MAC, r.IP)
	}
	fmt.Fprintf(w, "\n%d create, %d update, %d skip, %d device-only\n",
		len(plan.Create), len(plan.Update), len(plan.Skip), len(plan.Prune))
	return nil
}
```

Delete the temporary `runSet` stub from `main.go`.

- [ ] **Step 4: Run the tests, then commit**

Run: `go test ./utilities/goglps/ -v -race`
Expected: PASS. `TestApplyPlanContinuesPastFailure` is the important one.

```bash
git add utilities/goglps/set.go utilities/goglps/set_test.go utilities/goglps/main.go
git commit -m "feat(goglps): add --set with four-phase import"
```

### Task 27: goglps --add and --del

**Files:**
- Create: `utilities/goglps/addel.go`
- Test: `utilities/goglps/addel_test.go`
- Modify: `utilities/goglps/main.go`

**Interfaces:**
- Consumes: `ParseHosts` (Task 23), `validateAgainstDevice` (Task 25), `reservationWriter` (Task 26), `ReservationService` (Task 17).
- Produces: `runAdd(ctx, client, fragment string, modes modeFlags) error`, `runDel(ctx, client, modes modeFlags) error`, `findTarget(device []types.Reservation, modes modeFlags) ([]types.Reservation, error)`. Leaf.

- [ ] **Step 1: Write the failing test**

`utilities/goglps/addel_test.go`:

```go
package main

import (
	"strings"
	"testing"

	"github.com/gherlein/gogl/src/types"
)

func deviceState() []types.Reservation {
	return []types.Reservation{
		{Name: "nas", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13"},
		{Name: "printer", MAC: "aa:bb:cc:dd:ee:02", IP: "192.168.8.14"},
	}
}

func TestFindTargetByName(t *testing.T) {
	got, err := findTarget(deviceState(), modeFlags{name: "nas"})
	if err != nil {
		t.Fatalf("findTarget error: %v", err)
	}
	if len(got) != 1 || got[0].MAC != "aa:bb:cc:dd:ee:01" {
		t.Errorf("got %v, want nas", got)
	}
}

func TestFindTargetByMAC(t *testing.T) {
	got, err := findTarget(deviceState(), modeFlags{mac: "AA:BB:CC:DD:EE:02"})
	if err != nil {
		t.Fatalf("findTarget error: %v", err)
	}
	if len(got) != 1 || got[0].Name != "printer" {
		t.Errorf("got %v, want printer", got)
	}
}

func TestFindTargetByIP(t *testing.T) {
	got, err := findTarget(deviceState(), modeFlags{ip: "192.168.8.13"})
	if err != nil {
		t.Fatalf("findTarget error: %v", err)
	}
	if len(got) != 1 || got[0].Name != "nas" {
		t.Errorf("got %v, want nas", got)
	}
}

func TestFindTargetRequiresExactlyOneIdentifier(t *testing.T) {
	for _, modes := range []modeFlags{
		{},
		{name: "nas", mac: "aa:bb:cc:dd:ee:01"},
		{name: "nas", ip: "192.168.8.13"},
	} {
		if _, err := findTarget(deviceState(), modes); err == nil {
			t.Errorf("findTarget(%+v) succeeded, want an error", modes)
		}
	}
}

func TestFindTargetNotFound(t *testing.T) {
	_, err := findTarget(deviceState(), modeFlags{name: "ghost"})
	if err == nil {
		t.Fatal("findTarget succeeded for a missing host")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error %q does not name the target", err)
	}
}

// Multiple matches are refused without --force, because deleting the wrong
// reservation is not recoverable from the tool.
func TestFindTargetMultipleMatches(t *testing.T) {
	device := []types.Reservation{
		{Name: "a", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13"},
		{Name: "b", MAC: "aa:bb:cc:dd:ee:02", IP: "192.168.8.13"},
	}

	if _, err := findTarget(device, modeFlags{ip: "192.168.8.13"}); err == nil {
		t.Error("findTarget accepted an ambiguous match without --force")
	}

	got, err := findTarget(device, modeFlags{ip: "192.168.8.13", force: true})
	if err != nil {
		t.Fatalf("findTarget with --force: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("got %d matches with --force, want 2", len(got))
	}
}

func TestParseFragment(t *testing.T) {
	const fragment = `host mydevice {
    hardware ethernet aa:bb:cc:dd:ee:ff;
    fixed-address 192.168.8.50;
}`
	got, err := parseFragment(strings.NewReader(fragment))
	if err != nil {
		t.Fatalf("parseFragment error: %v", err)
	}
	if got.Name != "mydevice" || got.IP != "192.168.8.50" {
		t.Errorf("got %+v", got)
	}
}

// A fragment must contain exactly one declaration: --add adds one host.
func TestParseFragmentRejectsMultiple(t *testing.T) {
	const fragment = `host a {
    hardware ethernet aa:bb:cc:dd:ee:01;
    fixed-address 192.168.8.50;
}
host b {
    hardware ethernet aa:bb:cc:dd:ee:02;
    fixed-address 192.168.8.51;
}`
	if _, err := parseFragment(strings.NewReader(fragment)); err == nil {
		t.Error("parseFragment accepted two declarations")
	}
}

func TestParseFragmentRejectsEmpty(t *testing.T) {
	if _, err := parseFragment(strings.NewReader("# nothing here\n")); err == nil {
		t.Error("parseFragment accepted an empty fragment")
	}
}

func TestCheckAddConflicts(t *testing.T) {
	device := deviceState()

	tests := []struct {
		name string
		res  types.Reservation
		want string
	}{
		{"ip taken", types.Reservation{Name: "new", MAC: "aa:bb:cc:dd:ee:09", IP: "192.168.8.13"}, "already reserved"},
		{"mac taken", types.Reservation{Name: "new", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.99"}, "already reserved"},
		{"name taken", types.Reservation{Name: "nas", MAC: "aa:bb:cc:dd:ee:09", IP: "192.168.8.99"}, "already used"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkAddConflicts(&tt.res, device)
			if err == nil {
				t.Fatalf("no conflict reported, want one mentioning %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not mention %q", err, tt.want)
			}
		})
	}
}

func TestCheckAddConflictsClean(t *testing.T) {
	res := types.Reservation{Name: "camera", MAC: "aa:bb:cc:dd:ee:09", IP: "192.168.8.99"}
	if err := checkAddConflicts(&res, deviceState()); err != nil {
		t.Errorf("checkAddConflicts = %v, want nil", err)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./utilities/goglps/ -run 'TestFindTarget|TestParseFragment|TestCheckAdd' -v`
Expected: FAIL, `undefined: findTarget`.

- [ ] **Step 3: Write the implementation**

`utilities/goglps/addel.go`:

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	gogl "github.com/gherlein/gogl/src"
	"github.com/gherlein/gogl/src/types"
	"golang.org/x/term"
)

// parseFragment reads exactly one host declaration.
func parseFragment(r io.Reader) (*types.Reservation, error) {
	declarations, errs := ParseHosts(r)
	if len(errs) > 0 {
		return nil, errs[0]
	}
	switch len(declarations) {
	case 0:
		return nil, errors.New("no host declaration found")
	case 1:
		return &declarations[0].Reservation, nil
	default:
		return nil, fmt.Errorf("expected one host declaration, found %d", len(declarations))
	}
}

// checkAddConflicts reports the first collision between res and the device.
func checkAddConflicts(res *types.Reservation, device []types.Reservation) error {
	for _, existing := range device {
		if strings.EqualFold(existing.MAC, res.MAC) && existing.IP != res.IP {
			return fmt.Errorf("MAC %s is already reserved for %s", res.MAC, existing.IP)
		}
		if existing.IP == res.IP && !strings.EqualFold(existing.MAC, res.MAC) {
			return fmt.Errorf("address %s is already reserved for %s", res.IP, existing.MAC)
		}
		if existing.Name == res.Name && !strings.EqualFold(existing.MAC, res.MAC) {
			return fmt.Errorf("hostname %q is already used by %s", res.Name, existing.MAC)
		}
	}
	return nil
}

func runAdd(ctx context.Context, client *gogl.Client, fragment string, modes modeFlags) error {
	input := io.Reader(os.Stdin)
	if fragment != "" {
		input = strings.NewReader(fragment)
	}

	res, err := parseFragment(input)
	if err != nil {
		return err
	}

	network, err := client.Network().Get(ctx)
	if err != nil {
		return err
	}
	warnings, errs := validateAgainstDevice([]types.Reservation{*res}, network)
	for _, w := range warnings {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}
	if len(errs) > 0 {
		return errs[0]
	}

	device, err := client.Reservations().List(ctx)
	if err != nil {
		return err
	}

	if !modes.force {
		if err := checkAddConflicts(res, device); err != nil {
			return fmt.Errorf("%w (use --force to override)", err)
		}
	}

	if modes.dryRun {
		fmt.Printf("Would add: %s %s %s\n", res.Name, res.MAC, res.IP)
		return nil
	}

	// An existing MAC becomes an update, so --add is usable to change an entry
	// rather than failing and forcing a delete first.
	existing := false
	for _, r := range device {
		if strings.EqualFold(r.MAC, res.MAC) {
			existing = true
			break
		}
	}

	verb := "Created"
	if existing {
		if _, err := client.Reservations().Update(ctx, res); err != nil {
			return err
		}
		verb = "Updated"
	} else if _, err := client.Reservations().Create(ctx, res); err != nil {
		return err
	}

	fmt.Printf("%s: %s %s %s\n", verb, res.Name, res.MAC, res.IP)
	return nil
}

// findTarget resolves exactly one identifier to the matching reservations.
func findTarget(device []types.Reservation, modes modeFlags) ([]types.Reservation, error) {
	identifiers := 0
	for _, id := range []string{modes.name, modes.mac, modes.ip} {
		if id != "" {
			identifiers++
		}
	}
	if identifiers != 1 {
		return nil, errors.New("exactly one of --name, --mac, or --ip is required")
	}

	var matches []types.Reservation
	switch {
	case modes.name != "":
		for _, r := range device {
			if r.Name == modes.name {
				matches = append(matches, r)
			}
		}
	case modes.mac != "":
		normalized, err := types.NormalizeMAC(modes.mac)
		if err != nil {
			return nil, err
		}
		for _, r := range device {
			if strings.EqualFold(r.MAC, normalized) {
				matches = append(matches, r)
			}
		}
	case modes.ip != "":
		for _, r := range device {
			if r.IP == modes.ip {
				matches = append(matches, r)
			}
		}
	}

	if len(matches) == 0 {
		return nil, fmt.Errorf("no reservation matches %s", describeTarget(modes))
	}
	// Refuse an ambiguous delete: removing the wrong reservation is not
	// recoverable from inside this tool.
	if len(matches) > 1 && !modes.force {
		var b strings.Builder
		fmt.Fprintf(&b, "%d reservations match %s:\n", len(matches), describeTarget(modes))
		for _, r := range matches {
			fmt.Fprintf(&b, "  %s %s %s\n", r.Name, r.MAC, r.IP)
		}
		b.WriteString("pass --force to delete all of them")
		return nil, errors.New(b.String())
	}

	return matches, nil
}

func describeTarget(modes modeFlags) string {
	switch {
	case modes.name != "":
		return fmt.Sprintf("hostname %q", modes.name)
	case modes.mac != "":
		return fmt.Sprintf("MAC %s", modes.mac)
	default:
		return fmt.Sprintf("address %s", modes.ip)
	}
}

func runDel(ctx context.Context, client *gogl.Client, modes modeFlags) error {
	device, err := client.Reservations().List(ctx)
	if err != nil {
		return err
	}

	targets, err := findTarget(device, modes)
	if err != nil {
		return err
	}

	for _, r := range targets {
		fmt.Fprintf(os.Stderr, "Will delete: %s %s %s\n", r.Name, r.MAC, r.IP)
	}
	if modes.dryRun {
		return nil
	}

	// Prompt only when a human is watching. In a pipeline, proceed: the caller
	// has already committed by invoking the tool non-interactively.
	if term.IsTerminal(int(os.Stdout.Fd())) && !modes.force {
		fmt.Fprint(os.Stderr, "Proceed? [y/N] ")
		var answer string
		if _, err := fmt.Fscanln(&answer); err != nil {
			return errors.New("aborted")
		}
		if !strings.EqualFold(strings.TrimSpace(answer), "y") {
			return errors.New("aborted")
		}
	}

	failures := 0
	for _, r := range targets {
		if err := client.Reservations().Delete(ctx, r.MAC); err != nil {
			fmt.Fprintf(os.Stderr, "error: delete %s: %v\n", r.MAC, err)
			failures++
			continue
		}
		fmt.Printf("Deleted: %s %s %s\n  Removed DHCP reservation and DNS name\n", r.Name, r.MAC, r.IP)
	}
	if failures > 0 {
		return fmt.Errorf("%d deletion(s) failed", failures)
	}
	return nil
}
```

- [ ] **Step 4: Add the terminal dependency**

```bash
go get golang.org/x/term@latest
```

- [ ] **Step 5: Delete the temporary stubs**

Remove the placeholder `runAdd` and `runDel` from `main.go`.

- [ ] **Step 6: Run the tests and build**

Run: `go test ./... -v -race && make build`
Expected: PASS, and `bin/goglps` exists.

- [ ] **Step 7: Verify against a real router**

```bash
./bin/goglps --get
./bin/goglps --get > /tmp/before.hosts
./bin/goglps --add 'host testdevice {
    hardware ethernet aa:bb:cc:dd:ee:fe;
    fixed-address 192.168.8.77;
}'
./bin/goglps --get
```

Then confirm the DNS side actually works, which is the claim the whole design rests on:

```bash
nslookup testdevice 192.168.8.1
```

Expected: `192.168.8.77`. If the name does not resolve, the reservation's name is not
reaching dnsmasq — recheck the Phase 0 payload field names before going further.

Clean up:

```bash
./bin/goglps --del --name testdevice
./bin/goglps --set /tmp/before.hosts
```

- [ ] **Step 8: Commit**

```bash
git add utilities/goglps/ go.mod go.sum
git commit -m "feat(goglps): add --add and --del"
```

---

## Phase 9: Interoperability, Examples, Documentation

### Task 28: gofips interoperability test

**Files:**
- Create: `utilities/goglps/testdata/gofips-export.hosts`
- Test: `utilities/goglps/interop_test.go`

**Interfaces:**
- Consumes: `ParseHosts` (Task 23), `FormatHosts` (Task 24).
- Produces: no API. This is the contract test between `gofi` and `gogl`, and the only thing that would catch `gofips` changing its output format.

- [ ] **Step 1: Capture real gofips output**

If a UniFi controller is reachable, capture genuine output:

```bash
gofips -H "$UNIFI_CONTROLLER_IP" -k --get > utilities/goglps/testdata/gofips-export.hosts
```

Otherwise write the file by hand to exactly match `gofips`'s documented format, including its
header comment style:

```
# gofips fixed IP assignments
# exported from UDM at 192.168.4.1
# date: 2026-07-27

host myserver {
    hardware ethernet aa:bb:cc:dd:ee:01;
    fixed-address 192.168.4.10;
}

host printer {
    hardware ethernet aa:bb:cc:dd:ee:02;
    fixed-address 192.168.4.11;
}

host nas {
    hardware ethernet aa:bb:cc:dd:ee:03;
    fixed-address 192.168.4.13;
}
```

- [ ] **Step 2: Write the test**

`utilities/goglps/interop_test.go`:

```go
package main

import (
	"bytes"
	"os"
	"testing"
)

// The interoperability contract with gofi. A gofips export must parse here
// without modification; this test is the only thing that would catch gofips
// changing its output format.
func TestParsesRealGofipsExport(t *testing.T) {
	data, err := os.ReadFile("testdata/gofips-export.hosts")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	got, errs := ParseHosts(bytes.NewReader(data))
	if len(errs) != 0 {
		t.Fatalf("gofips export did not parse: %v", errs)
	}
	if len(got) != 3 {
		t.Fatalf("parsed %d declarations, want 3", len(got))
	}

	want := map[string]string{
		"myserver": "192.168.4.10",
		"printer":  "192.168.4.11",
		"nas":      "192.168.4.13",
	}
	for _, d := range got {
		wantIP, ok := want[d.Reservation.Name]
		if !ok {
			t.Errorf("unexpected host %q", d.Reservation.Name)
			continue
		}
		if d.Reservation.IP != wantIP {
			t.Errorf("host %q has IP %q, want %q", d.Reservation.Name, d.Reservation.IP, wantIP)
		}
		delete(want, d.Reservation.Name)
	}
	for name := range want {
		t.Errorf("host %q was not parsed", name)
	}
}

// Our output must be re-readable by the same rules, so a file can move back and
// forth between the two tools.
func TestOutputIsGofipsCompatible(t *testing.T) {
	data, err := os.ReadFile("testdata/gofips-export.hosts")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	parsed, errs := ParseHosts(bytes.NewReader(data))
	if len(errs) != 0 {
		t.Fatalf("parse: %v", errs)
	}

	var buf bytes.Buffer
	if err := FormatHosts(&buf, reservationsOf(parsed), Header{Host: "192.168.4.1", Date: "2026-07-27"}); err != nil {
		t.Fatalf("FormatHosts: %v", err)
	}

	reparsed, errs := ParseHosts(bytes.NewReader(buf.Bytes()))
	if len(errs) != 0 {
		t.Fatalf("our own output did not re-parse: %v", errs)
	}
	if len(reparsed) != len(parsed) {
		t.Errorf("re-parsed %d declarations, want %d", len(reparsed), len(parsed))
	}
}
```

- [ ] **Step 3: Run, then commit**

Run: `go test ./utilities/goglps/ -run Interop -v` and
`go test ./utilities/goglps/ -run 'TestParsesReal|TestOutputIs' -v`
Expected: PASS.

```bash
git add utilities/goglps/testdata/ utilities/goglps/interop_test.go
git commit -m "test(goglps): add gofips interoperability contract test"
```

### Task 29: Example programs

**Files:**
- Create: `examples/basic/main.go`
- Create: `examples/list/main.go`
- Create: `examples/reservations/main.go`

**Interfaces:**
- Consumes: the whole public API.
- Produces: three runnable examples. `make examples-test` compiles them.

- [ ] **Step 1: Write examples/basic**

`examples/basic/main.go`:

```go
// Command basic connects to a GL.iNet router and prints its identity.
//
// Run with:
//
//	GL_ROUTER_IP=192.168.8.1 GL_PASSWORD=... go run ./examples/basic
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	gogl "github.com/gherlein/gogl/src"
)

func main() {
	client, err := gogl.New(gogl.Config{
		Host:     os.Getenv("GL_ROUTER_IP"),
		Password: os.Getenv("GL_PASSWORD"),
	})
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	info, err := client.System().Info(context.Background())
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("model:    %s\n", info.Model)
	fmt.Printf("firmware: %s\n", info.Firmware)
	fmt.Printf("uptime:   %d seconds\n", info.Uptime)
}
```

- [ ] **Step 2: Write examples/list**

`examples/list/main.go`:

```go
// Command list prints the router's network configuration, reservations, and
// connected clients.
//
// Run with:
//
//	GL_ROUTER_IP=192.168.8.1 GL_PASSWORD=... go run ./examples/list
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	gogl "github.com/gherlein/gogl/src"
)

func main() {
	client, err := gogl.New(gogl.Config{
		Host:     os.Getenv("GL_ROUTER_IP"),
		Password: os.Getenv("GL_PASSWORD"),
	})
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	ctx := context.Background()

	network, err := client.Network().Get(ctx)
	if err != nil {
		log.Fatal(err)
	}
	subnet, err := network.Subnet()
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("LAN:   %s\n", subnet)
	fmt.Printf("pool:  %s - %s (%d addresses)\n", network.DHCPStart, network.DHCPStop, network.PoolSize())
	fmt.Printf("lease: %s\n", network.DHCPLease)

	reservations, err := client.Reservations().List(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("\n%d reservations:\n", len(reservations))
	for _, r := range reservations {
		fmt.Printf("  %-20s %s  %s\n", r.Name, r.MAC, r.IP)
	}

	clients, err := client.Clients().List(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("\n%d clients:\n", len(clients))
	for _, c := range clients {
		wiring := "wifi"
		if c.IsWired {
			wiring = "wired"
		}
		fmt.Printf("  %-20s %s  %-15s %s\n", c.Hostname(), c.MAC, c.IP, wiring)
	}
}
```

- [ ] **Step 3: Write examples/reservations**

`examples/reservations/main.go`:

```go
// Command reservations demonstrates the full reservation lifecycle: create,
// read, update, delete.
//
// It uses an address near the top of the subnet and a locally-administered MAC
// that no real device will have, so it is safe to run against a live router.
//
// Run with:
//
//	GL_ROUTER_IP=192.168.8.1 GL_PASSWORD=... go run ./examples/reservations
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"

	gogl "github.com/gherlein/gogl/src"
	"github.com/gherlein/gogl/src/types"
)

const (
	demoName = "gogl-demo"
	demoMAC  = "02:00:00:de:m0:01"
)

func main() {
	client, err := gogl.New(gogl.Config{
		Host:     os.Getenv("GL_ROUTER_IP"),
		Password: os.Getenv("GL_PASSWORD"),
	})
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	ctx := context.Background()

	// Pick an address inside the subnet but outside the DHCP pool.
	network, err := client.Network().Get(ctx)
	if err != nil {
		log.Fatal(err)
	}
	subnet, err := network.Subnet()
	if err != nil {
		log.Fatal(err)
	}
	base := subnet.IP.To4()
	if base == nil {
		log.Fatal("router LAN is not IPv4")
	}
	demoIP := fmt.Sprintf("%d.%d.%d.250", base[0], base[1], base[2])

	reservations := client.Reservations()

	fmt.Printf("creating %s -> %s\n", demoName, demoIP)
	created, err := reservations.Create(ctx, &types.Reservation{
		Name: demoName, MAC: demoMAC, IP: demoIP,
	})
	if err != nil {
		log.Fatal(err)
	}

	// Clean up even if a later step fails, so a demo never leaves debris.
	defer func() {
		if err := reservations.Delete(ctx, demoMAC); err != nil {
			log.Printf("cleanup failed, remove %s by hand: %v", demoName, err)
			return
		}
		fmt.Printf("deleted %s\n", demoName)
	}()

	fetched, err := reservations.GetByName(ctx, demoName)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("read back: %s %s %s\n", fetched.Name, fetched.MAC, fetched.IP)
	fmt.Printf("the router now answers DNS for %q at %s\n", demoName, fetched.IP)

	// Creating the same MAC twice is a conflict, not a silent overwrite.
	if _, err := reservations.Create(ctx, created); !errors.Is(err, gogl.ErrConflict) {
		log.Printf("expected ErrConflict on a duplicate create, got: %v", err)
	} else {
		fmt.Println("duplicate create correctly refused")
	}

	// A name that would corrupt dnsmasq's config is refused by the library.
	if _, err := reservations.Create(ctx, &types.Reservation{
		Name: `bad"name`, MAC: "02:00:00:de:m0:02", IP: demoIP,
	}); errors.Is(err, gogl.ErrInvalidName) {
		fmt.Println("unsafe name correctly refused")
	} else {
		log.Printf("expected ErrInvalidName, got: %v", err)
	}
}
```

The demo MAC above is not valid hex. Replace `m0` with `a0` in both constants and in the
second create so it parses: `02:00:00:de:a0:01` and `02:00:00:de:a0:02`.

- [ ] **Step 4: Verify they compile and run**

Run: `make examples-test && make examples`
Expected: all three compile.

```bash
export GL_ROUTER_IP=192.168.8.1
export GL_PASSWORD='<your router password>'
./bin/basic
./bin/list
./bin/reservations
```

Expected: `reservations` creates, reads, refuses both bad cases, and deletes, leaving the
router as it started. Confirm with `./bin/goglps --get`.

- [ ] **Step 5: Commit**

```bash
git add examples/
git commit -m "docs(examples): add basic, list, and reservations examples"
```

### Task 30: README

**Files:**
- Create: `README.md` (if Phase 9 finds it absent or stale)

**Interfaces:**
- Consumes: everything.
- Produces: the repository's front door.

- [ ] **Step 1: Check whether README.md already matches the implementation**

The README written before implementation describes intended behavior. Reconcile it against
what was built:

```bash
grep -n 'goglps\|goglnet\|goglmac\|GL_' README.md
./bin/goglnet --help 2>&1 | head -30
./bin/goglps --help 2>&1 | head -40
./bin/goglmac --help 2>&1 | head -30
```

- [ ] **Step 2: Correct every divergence**

Any flag, environment variable, or output sample in the README that differs from the built
binaries is a bug in the README. Fix the README, not the code, unless the code is wrong.

Check specifically:
- Flag names and shorthands against each `--help`.
- The `GL_PASSWORD` / `GL_USERNAME` / `GL_ROUTER_IP` names.
- Sample output, regenerated from a real run rather than edited by hand.
- The two-command workflow, run end to end at least once.
- Whether anything documented as unsupported turned out to be supported, or the reverse.

- [ ] **Step 3: Update the Phase 0 findings**

If Phase 0 revealed that writes need a separate apply step, or that a field name differs from
what the plan assumed, record it in the README's limitations section and in
`GL_INET_4X_API_DOCUMENTATION.md`.

- [ ] **Step 4: Run the full suite one last time**

Run: `make all`
Expected: lint clean, all tests pass with `-race`, everything builds.

Run: `make coverage && go tool cover -func=coverage.out | tail -1`
Expected: 100% of written code. Anything below is either dead code to delete or a missing
test.

- [ ] **Step 5: Commit**

```bash
git add README.md GL_INET_4X_API_DOCUMENTATION.md
git commit -m "docs: reconcile README with implemented behavior"
```

---

## Plan Self-Review

Checked against `VISION.md` after writing.

**Spec coverage.** Every `VISION.md` section maps to a task: target device (Task 3),
auth flow (Tasks 8, 11), session lifetime (Task 12), API discovery (Tasks 1, 2), unified
reservation (Tasks 6, 17), name validation (Task 6), type patterns (Tasks 5, 6, 7), mock
server (Tasks 9, 10), replication workflow (Tasks 23, 26, 28), `goglps` all four modes
(Tasks 25, 26, 27), `goglnet` (Task 20), `goglmac` (Tasks 21, 22), common CLI conventions
(Task 19), Makefile (Task 3).

**Known gaps, deliberate.** `VISION.md` mentions guest-network reporting in `goglnet` as
conditional on the API exposing it; no task implements it, because Phase 0 has to establish
whether it is reachable first. Add it to Task 20 if the fixture shows it. `VISION.md` also
describes a `--dry-run` for every mode; Tasks 25 through 27 implement it for `--set`,
`--add` and `--del` but `--get` has nothing to dry-run, which is correct rather than missing.

**Type consistency.** `types.Reservation`, `types.Network`, `types.Client`,
`types.SystemInfo`, `types.LeaseTime` are defined once and used with the same field names
throughout. `reservationLister` and `networkGetter` are declared in both
`utilities/goglnet/operations.go` and `utilities/goglps/operations.go` — these are separate
`main` packages, so the duplication is required, not accidental.

**One risk carried forward.** Every group and method constant in Phases 5 onward is a
placeholder from Phase 0. They are declared in `src/mock/handlers.go` and in each service
file so that correcting them touches a handful of lines. If Phase 0 shows that reservation
writes need an apply step, Task 17's `replace` and Task 26's `applyPlan` are the two places
that change.
