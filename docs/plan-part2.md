# gogl Implementation Plan — Part 2: Phases 3 and 4

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
Continuation of [`plan.md`](plan.md). Task numbering resumes at 9. Execute `plan.md` in full
first. **Global Constraints from `plan.md` apply to every task here.**

---

## Phase 3: Mock Server

The mock is built before the transport that talks to it, because the transport's tests are
the only way to prove the transport works. It must reproduce the protocol's hostile
behaviors, not just serve happy-path payloads — a mock that never expires a nonce cannot
catch the bug that a real router would.

### Task 9: Mock server core

**Files:**
- Create: `src/mock/server.go`
- Test: `src/mock/server_test.go`

**Interfaces:**
- Consumes: `auth.Cipher`, `auth.LoginHash`, `auth.AlgMD5|AlgSHA256|AlgSHA512` (Task 8).
- Produces: `mock.NewServer(t *testing.T, opts Options) *Server`, `(*Server).URL() string`, `(*Server).Close()`, and `mock.Options{Username, Password string; Alg int; AlgAsString bool; NonceTTL, SessionTTL time.Duration}`. Tasks 10 through 18 all construct servers with it.

- [ ] **Step 1: Write the failing test**

`src/mock/server_test.go`:

```go
package mock

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gherlein/gogl/src/auth"
)

// post is a raw JSON-RPC helper so the mock's tests do not depend on the
// transport, which does not exist yet.
func post(t *testing.T, url string, body any) map[string]any {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := http.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func challenge(t *testing.T, url, username string) (alg int, salt, nonce string) {
	t.Helper()
	out := post(t, url, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "challenge",
		"params": map[string]any{"username": username},
	})
	result, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("challenge returned no result: %v", out)
	}

	switch v := result["alg"].(type) {
	case float64:
		alg = int(v)
	case string:
		if _, err := fmt.Sscanf(v, "%d", &alg); err != nil {
			t.Fatalf("alg %q not numeric: %v", v, err)
		}
	default:
		t.Fatalf("alg has unexpected type %T", v)
	}
	salt, _ = result["salt"].(string)
	nonce, _ = result["nonce"].(string)
	return alg, salt, nonce
}

func TestChallengeReturnsAlgSaltNonce(t *testing.T) {
	s := NewServer(t, Options{Password: "secret"})
	alg, salt, nonce := challenge(t, s.URL(), "root")

	if alg != auth.AlgSHA512 {
		t.Errorf("alg = %d, want %d (the default)", alg, auth.AlgSHA512)
	}
	if salt == "" {
		t.Error("salt is empty")
	}
	if nonce == "" {
		t.Error("nonce is empty")
	}
}

// Each challenge must mint a fresh nonce, or replay protection is absent.
func TestChallengeMintsFreshNonce(t *testing.T) {
	s := NewServer(t, Options{Password: "secret"})
	_, _, first := challenge(t, s.URL(), "root")
	_, _, second := challenge(t, s.URL(), "root")
	if first == second {
		t.Error("two challenges returned the same nonce")
	}
}

// The salt must be stable, or a client cannot cache the expensive cipher.
func TestChallengeSaltIsStable(t *testing.T) {
	s := NewServer(t, Options{Password: "secret"})
	_, first, _ := challenge(t, s.URL(), "root")
	_, second, _ := challenge(t, s.URL(), "root")
	if first != second {
		t.Errorf("salt changed between challenges: %q then %q", first, second)
	}
}

func TestLoginSucceedsWithCorrectDigest(t *testing.T) {
	s := NewServer(t, Options{Password: "secret"})
	alg, salt, nonce := challenge(t, s.URL(), "root")

	cipher, err := auth.Cipher("secret", salt, alg)
	if err != nil {
		t.Fatalf("Cipher: %v", err)
	}
	out := post(t, s.URL(), map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "login",
		"params": map[string]any{"username": "root", "hash": auth.LoginHash("root", cipher, nonce)},
	})

	result, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("login returned no result: %v", out)
	}
	if sid, _ := result["sid"].(string); sid == "" {
		t.Error("login returned an empty sid")
	}
}

func TestLoginRejectsWrongPassword(t *testing.T) {
	s := NewServer(t, Options{Password: "secret"})
	alg, salt, nonce := challenge(t, s.URL(), "root")

	cipher, err := auth.Cipher("wrong", salt, alg)
	if err != nil {
		t.Fatalf("Cipher: %v", err)
	}
	out := post(t, s.URL(), map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "login",
		"params": map[string]any{"username": "root", "hash": auth.LoginHash("root", cipher, nonce)},
	})
	if _, ok := out["error"]; !ok {
		t.Errorf("login with wrong password succeeded: %v", out)
	}
}

// A client that caches a challenge and reuses its nonce must fail here exactly
// as it would against hardware.
func TestLoginRejectsExpiredNonce(t *testing.T) {
	s := NewServer(t, Options{Password: "secret", NonceTTL: 10 * time.Millisecond})
	alg, salt, nonce := challenge(t, s.URL(), "root")

	cipher, err := auth.Cipher("secret", salt, alg)
	if err != nil {
		t.Fatalf("Cipher: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	out := post(t, s.URL(), map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "login",
		"params": map[string]any{"username": "root", "hash": auth.LoginHash("root", cipher, nonce)},
	})
	if _, ok := out["error"]; !ok {
		t.Errorf("login with expired nonce succeeded: %v", out)
	}
}

// A nonce is single-use.
func TestLoginRejectsReusedNonce(t *testing.T) {
	s := NewServer(t, Options{Password: "secret"})
	alg, salt, nonce := challenge(t, s.URL(), "root")
	cipher, err := auth.Cipher("secret", salt, alg)
	if err != nil {
		t.Fatalf("Cipher: %v", err)
	}
	body := map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "login",
		"params": map[string]any{"username": "root", "hash": auth.LoginHash("root", cipher, nonce)},
	}

	if out := post(t, s.URL(), body); out["result"] == nil {
		t.Fatalf("first login failed: %v", out)
	}
	if out := post(t, s.URL(), body); out["error"] == nil {
		t.Errorf("second login with the same nonce succeeded: %v", out)
	}
}

func TestAlgAsString(t *testing.T) {
	s := NewServer(t, Options{Password: "secret", AlgAsString: true})
	out := post(t, s.URL(), map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "challenge",
		"params": map[string]any{"username": "root"},
	})
	result := out["result"].(map[string]any)
	if _, ok := result["alg"].(string); !ok {
		t.Errorf("alg is %T, want string when AlgAsString is set", result["alg"])
	}
}

func TestAliveRequiresValidSession(t *testing.T) {
	s := NewServer(t, Options{Password: "secret"})
	out := post(t, s.URL(), map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "alive",
		"params": map[string]any{"sid": "bogus-sid"},
	})
	if _, ok := out["error"]; !ok {
		t.Errorf("alive with a bogus sid succeeded: %v", out)
	}
}

func TestAllAlgorithms(t *testing.T) {
	for _, alg := range []int{auth.AlgMD5, auth.AlgSHA256, auth.AlgSHA512} {
		s := NewServer(t, Options{Password: "secret", Alg: alg})
		gotAlg, salt, nonce := challenge(t, s.URL(), "root")
		if gotAlg != alg {
			t.Errorf("alg = %d, want %d", gotAlg, alg)
		}
		cipher, err := auth.Cipher("secret", salt, gotAlg)
		if err != nil {
			t.Fatalf("Cipher(alg=%d): %v", alg, err)
		}
		out := post(t, s.URL(), map[string]any{
			"jsonrpc": "2.0", "id": 2, "method": "login",
			"params": map[string]any{"username": "root", "hash": auth.LoginHash("root", cipher, nonce)},
		})
		if out["result"] == nil {
			t.Errorf("login failed for alg %d: %v", alg, out)
		}
	}
}
```

Add `"fmt"` to the test file's imports.

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./src/mock/ -v`
Expected: FAIL, `undefined: NewServer`.

- [ ] **Step 3: Write the mock core**

`src/mock/server.go`:

```go
// Package mock provides an in-process JSON-RPC server that behaves like a
// GL.iNet router, including its authentication quirks. Tests run against it
// rather than hardware, which is why rule 5 forbids SSH and UCI: everything
// gogl does must be reachable this way.
package mock

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/gherlein/gogl/src/auth"
)

// Defaults mirror the real device. Tests shorten the TTLs to drive expiry
// deterministically rather than by sleeping for tens of seconds.
const (
	DefaultUsername   = "root"
	DefaultNonceTTL   = 1 * time.Second
	DefaultSessionTTL = 35 * time.Second
	defaultSalt       = "abcdefgh"

	codeAccessDenied = -32000
	codeNotFound     = -32001
	codeBadRequest   = -32600
)

// Options configures a Server.
type Options struct {
	Username string
	Password string

	// Alg selects the crypt algorithm advertised by challenge, so tests can
	// exercise the MD5, SHA-256 and SHA-512 paths. Defaults to SHA-512.
	Alg int

	// AlgAsString makes challenge emit alg as a JSON string rather than a
	// number, reproducing observed firmware variation.
	AlgAsString bool

	NonceTTL   time.Duration
	SessionTTL time.Duration
}

type nonceRecord struct {
	issued time.Time
	used   bool
}

// Server is an httptest-backed /rpc endpoint.
type Server struct {
	t    *testing.T
	http *httptest.Server
	opts Options

	mu       sync.Mutex
	nonces   map[string]*nonceRecord
	nonceSeq int
	sid      string
	sidSeen  time.Time

	// state holds per-group fixture payloads, populated by Task 10.
	state map[string]json.RawMessage
	// failures holds one-shot injected errors keyed by "group.method".
	failures map[string]*RPCFault
}

// RPCFault is an injected error, returned once for the next matching call.
type RPCFault struct {
	Code    int
	Message string
}

// NewServer starts a mock router and registers cleanup with t.
func NewServer(t *testing.T, opts Options) *Server {
	t.Helper()

	if opts.Username == "" {
		opts.Username = DefaultUsername
	}
	if opts.Alg == 0 {
		opts.Alg = auth.AlgSHA512
	}
	if opts.NonceTTL == 0 {
		opts.NonceTTL = DefaultNonceTTL
	}
	if opts.SessionTTL == 0 {
		opts.SessionTTL = DefaultSessionTTL
	}

	s := &Server{
		t:        t,
		opts:     opts,
		nonces:   make(map[string]*nonceRecord),
		state:    make(map[string]json.RawMessage),
		failures: make(map[string]*RPCFault),
	}
	s.http = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.http.Close)
	return s
}

func (s *Server) URL() string { return s.http.URL + "/rpc" }

func (s *Server) Close() { s.http.Close() }

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	var req request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, nil, codeBadRequest, "malformed request")
		return
	}

	switch req.Method {
	case "challenge":
		s.handleChallenge(w, &req)
	case "login":
		s.handleLogin(w, &req)
	case "alive":
		s.handleAlive(w, &req)
	case "call":
		s.handleCall(w, &req)
	default:
		s.writeError(w, req.ID, codeBadRequest, "unknown method "+req.Method)
	}
}

func (s *Server) handleChallenge(w http.ResponseWriter, req *request) {
	var params struct {
		Username string `json:"username"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		s.writeError(w, req.ID, codeBadRequest, "bad challenge params")
		return
	}
	if params.Username != s.opts.Username {
		s.writeError(w, req.ID, codeAccessDenied, "unknown user")
		return
	}

	s.mu.Lock()
	s.nonceSeq++
	nonce := fmt.Sprintf("nonce-%d", s.nonceSeq)
	s.nonces[nonce] = &nonceRecord{issued: time.Now()}
	s.mu.Unlock()

	result := map[string]any{
		"salt":  defaultSalt,
		"nonce": nonce,
	}
	// The salt is stable across challenges so a client can cache the expensive
	// cipher; only the nonce rotates.
	if s.opts.AlgAsString {
		result["alg"] = strconv.Itoa(s.opts.Alg)
	} else {
		result["alg"] = s.opts.Alg
	}
	s.writeResult(w, req.ID, result)
}

func (s *Server) handleLogin(w http.ResponseWriter, req *request) {
	var params struct {
		Username string `json:"username"`
		Hash     string `json:"hash"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		s.writeError(w, req.ID, codeBadRequest, "bad login params")
		return
	}

	cipher, err := auth.Cipher(s.opts.Password, defaultSalt, s.opts.Alg)
	if err != nil {
		s.t.Fatalf("mock: cipher: %v", err)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Find the nonce this digest was computed against. The real device tracks
	// the outstanding nonce; reproducing that is what makes reuse and expiry
	// testable.
	var matched string
	for nonce, rec := range s.nonces {
		if auth.LoginHash(params.Username, cipher, nonce) == params.Hash {
			matched, _ = nonce, rec
			break
		}
	}
	if matched == "" {
		s.writeErrorLocked(w, req.ID, codeAccessDenied, "invalid credentials")
		return
	}

	rec := s.nonces[matched]
	if rec.used {
		s.writeErrorLocked(w, req.ID, codeAccessDenied, "nonce already used")
		return
	}
	if time.Since(rec.issued) > s.opts.NonceTTL {
		s.writeErrorLocked(w, req.ID, codeAccessDenied, "nonce expired")
		return
	}
	rec.used = true

	s.sid = fmt.Sprintf("sid-%d", s.nonceSeq)
	s.sidSeen = time.Now()
	s.writeResultLocked(w, req.ID, map[string]any{"sid": s.sid, "username": params.Username})
}

func (s *Server) handleAlive(w http.ResponseWriter, req *request) {
	var params struct {
		SID string `json:"sid"`
	}
	// alive is observed with the sid both as a bare param object and as the
	// first element of a params array; accept either.
	if err := json.Unmarshal(req.Params, &params); err != nil {
		var arr []json.RawMessage
		if err := json.Unmarshal(req.Params, &arr); err != nil || len(arr) == 0 {
			s.writeError(w, req.ID, codeBadRequest, "bad alive params")
			return
		}
		if err := json.Unmarshal(arr[0], &params.SID); err != nil {
			s.writeError(w, req.ID, codeBadRequest, "bad alive sid")
			return
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.sessionValidLocked(params.SID) {
		s.writeErrorLocked(w, req.ID, codeAccessDenied, "Access denied")
		return
	}
	s.sidSeen = time.Now()
	s.writeResultLocked(w, req.ID, map[string]any{"alive": true})
}

// sessionValidLocked reports whether sid is the current session and has not
// idled out. Callers must hold s.mu.
func (s *Server) sessionValidLocked(sid string) bool {
	if sid == "" || sid != s.sid {
		return false
	}
	return time.Since(s.sidSeen) <= s.opts.SessionTTL
}

func (s *Server) writeResult(w http.ResponseWriter, id json.RawMessage, result any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writeResultLocked(w, id, result)
}

func (s *Server) writeResultLocked(w http.ResponseWriter, id json.RawMessage, result any) {
	s.encode(w, map[string]any{"jsonrpc": "2.0", "id": rawOrNull(id), "result": result})
}

func (s *Server) writeError(w http.ResponseWriter, id json.RawMessage, code int, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writeErrorLocked(w, id, code, message)
}

func (s *Server) writeErrorLocked(w http.ResponseWriter, id json.RawMessage, code int, message string) {
	s.encode(w, map[string]any{
		"jsonrpc": "2.0", "id": rawOrNull(id),
		"error": map[string]any{"code": code, "message": message},
	})
}

func (s *Server) encode(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		s.t.Errorf("mock: encode response: %v", err)
	}
}

func rawOrNull(id json.RawMessage) any {
	if len(id) == 0 {
		return nil
	}
	return id
}
```

- [ ] **Step 4: Stub handleCall so the package compiles**

Task 10 replaces this. It exists now only so Phase 3's tests can run.

```go
func (s *Server) handleCall(w http.ResponseWriter, req *request) {
	s.writeError(w, req.ID, codeNotFound, "no groups registered yet")
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./src/mock/ -v -race`
Expected: PASS, all ten tests.

- [ ] **Step 6: Commit**

```bash
git add src/mock/server.go src/mock/server_test.go
git commit -m "feat(mock): add JSON-RPC server with nonce and session expiry"
```

### Task 10: Mock call dispatch, fixtures, and fault injection

**Files:**
- Modify: `src/mock/server.go` (replace the `handleCall` stub)
- Create: `src/mock/handlers.go`
- Test: `src/mock/handlers_test.go`

**Interfaces:**
- Consumes: `mock.Server` (Task 9), `types.Reservation` (Task 6).
- Produces: `(*Server).LoadFixture(group, method string, payload json.RawMessage)`, `(*Server).SetReservations([]types.Reservation)`, `(*Server).Reservations() []types.Reservation`, `(*Server).FailNext(group, method string, code int, message string)`. Tasks 12 through 18 use all four.

Group and method name constants come from Phase 0. Until then the mock accepts any
group/method for which a fixture has been loaded, which is what keeps Phases 3 and 4
independent of discovery.

- [ ] **Step 1: Write the failing test**

`src/mock/handlers_test.go`:

```go
package mock

import (
	"encoding/json"
	"testing"

	"github.com/gherlein/gogl/src/auth"
	"github.com/gherlein/gogl/src/types"
)

// authenticate performs the full two-challenge login and returns the sid.
func authenticate(t *testing.T, s *Server) string {
	t.Helper()
	alg, salt, nonce := challenge(t, s.URL(), s.opts.Username)
	cipher, err := auth.Cipher(s.opts.Password, salt, alg)
	if err != nil {
		t.Fatalf("Cipher: %v", err)
	}
	out := post(t, s.URL(), map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "login",
		"params": map[string]any{
			"username": s.opts.Username,
			"hash":     auth.LoginHash(s.opts.Username, cipher, nonce),
		},
	})
	result, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("login failed: %v", out)
	}
	sid, _ := result["sid"].(string)
	if sid == "" {
		t.Fatal("empty sid")
	}
	return sid
}

func callRPC(t *testing.T, s *Server, sid, group, method string, args any) map[string]any {
	t.Helper()
	return post(t, s.URL(), map[string]any{
		"jsonrpc": "2.0", "id": 7, "method": "call",
		"params": []any{sid, group, method, args},
	})
}

func TestCallReturnsLoadedFixture(t *testing.T) {
	s := NewServer(t, Options{Password: "secret"})
	s.LoadFixture("lan", "get_config", json.RawMessage(`{"lan_ip":"192.168.8.1"}`))
	sid := authenticate(t, s)

	out := callRPC(t, s, sid, "lan", "get_config", map[string]any{})
	result, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %v", out)
	}
	if result["lan_ip"] != "192.168.8.1" {
		t.Errorf("lan_ip = %v, want 192.168.8.1", result["lan_ip"])
	}
}

func TestCallRejectsBadSID(t *testing.T) {
	s := NewServer(t, Options{Password: "secret"})
	s.LoadFixture("lan", "get_config", json.RawMessage(`{}`))
	authenticate(t, s)

	out := callRPC(t, s, "not-the-sid", "lan", "get_config", map[string]any{})
	errObj, ok := out["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected an error, got %v", out)
	}
	if int(errObj["code"].(float64)) != codeAccessDenied {
		t.Errorf("code = %v, want %d", errObj["code"], codeAccessDenied)
	}
}

func TestCallUnknownGroupReturnsError(t *testing.T) {
	s := NewServer(t, Options{Password: "secret"})
	sid := authenticate(t, s)
	out := callRPC(t, s, sid, "nosuch", "nomethod", map[string]any{})
	if _, ok := out["error"]; !ok {
		t.Errorf("unknown group succeeded: %v", out)
	}
}

func TestFailNextIsOneShot(t *testing.T) {
	s := NewServer(t, Options{Password: "secret"})
	s.LoadFixture("lan", "get_config", json.RawMessage(`{"lan_ip":"192.168.8.1"}`))
	sid := authenticate(t, s)

	s.FailNext("lan", "get_config", codeNotFound, "injected")

	if out := callRPC(t, s, sid, "lan", "get_config", map[string]any{}); out["error"] == nil {
		t.Fatalf("injected failure did not fire: %v", out)
	}
	if out := callRPC(t, s, sid, "lan", "get_config", map[string]any{}); out["error"] != nil {
		t.Errorf("injected failure fired twice: %v", out)
	}
}

func TestReservationRoundTrip(t *testing.T) {
	s := NewServer(t, Options{Password: "secret"})
	s.SetReservations([]types.Reservation{
		{Name: "nas", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13", Enabled: true},
	})
	sid := authenticate(t, s)

	out := callRPC(t, s, sid, ReservationGroup, MethodGetConfig, map[string]any{})
	result, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %v", out)
	}
	list, ok := result["res"].([]any)
	if !ok || len(list) != 1 {
		t.Fatalf("res = %v, want one entry", result["res"])
	}

	// Writing through the API must be visible to Reservations(), so tests can
	// assert on device state rather than only on returned values.
	callRPC(t, s, sid, ReservationGroup, MethodSetConfig, map[string]any{
		"res": []any{
			map[string]any{"name": "nas", "mac": "aa:bb:cc:dd:ee:01", "ip": "192.168.8.13", "enabled": true},
			map[string]any{"name": "printer", "mac": "aa:bb:cc:dd:ee:02", "ip": "192.168.8.14", "enabled": true},
		},
	})

	got := s.Reservations()
	if len(got) != 2 {
		t.Fatalf("Reservations() returned %d entries, want 2", len(got))
	}
	if got[1].Name != "printer" {
		t.Errorf("second reservation name = %q, want printer", got[1].Name)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./src/mock/ -run 'TestCall|TestFailNext|TestReservation' -v`
Expected: FAIL, `undefined: LoadFixture`.

- [ ] **Step 3: Write the dispatch**

`src/mock/handlers.go`:

```go
package mock

import (
	"encoding/json"
	"net/http"

	"github.com/gherlein/gogl/src/types"
)

// Group and method names. These are placeholders until Phase 0 records the real
// ones; when it does, change these constants and the services' call sites, and
// nothing else.
const (
	ReservationGroup = "dhcp"
	NetworkGroup     = "lan"
	ClientGroup      = "clients"
	SystemGroup      = "system"

	MethodGetConfig = "get_config"
	MethodSetConfig = "set_config"
	MethodGetStatus = "get_status"
	MethodGetList   = "get_list"
)

// reservationsKey is the field in the reservation group's payload holding the
// list. Confirmed in Phase 0.
const reservationsKey = "res"

// LoadFixture registers the result payload returned for group.method.
func (s *Server) LoadFixture(group, method string, payload json.RawMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state[group+"."+method] = payload
}

// SetReservations seeds the mock's reservation table.
func (s *Server) SetReservations(res []types.Reservation) {
	payload, err := json.Marshal(map[string]any{reservationsKey: res})
	if err != nil {
		s.t.Fatalf("mock: marshal reservations: %v", err)
		return
	}
	s.LoadFixture(ReservationGroup, MethodGetConfig, payload)
}

// Reservations returns the mock's current reservation table so tests can assert
// on what was written, not only on what a call returned.
func (s *Server) Reservations() []types.Reservation {
	s.mu.Lock()
	payload, ok := s.state[ReservationGroup+"."+MethodGetConfig]
	s.mu.Unlock()
	if !ok {
		return nil
	}

	var wrapper struct {
		Res []types.Reservation `json:"res"`
	}
	if err := json.Unmarshal(payload, &wrapper); err != nil {
		s.t.Fatalf("mock: unmarshal reservations: %v", err)
		return nil
	}
	return wrapper.Res
}

// FailNext makes the next call to group.method return an RPC error instead of
// its fixture. One shot, so a test can prove that a per-entry failure is
// isolated rather than fatal.
func (s *Server) FailNext(group, method string, code int, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures[group+"."+method] = &RPCFault{Code: code, Message: message}
}

func (s *Server) handleCall(w http.ResponseWriter, req *request) {
	var params []json.RawMessage
	if err := json.Unmarshal(req.Params, &params); err != nil || len(params) < 3 {
		s.writeError(w, req.ID, codeBadRequest, "call params must be [sid, group, method, args]")
		return
	}

	var sid, group, method string
	if err := json.Unmarshal(params[0], &sid); err != nil {
		s.writeError(w, req.ID, codeBadRequest, "bad sid")
		return
	}
	if err := json.Unmarshal(params[1], &group); err != nil {
		s.writeError(w, req.ID, codeBadRequest, "bad group")
		return
	}
	if err := json.Unmarshal(params[2], &method); err != nil {
		s.writeError(w, req.ID, codeBadRequest, "bad method")
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.sessionValidLocked(sid) {
		s.writeErrorLocked(w, req.ID, codeAccessDenied, "Access denied")
		return
	}
	s.sidSeen = timeNow()

	key := group + "." + method
	if fault, ok := s.failures[key]; ok {
		delete(s.failures, key)
		s.writeErrorLocked(w, req.ID, fault.Code, fault.Message)
		return
	}

	// A set_config on the reservation group replaces the stored table, so that
	// writes are observable through Reservations().
	if group == ReservationGroup && method == MethodSetConfig {
		if len(params) < 4 {
			s.writeErrorLocked(w, req.ID, codeBadRequest, "set_config requires args")
			return
		}
		s.state[ReservationGroup+"."+MethodGetConfig] = params[3]
		s.writeResultLocked(w, req.ID, map[string]any{})
		return
	}

	payload, ok := s.state[key]
	if !ok {
		s.writeErrorLocked(w, req.ID, codeNotFound, "no fixture for "+key)
		return
	}
	s.writeResultLocked(w, req.ID, json.RawMessage(payload))
}
```

Add to `src/mock/server.go`:

```go
// timeNow exists so the mock's clock is a single seam, in case a future test
// needs to control it.
func timeNow() time.Time { return time.Now() }
```

- [ ] **Step 4: Remove the stub**

Delete the `handleCall` stub added in Task 9 Step 4 from `src/mock/server.go`. The real one
now lives in `handlers.go`.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./src/mock/ -v -race`
Expected: PASS, all sixteen tests.

- [ ] **Step 6: Commit**

```bash
git add src/mock/handlers.go src/mock/handlers_test.go src/mock/server.go
git commit -m "feat(mock): add call dispatch, fixtures, and fault injection"
```

---

## Phase 4: Authentication and Transport

### Task 11: Login sequence

**Files:**
- Create: `src/auth/login.go`
- Test: `src/auth/login_test.go`

**Interfaces:**
- Consumes: `auth.Cipher`, `auth.LoginHash` (Task 8), `mock.NewServer` (Task 9).
- Produces: `auth.Authenticator` with `NewAuthenticator(httpClient *http.Client, url, username, password string) *Authenticator` and `(*Authenticator).Login(ctx context.Context) (string, error)`. Task 13 calls `Login`.

The two-challenge sequence lives here and nowhere else.

- [ ] **Step 1: Write the failing test**

`src/auth/login_test.go`:

```go
package auth_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/gherlein/gogl/src/auth"
	"github.com/gherlein/gogl/src/mock"
)

func TestLoginReturnsSID(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	a := auth.NewAuthenticator(http.DefaultClient, s.URL(), "root", "secret")

	sid, err := a.Login(context.Background())
	if err != nil {
		t.Fatalf("Login error: %v", err)
	}
	if sid == "" {
		t.Error("Login returned an empty sid")
	}
}

func TestLoginRejectsWrongPassword(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	a := auth.NewAuthenticator(http.DefaultClient, s.URL(), "root", "wrong")

	if _, err := a.Login(context.Background()); err == nil {
		t.Error("Login with the wrong password succeeded")
	}
}

// The whole reason for two challenges: a nonce that dies in under a second must
// still yield a successful login, because the nonce is fetched after the slow
// crypt step rather than before it.
func TestLoginSucceedsWithShortNonceTTL(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret", NonceTTL: 300 * time.Millisecond})
	a := auth.NewAuthenticator(http.DefaultClient, s.URL(), "root", "secret")

	if _, err := a.Login(context.Background()); err != nil {
		t.Fatalf("Login error with a 300ms nonce TTL: %v", err)
	}
}

// The cipher is cached across logins, so a second login must not repeat the
// expensive crypt. Measured rather than asserted structurally: the second login
// should be markedly faster than the first.
func TestLoginCachesCipher(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	a := auth.NewAuthenticator(http.DefaultClient, s.URL(), "root", "secret")

	start := time.Now()
	if _, err := a.Login(context.Background()); err != nil {
		t.Fatalf("first Login: %v", err)
	}
	first := time.Since(start)

	start = time.Now()
	if _, err := a.Login(context.Background()); err != nil {
		t.Fatalf("second Login: %v", err)
	}
	second := time.Since(start)

	if second > first {
		t.Errorf("second login (%v) was not faster than the first (%v); cipher is not cached", second, first)
	}
}

func TestLoginAcceptsAlgAsString(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret", AlgAsString: true})
	a := auth.NewAuthenticator(http.DefaultClient, s.URL(), "root", "secret")

	if _, err := a.Login(context.Background()); err != nil {
		t.Fatalf("Login with alg as a string: %v", err)
	}
}

func TestLoginRejectsUnsupportedAlg(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret", Alg: 3})
	a := auth.NewAuthenticator(http.DefaultClient, s.URL(), "root", "secret")

	_, err := a.Login(context.Background())
	if !errors.Is(err, auth.ErrUnsupportedAlgorithm) {
		t.Errorf("Login error = %v, want ErrUnsupportedAlgorithm", err)
	}
}

func TestLoginHonoursContextCancellation(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	a := auth.NewAuthenticator(http.DefaultClient, s.URL(), "root", "secret")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := a.Login(ctx); err == nil {
		t.Error("Login with a cancelled context succeeded")
	}
}
```

Note this is an external test package (`auth_test`) to avoid an import cycle: `mock` imports
`auth`, so `auth`'s internal tests cannot import `mock`.

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./src/auth/ -v`
Expected: FAIL, `undefined: NewAuthenticator`.

- [ ] **Step 3: Write the implementation**

`src/auth/login.go`:

```go
package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
)

// Authenticator performs the challenge/response login. It caches the derived
// cipher, which depends only on password and salt, so that re-authentication
// does not repeat the deliberately slow crypt step.
type Authenticator struct {
	httpClient *http.Client
	url        string
	username   string
	password   string

	mu     sync.Mutex
	cipher string
	salt   string
}

func NewAuthenticator(httpClient *http.Client, url, username, password string) *Authenticator {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Authenticator{httpClient: httpClient, url: url, username: username, password: password}
}

type challengeResult struct {
	Alg   json.RawMessage `json:"alg"`
	Salt  string          `json:"salt"`
	Nonce string          `json:"nonce"`
}

// Login returns a fresh session id.
//
// It issues challenge twice on purpose. The first supplies the salt, from which
// the cipher is derived at a cost of 5000 hash rounds; by the time that
// completes, the first challenge's nonce has very likely expired, since the
// router keeps it alive for only about a second. The second challenge supplies a
// nonce that is fresh at the moment the cheap MD5 digest is computed over it.
//
// A single-challenge implementation races against its own crypt cost and fails
// intermittently, which is far worse than failing consistently.
func (a *Authenticator) Login(ctx context.Context) (string, error) {
	first, err := a.challenge(ctx)
	if err != nil {
		return "", err
	}

	alg, err := decodeAlg(first.Alg)
	if err != nil {
		return "", err
	}

	cipher, err := a.cipherFor(first.Salt, alg)
	if err != nil {
		return "", err
	}

	second, err := a.challenge(ctx)
	if err != nil {
		return "", err
	}
	// A salt change would invalidate the cached cipher. It should never happen,
	// but silently signing with a stale salt would look like a wrong password.
	if second.Salt != first.Salt {
		cipher, err = a.cipherFor(second.Salt, alg)
		if err != nil {
			return "", err
		}
	}

	var result struct {
		SID string `json:"sid"`
	}
	params := map[string]any{
		"username": a.username,
		"hash":     LoginHash(a.username, cipher, second.Nonce),
	}
	if err := a.post(ctx, "login", params, &result); err != nil {
		return "", fmt.Errorf("%w: %w", ErrUnauthorized, err)
	}
	if result.SID == "" {
		return "", fmt.Errorf("%w: router returned no session id", ErrUnauthorized)
	}
	return result.SID, nil
}

// cipherFor returns the cached cipher, deriving it if the salt has changed.
func (a *Authenticator) cipherFor(salt string, alg int) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.cipher != "" && a.salt == salt {
		return a.cipher, nil
	}
	cipher, err := Cipher(a.password, salt, alg)
	if err != nil {
		return "", err
	}
	a.cipher, a.salt = cipher, salt
	return cipher, nil
}

func (a *Authenticator) challenge(ctx context.Context) (challengeResult, error) {
	var result challengeResult
	err := a.post(ctx, "challenge", map[string]any{"username": a.username}, &result)
	return result, err
}

// decodeAlg tolerates alg arriving as a JSON number or a JSON string, both of
// which have been observed from firmware in the wild.
func decodeAlg(raw json.RawMessage) (int, error) {
	var asNumber int
	if err := json.Unmarshal(raw, &asNumber); err == nil {
		return asNumber, nil
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err != nil {
		return 0, fmt.Errorf("%w: alg is neither number nor string: %s", ErrUnsupportedAlgorithm, raw)
	}
	n, err := strconv.Atoi(asString)
	if err != nil {
		return 0, fmt.Errorf("%w: alg %q is not numeric", ErrUnsupportedAlgorithm, asString)
	}
	return n, nil
}

func (a *Authenticator) post(ctx context.Context, method string, params, out any) error {
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		return fmt.Errorf("gogl: marshal %s: %w", method, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("gogl: build %s request: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("gogl: %s: %w", method, err)
	}
	defer resp.Body.Close()

	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return fmt.Errorf("gogl: decode %s response: %w", method, err)
	}
	if envelope.Error != nil {
		return fmt.Errorf("gogl: %s failed: %s (code %d)", method, envelope.Error.Message, envelope.Error.Code)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(envelope.Result, out); err != nil {
		return fmt.Errorf("gogl: decode %s result: %w", method, err)
	}
	return nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./src/auth/ -v -race`
Expected: PASS, all seven login tests plus Task 8's crypt tests.

- [ ] **Step 5: Commit**

```bash
git add src/auth/login.go src/auth/login_test.go
git commit -m "feat(auth): add two-challenge login sequence with cipher caching"
```

---

### Task 12: JSON-RPC transport with session renewal

**Files:**
- Create: `src/transport/transport.go`
- Create: `src/transport/rpc.go`
- Test: `src/transport/rpc_test.go`

**Interfaces:**
- Consumes: `auth.NewAuthenticator`, `(*Authenticator).Login` (Task 11); `mock` (Tasks 9, 10).
- Produces: `transport.Transport` interface with `Call(ctx, group, method string, args, out any) error` and `Close() error`; `transport.New(cfg Config) *RPC`; `transport.Config{URL, Username, Password string; HTTPClient *http.Client; KeepaliveInterval time.Duration; MaxConcurrent int}`. Tasks 14 through 18 consume `Transport`.

This is where the sid lives, where the keepalive runs, and where the single transparent
retry happens. Nothing above this layer knows a session exists.

- [ ] **Step 1: Write the failing test**

`src/transport/rpc_test.go`:

```go
package transport_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gherlein/gogl/src/mock"
	"github.com/gherlein/gogl/src/transport"
)

func newTransport(t *testing.T, s *mock.Server, cfg transport.Config) transport.Transport {
	t.Helper()
	cfg.URL = s.URL()
	if cfg.Username == "" {
		cfg.Username = "root"
	}
	if cfg.Password == "" {
		cfg.Password = "secret"
	}
	tr := transport.New(cfg)
	t.Cleanup(func() {
		if err := tr.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return tr
}

func TestCallDecodesResult(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	s.LoadFixture("lan", "get_config", json.RawMessage(`{"lan_ip":"192.168.8.1","netmask":"255.255.255.0"}`))
	tr := newTransport(t, s, transport.Config{})

	var out struct {
		LANIP   string `json:"lan_ip"`
		Netmask string `json:"netmask"`
	}
	if err := tr.Call(context.Background(), "lan", "get_config", nil, &out); err != nil {
		t.Fatalf("Call error: %v", err)
	}
	if out.LANIP != "192.168.8.1" {
		t.Errorf("LANIP = %q, want 192.168.8.1", out.LANIP)
	}
}

// out may be nil when the caller does not care about the payload.
func TestCallWithNilOut(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	s.LoadFixture("lan", "get_config", json.RawMessage(`{}`))
	tr := newTransport(t, s, transport.Config{})

	if err := tr.Call(context.Background(), "lan", "get_config", nil, nil); err != nil {
		t.Errorf("Call with nil out: %v", err)
	}
}

func TestCallSurfacesRPCError(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	s.LoadFixture("lan", "get_config", json.RawMessage(`{}`))
	tr := newTransport(t, s, transport.Config{})

	s.FailNext("lan", "get_config", -32001, "injected")

	err := tr.Call(context.Background(), "lan", "get_config", nil, nil)
	if err == nil {
		t.Fatal("Call succeeded, want error")
	}
	var rpcErr *transport.Error
	if !errors.As(err, &rpcErr) {
		t.Fatalf("error is %T, want *transport.Error", err)
	}
	if rpcErr.Group != "lan" || rpcErr.Method != "get_config" {
		t.Errorf("error lost its call site: group=%q method=%q", rpcErr.Group, rpcErr.Method)
	}
}

// A session that idles out must be renewed transparently: the caller sees a
// successful call, not an error.
func TestCallRetriesOnExpiredSession(t *testing.T) {
	s := mock.NewServer(t, mock.Options{
		Password:   "secret",
		SessionTTL: 50 * time.Millisecond,
	})
	s.LoadFixture("lan", "get_config", json.RawMessage(`{"lan_ip":"192.168.8.1"}`))
	// Keepalive off, so the session genuinely expires.
	tr := newTransport(t, s, transport.Config{KeepaliveInterval: -1})

	if err := tr.Call(context.Background(), "lan", "get_config", nil, nil); err != nil {
		t.Fatalf("first Call: %v", err)
	}
	time.Sleep(120 * time.Millisecond)
	if err := tr.Call(context.Background(), "lan", "get_config", nil, nil); err != nil {
		t.Errorf("second Call after session expiry: %v", err)
	}
}

// Retry is bounded at one attempt: a wrong password must not become a login
// flood against a small SoC.
func TestCallDoesNotRetryForever(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	s.LoadFixture("lan", "get_config", json.RawMessage(`{}`))
	tr := newTransport(t, s, transport.Config{Password: "wrong", KeepaliveInterval: -1})

	done := make(chan error, 1)
	go func() {
		done <- tr.Call(context.Background(), "lan", "get_config", nil, nil)
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Error("Call with a wrong password succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Call did not return; retry is probably unbounded")
	}
}

// A burst of calls arriving with no session must produce exactly one login.
func TestConcurrentCallsLoginOnce(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	s.LoadFixture("lan", "get_config", json.RawMessage(`{}`))
	tr := newTransport(t, s, transport.Config{KeepaliveInterval: -1, MaxConcurrent: 8})

	const callers = 16
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- tr.Call(context.Background(), "lan", "get_config", nil, nil)
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Errorf("concurrent Call: %v", err)
		}
	}
	if got := s.LoginCount(); got != 1 {
		t.Errorf("LoginCount() = %d, want 1", got)
	}
}

func TestKeepaliveHoldsSessionOpen(t *testing.T) {
	s := mock.NewServer(t, mock.Options{
		Password:   "secret",
		SessionTTL: 100 * time.Millisecond,
	})
	s.LoadFixture("lan", "get_config", json.RawMessage(`{}`))
	tr := newTransport(t, s, transport.Config{KeepaliveInterval: 30 * time.Millisecond})

	if err := tr.Call(context.Background(), "lan", "get_config", nil, nil); err != nil {
		t.Fatalf("first Call: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	if got := s.LoginCount(); got != 1 {
		t.Errorf("LoginCount() = %d after keepalive should have held the session, want 1", got)
	}
}

func TestCloseStopsKeepalive(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	s.LoadFixture("lan", "get_config", json.RawMessage(`{}`))
	tr := transport.New(transport.Config{
		URL: s.URL(), Username: "root", Password: "secret",
		KeepaliveInterval: 10 * time.Millisecond,
	})
	if err := tr.Call(context.Background(), "lan", "get_config", nil, nil); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if err := tr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	before := s.AliveCount()
	time.Sleep(80 * time.Millisecond)
	if after := s.AliveCount(); after != before {
		t.Errorf("keepalive still running after Close: alive count went %d -> %d", before, after)
	}
}

// Close must be safe to call more than once, since defer plus explicit Close is
// a normal pattern.
func TestCloseIsIdempotent(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	tr := transport.New(transport.Config{URL: s.URL(), Username: "root", Password: "secret"})
	if err := tr.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := tr.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}
```

- [ ] **Step 2: Add the mock counters the tests need**

In `src/mock/server.go`, add to the `Server` struct:

```go
	loginCount int
	aliveCount int
```

Increment `s.loginCount++` immediately after `s.sid = ...` in `handleLogin`, and
`s.aliveCount++` at the top of the locked section in `handleAlive`. Then add accessors:

```go
// LoginCount reports how many successful logins the mock has served. Tests use
// it to prove that a burst of concurrent calls produced exactly one login.
func (s *Server) LoginCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loginCount
}

// AliveCount reports how many alive calls the mock has served, so a test can
// prove the keepalive stopped after Close.
func (s *Server) AliveCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.aliveCount
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./src/transport/ -v`
Expected: FAIL, `undefined: transport.New`.

- [ ] **Step 4: Write the interface**

`src/transport/transport.go`:

```go
// Package transport carries JSON-RPC calls to a GL.iNet router, owning session
// acquisition, renewal, and retry. Layers above it do not know a session exists.
package transport

import "context"

// Transport performs authenticated JSON-RPC calls against a GL.iNet router.
type Transport interface {
	// Call invokes method on group with args, decoding the result into out.
	// out may be nil to discard the result; args may be nil for no arguments.
	Call(ctx context.Context, group, method string, args, out any) error

	// Close stops the keepalive goroutine and releases resources. Safe to call
	// more than once.
	Close() error
}

// Error is a JSON-RPC error returned by the router, carrying the group and
// method that produced it so a failure is traceable to its call site.
type Error struct {
	Code    int
	Message string
	Group   string
	Method  string
}

func (e *Error) Error() string {
	return fmt.Sprintf("gogl: rpc error %d on %s.%s: %s", e.Code, e.Group, e.Method, e.Message)
}
```

Add `"fmt"` to the imports.

- [ ] **Step 5: Write the implementation**

`src/transport/rpc.go`:

```go
package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gherlein/gogl/src/auth"
)

// Defaults. KeepaliveInterval stays well under the device's ~35s session idle
// timeout; a negative value disables the keepalive entirely, which tests use to
// force expiry.
const (
	DefaultKeepaliveInterval = 20 * time.Second
	DefaultMaxConcurrent     = 4
	DefaultTimeout           = 10 * time.Second

	// codeAccessDenied is what the router returns for a stale or absent sid.
	codeAccessDenied = -32000
)

type Config struct {
	URL      string
	Username string
	Password string

	HTTPClient        *http.Client
	KeepaliveInterval time.Duration
	MaxConcurrent     int
}

// RPC is the JSON-RPC implementation of Transport.
type RPC struct {
	cfg  Config
	auth *authenticatorish

	// sid is read on every call's hot path, so it is atomic rather than mutex
	// guarded. loginMu serializes the write side.
	sid     atomic.Value
	loginMu sync.Mutex

	// sem bounds in-flight requests. The SFT1200 is a small SoC and drops
	// requests under load.
	sem chan struct{}

	nextID atomic.Int64

	closeOnce sync.Once
	done      chan struct{}
	wg        sync.WaitGroup
}

// authenticatorish is the subset of auth.Authenticator that RPC needs, named so
// that a test could substitute a fake without a new interface in auth.
type authenticatorish = auth.Authenticator

func New(cfg Config) *RPC {
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: DefaultTimeout}
	}
	if cfg.KeepaliveInterval == 0 {
		cfg.KeepaliveInterval = DefaultKeepaliveInterval
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = DefaultMaxConcurrent
	}

	r := &RPC{
		cfg:  cfg,
		auth: auth.NewAuthenticator(cfg.HTTPClient, cfg.URL, cfg.Username, cfg.Password),
		sem:  make(chan struct{}, cfg.MaxConcurrent),
		done: make(chan struct{}),
	}
	r.sid.Store("")

	if cfg.KeepaliveInterval > 0 {
		r.wg.Add(1)
		go r.keepalive()
	}
	return r
}

func (r *RPC) Close() error {
	r.closeOnce.Do(func() {
		close(r.done)
		r.wg.Wait()
	})
	return nil
}

// Call sends one request, renewing the session and retrying exactly once if the
// router reports the sid is stale.
func (r *RPC) Call(ctx context.Context, group, method string, args, out any) error {
	select {
	case r.sem <- struct{}{}:
		defer func() { <-r.sem }()
	case <-ctx.Done():
		return ctx.Err()
	}

	sid, err := r.session(ctx, false)
	if err != nil {
		return err
	}

	err = r.do(ctx, sid, group, method, args, out)

	var rpcErr *Error
	if errors.As(err, &rpcErr) && rpcErr.Code == codeAccessDenied {
		// The session died between the last keepalive and now. Force one
		// re-login and retry once. Bounded deliberately: an unbounded loop
		// against a wrong password is a login flood.
		sid, loginErr := r.session(ctx, true)
		if loginErr != nil {
			return loginErr
		}
		return r.do(ctx, sid, group, method, args, out)
	}
	return err
}

// session returns a valid sid, logging in if there is none or if force is set.
// Double-checked locking: a burst of callers that all see an empty sid must
// produce one login, not one each.
func (r *RPC) session(ctx context.Context, force bool) (string, error) {
	if !force {
		if sid, _ := r.sid.Load().(string); sid != "" {
			return sid, nil
		}
	}

	r.loginMu.Lock()
	defer r.loginMu.Unlock()

	current, _ := r.sid.Load().(string)
	if !force && current != "" {
		return current, nil
	}
	if force && current != "" && current != r.staleSID {
		// Another goroutine already replaced the sid we found stale.
		return current, nil
	}

	sid, err := r.auth.Login(ctx)
	if err != nil {
		return "", err
	}
	r.sid.Store(sid)
	r.staleSID = ""
	return sid, nil
}

func (r *RPC) do(ctx context.Context, sid, group, method string, args, out any) error {
	if args == nil {
		args = map[string]any{}
	}
	id := r.nextID.Add(1)

	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  "call",
		"params":  []any{sid, group, method, args},
	})
	if err != nil {
		return fmt.Errorf("gogl: marshal %s.%s: %w", group, method, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.cfg.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("gogl: build %s.%s request: %w", group, method, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.cfg.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("gogl: %s.%s: %w", group, method, err)
	}
	defer resp.Body.Close()

	var envelope struct {
		ID     int64           `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return fmt.Errorf("gogl: decode %s.%s response: %w", group, method, err)
	}

	// One request per round trip, so a mismatched id means the response does not
	// belong to this call. There is no multiplexing to reconcile it against.
	if envelope.ID != id {
		return fmt.Errorf("gogl: %s.%s: response id %d does not match request id %d", group, method, envelope.ID, id)
	}

	if envelope.Error != nil {
		if envelope.Error.Code == codeAccessDenied {
			r.markStale(sid)
		}
		return &Error{
			Code:    envelope.Error.Code,
			Message: envelope.Error.Message,
			Group:   group,
			Method:  method,
		}
	}

	if out == nil {
		return nil
	}
	if err := json.Unmarshal(envelope.Result, out); err != nil {
		return fmt.Errorf("gogl: decode %s.%s result: %w", group, method, err)
	}
	return nil
}

// markStale records which sid was rejected so that session(force) can tell a
// genuine renewal from a redundant one.
func (r *RPC) markStale(sid string) {
	r.loginMu.Lock()
	defer r.loginMu.Unlock()
	if current, _ := r.sid.Load().(string); current == sid {
		r.staleSID = sid
		r.sid.Store("")
	}
}

func (r *RPC) keepalive() {
	defer r.wg.Done()
	ticker := time.NewTicker(r.cfg.KeepaliveInterval)
	defer ticker.Stop()

	for {
		select {
		case <-r.done:
			return
		case <-ticker.C:
			sid, _ := r.sid.Load().(string)
			if sid == "" {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), r.cfg.KeepaliveInterval)
			if err := r.alive(ctx, sid); err != nil {
				// A failed keepalive is not fatal: the next Call will re-login
				// through the retry path.
				r.markStale(sid)
			}
			cancel()
		}
	}
}

func (r *RPC) alive(ctx context.Context, sid string) error {
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": r.nextID.Add(1), "method": "alive",
		"params": map[string]any{"sid": sid},
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.cfg.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.cfg.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var envelope struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return err
	}
	if envelope.Error != nil {
		return fmt.Errorf("gogl: keepalive rejected with code %d", envelope.Error.Code)
	}
	return nil
}
```

Add `staleSID string` to the `RPC` struct (guarded by `loginMu`), and add `"errors"` to the
imports.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./src/transport/ -v -race`
Expected: PASS, all nine tests. `TestConcurrentCallsLoginOnce` is the one that matters most;
if it reports more than one login, the double-checked locking in `session` is wrong.

- [ ] **Step 7: Commit**

```bash
git add src/transport/ src/mock/server.go
git commit -m "feat(transport): add JSON-RPC transport with keepalive and single retry"
```

### Task 13: Client and Config

**Files:**
- Create: `src/client.go`
- Test: `src/client_test.go`

**Interfaces:**
- Consumes: `transport.New`, `transport.Config` (Task 12).
- Produces: `gogl.Config`, `gogl.New(cfg Config) (*Client, error)`, `(*Client).Call(...)`, `(*Client).Close()`, and the four service accessors (stubbed here, filled in Tasks 15 through 18). Every utility and example constructs a `Client`.

- [ ] **Step 1: Write the failing test**

`src/client_test.go`:

```go
package gogl_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"

	gogl "github.com/gherlein/gogl/src"
	"github.com/gherlein/gogl/src/mock"
)

// clientFor points a Client at a mock server by parsing the mock's URL back
// into host and port.
func clientFor(t *testing.T, s *mock.Server) *gogl.Client {
	t.Helper()
	u, err := url.Parse(s.URL())
	if err != nil {
		t.Fatalf("parse mock URL: %v", err)
	}
	port := 80
	if p := u.Port(); p != "" {
		if _, err := fmt.Sscanf(p, "%d", &port); err != nil {
			t.Fatalf("parse port %q: %v", p, err)
		}
	}

	c, err := gogl.New(gogl.Config{
		Host:     u.Hostname(),
		Port:     port,
		Password: "secret",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return c
}

func TestNewRequiresHost(t *testing.T) {
	_, err := gogl.New(gogl.Config{Password: "secret"})
	if err == nil {
		t.Fatal("New without Host succeeded")
	}
	if !strings.Contains(err.Error(), "host") {
		t.Errorf("error %q does not mention the missing host", err)
	}
}

func TestNewRequiresPassword(t *testing.T) {
	_, err := gogl.New(gogl.Config{Host: "192.168.8.1"})
	if err == nil {
		t.Fatal("New without Password succeeded")
	}
	if !strings.Contains(err.Error(), "password") {
		t.Errorf("error %q does not mention the missing password", err)
	}
}

// New must not contact the router, so constructing a client is cheap and cannot
// fail on a network error.
func TestNewDoesNotContactRouter(t *testing.T) {
	c, err := gogl.New(gogl.Config{Host: "192.0.2.1", Password: "secret"})
	if err != nil {
		t.Fatalf("New against an unreachable host failed: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestConfigDefaults(t *testing.T) {
	cfg := gogl.Config{Host: "192.168.8.1", Password: "secret"}
	c, err := gogl.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()

	// Port 80 and HTTP, because that is what GL.iNet firmware serves.
	if got := c.Endpoint(); got != "http://192.168.8.1:80/rpc" {
		t.Errorf("Endpoint() = %q, want http://192.168.8.1:80/rpc", got)
	}
}

func TestConfigHTTPS(t *testing.T) {
	c, err := gogl.New(gogl.Config{Host: "192.168.8.1", Port: 443, HTTPS: true, Password: "secret"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()

	if got := c.Endpoint(); got != "https://192.168.8.1:443/rpc" {
		t.Errorf("Endpoint() = %q, want https://192.168.8.1:443/rpc", got)
	}
}

// Username defaults to root, which is the only account standard firmware has.
func TestConfigDefaultUsername(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	s.LoadFixture("lan", "get_config", json.RawMessage(`{}`))
	c := clientFor(t, s)

	if err := c.Call(context.Background(), "lan", "get_config", nil, nil); err != nil {
		t.Errorf("Call with the default username: %v", err)
	}
}

func TestCallEscapeHatch(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	s.LoadFixture("anygroup", "anymethod", json.RawMessage(`{"value":42}`))
	c := clientFor(t, s)

	var out struct {
		Value int `json:"value"`
	}
	if err := c.Call(context.Background(), "anygroup", "anymethod", nil, &out); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if out.Value != 42 {
		t.Errorf("Value = %d, want 42", out.Value)
	}
}

func TestCallOnUnknownGroupErrors(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	c := clientFor(t, s)

	err := c.Call(context.Background(), "nope", "nope", nil, nil)
	if err == nil {
		t.Fatal("Call on an unknown group succeeded")
	}
	if !errors.Is(err, gogl.ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound via RPCError.Unwrap", err)
	}
}
```

Add `"fmt"` to the test imports.

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./src/ -run 'TestNew|TestConfig|TestCall' -v`
Expected: FAIL, `undefined: gogl.New`.

- [ ] **Step 3: Write the implementation**

`src/client.go`:

```go
package gogl

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gherlein/gogl/src/transport"
)

// Defaults reflect what GL.iNet firmware actually serves: HTTP on port 80, and
// the root account, which is the only one standard firmware has.
const (
	DefaultPort     = 80
	DefaultUsername = "root"
	DefaultTimeout  = 10 * time.Second
)

// Config configures a Client. Zero values are safe: the resulting client
// verifies TLS and uses conservative timeouts.
type Config struct {
	Host string // required
	Port int    // default 80
	HTTPS bool  // default false; GL.iNet serves HTTP on 80

	Username string // default "root"
	Password string // required

	// InsecureSkipVerify disables TLS certificate verification. Named for what
	// it does, and false by default, so the library is secure at its zero value
	// even though the CLIs default the other way to reach self-signed devices.
	// A library must not be insecure by default.
	InsecureSkipVerify bool

	Timeout           time.Duration
	KeepaliveInterval time.Duration
	MaxConcurrent     int
}

// Client is a connection to a GL.iNet router. It is safe for concurrent use.
type Client struct {
	cfg       Config
	endpoint  string
	transport transport.Transport
}

// New builds a Client. It does not contact the router: the first call
// authenticates lazily, so construction is cheap and cannot fail on a network
// error.
func New(cfg Config) (*Client, error) {
	if cfg.Host == "" {
		return nil, errors.New("gogl: config requires a host")
	}
	if cfg.Password == "" {
		return nil, errors.New("gogl: config requires a password")
	}

	if cfg.Port == 0 {
		cfg.Port = DefaultPort
	}
	if cfg.Username == "" {
		cfg.Username = DefaultUsername
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = DefaultTimeout
	}

	scheme := "http"
	httpClient := &http.Client{Timeout: cfg.Timeout}
	if cfg.HTTPS {
		scheme = "https"
		httpClient.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: cfg.InsecureSkipVerify},
		}
	}
	endpoint := fmt.Sprintf("%s://%s:%d/rpc", scheme, cfg.Host, cfg.Port)

	return &Client{
		cfg:      cfg,
		endpoint: endpoint,
		transport: transport.New(transport.Config{
			URL:               endpoint,
			Username:          cfg.Username,
			Password:          cfg.Password,
			HTTPClient:        httpClient,
			KeepaliveInterval: cfg.KeepaliveInterval,
			MaxConcurrent:     cfg.MaxConcurrent,
		}),
	}, nil
}

// Endpoint returns the JSON-RPC URL this client talks to.
func (c *Client) Endpoint() string { return c.endpoint }

// Call invokes an arbitrary group and method, bypassing the typed services. It
// exists so that endpoints not yet modelled remain reachable, and because API
// discovery is done with it. Prefer a typed service where one exists.
func (c *Client) Call(ctx context.Context, group, method string, args, out any) error {
	err := c.transport.Call(ctx, group, method, args, out)

	// Translate the transport's error into the package's own type so callers
	// need not import transport to inspect a failure.
	var tErr *transport.Error
	if errors.As(err, &tErr) {
		return &RPCError{Code: tErr.Code, Message: tErr.Message, Group: tErr.Group, Method: tErr.Method}
	}
	return err
}

func (c *Client) Close() error { return c.transport.Close() }
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./src/ -v -race`
Expected: PASS, all client tests plus Task 4's error tests.

- [ ] **Step 5: Commit**

```bash
git add src/client.go src/client_test.go
git commit -m "feat(client): add Client and Config with lazy authentication"
```

### Task 14: Phase 4 integration check

**Files:**
- Test: `src/integration_test.go`

**Interfaces:**
- Consumes: everything from Tasks 4 through 13.
- Produces: no new API. A gate proving the stack works end to end before services are layered on.

- [ ] **Step 1: Write the test**

`src/integration_test.go`:

```go
package gogl_test

import (
	"context"
	"encoding/json"
	"net/url"
	"sync"
	"testing"
	"time"

	gogl "github.com/gherlein/gogl/src"
	"github.com/gherlein/gogl/src/mock"
)

// The full stack: crypt, two-challenge login, session, keepalive, retry, and
// call dispatch, under concurrency and across a session expiry.
func TestStackSurvivesSessionExpiryUnderLoad(t *testing.T) {
	s := mock.NewServer(t, mock.Options{
		Password:   "secret",
		SessionTTL: 60 * time.Millisecond,
	})
	s.LoadFixture("lan", "get_config", json.RawMessage(`{"lan_ip":"192.168.8.1"}`))

	u, err := url.Parse(s.URL())
	if err != nil {
		t.Fatalf("parse URL: %v", err)
	}
	var port int
	if _, err := fmt.Sscanf(u.Port(), "%d", &port); err != nil {
		t.Fatalf("parse port: %v", err)
	}

	c, err := gogl.New(gogl.Config{
		Host: u.Hostname(), Port: port, Password: "secret",
		KeepaliveInterval: 20 * time.Millisecond,
		MaxConcurrent:     4,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()

	// Hammer the client across several session lifetimes.
	deadline := time.Now().Add(400 * time.Millisecond)
	var wg sync.WaitGroup
	errs := make(chan error, 256)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(deadline) {
				var out struct {
					LANIP string `json:"lan_ip"`
				}
				if err := c.Call(context.Background(), "lan", "get_config", nil, &out); err != nil {
					errs <- err
					return
				}
				if out.LANIP != "192.168.8.1" {
					errs <- fmt.Errorf("lan_ip = %q", out.LANIP)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("call failed during sustained load: %v", err)
	}
}
```

Add `"fmt"` to the imports.

- [ ] **Step 2: Run it**

Run: `go test ./src/ -run TestStackSurvives -v -race -count=3`
Expected: PASS all three runs. Running it three times catches flakiness that a single pass
would hide, and this test exists precisely to catch timing races.

- [ ] **Step 3: Run the whole suite with coverage**

Run: `make test && make coverage`
Expected: PASS. Coverage of `src/auth`, `src/transport`, `src/types`, `src/internal/ipmath`
at 100% of the code written so far. Anything uncovered is either dead code to delete or a
missing test to write.

- [ ] **Step 4: Commit**

```bash
git add src/integration_test.go
git commit -m "test: add Phase 4 integration check across session expiry"
```

**Gate:** Phase 5 requires Phase 0 output. Confirm every row in `docs/DESIGN.md` § Phase 0
is answered and its fixture committed before continuing.

---

## Remaining Tasks

Tasks 15 through 30 continue in [`plan-part3.md`](plan-part3.md) and
[`plan-part4.md`](plan-part4.md).
