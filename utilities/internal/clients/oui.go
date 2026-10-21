package clients

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// OUIURL is the IEEE's canonical database.
	OUIURL = "https://standards-oui.ieee.org/oui/oui.txt"

	ouiFilename = "oui.txt"

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

	// scannerMaxLine is generous because the IEEE file carries long address
	// lines; the default would error on them.
	scannerMaxLine = 1024 * 1024

	// userAgent must be set explicitly. IEEE fronts the OUI file with a bot
	// Filter that answers Go's default "Go-http-client/1.1" with HTTP 418, which
	// looks exactly like a broken network but is not. Any honest identifier gets
	// through; this one does not impersonate a browser.
	userAgent = "goglmac (+https://github.com/gherlein/gogl)"

	// downloadTimeout bounds the fetch. Without it a captive portal -- the normal
	// state of the hotel network this tool exists to sit behind -- can hang the
	// request indefinitely.
	downloadTimeout = 60 * time.Second
)

// fetcher returns the OUI file's contents. Injected so tests never touch the
// network.
type fetcher func() (string, error)

// OUIDatabase maps a lowercase colon-separated 3-octet prefix to a manufacturer.
type OUIDatabase map[string]string

// LoadOUI returns the database, downloading it when the cache is missing or older
// than maxCacheAge.
//
// A download failure with any cache present is a warning and the cache is used; a
// download failure with no cache is fatal. That matches gofimac. The realistic
// path is that the tool ran at least once with a network connection.
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
	fmt.Fprintf(os.Stderr, "downloading OUI database from %s ...\n", OUIURL)
	return fetchOUI(OUIURL)
}

// fetchOUI performs the download. Split from downloadOUI so a test can point it
// at a local server and assert on the headers actually sent.
func fetchOUI(url string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), downloadTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s returned %s", url, resp.Status)
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
	scanner.Buffer(make([]byte, 0, 64*1024), scannerMaxLine)

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

// Lookup returns the manufacturer for mac's OUI prefix.
//
// A locally-administered address returns "randomized" rather than "unknown",
// because it is not a missing entry: such an address will never be registered,
// and is a poor choice to reserve an address for.
func (db OUIDatabase) Lookup(mac string) string {
	octets := strings.Split(strings.ToLower(strings.TrimSpace(mac)), ":")
	if len(octets) < ouiPrefixOctets {
		return unknownManufacturer
	}

	prefix := strings.Join(octets[:ouiPrefixOctets], ":")
	if organization, ok := db[prefix]; ok {
		return organization
	}

	first, err := strconv.ParseUint(octets[0], 16, 8)
	if err == nil && first&localAdminBit != 0 {
		return randomizedManufacturer
	}
	return unknownManufacturer
}
