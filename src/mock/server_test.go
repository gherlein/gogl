package mock

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/gherlein/gogl/src/auth"
)

// post is a raw JSON-RPC helper. The mock's own tests deliberately avoid the
// transport, which is built on top of the mock and cannot be a dependency of it.
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

func loginWith(t *testing.T, s *Server, password string) map[string]any {
	t.Helper()
	alg, salt, nonce := challenge(t, s.URL(), s.Username())
	cipher, err := auth.Cipher(password, salt, alg)
	if err != nil {
		t.Fatalf("Cipher: %v", err)
	}
	hash, err := auth.LoginHash(s.Username(), cipher, nonce, s.opts.HashMethod)
	if err != nil {
		t.Fatalf("LoginHash: %v", err)
	}
	return post(t, s.URL(), map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "login",
		"params": map[string]any{"username": s.Username(), "hash": hash},
	})
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

func TestChallengeRejectsUnknownUser(t *testing.T) {
	s := NewServer(t, Options{Password: "secret"})
	out := post(t, s.URL(), map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "challenge",
		"params": map[string]any{"username": "nobody"},
	})
	if _, ok := out["error"]; !ok {
		t.Errorf("challenge for an unknown user succeeded: %v", out)
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
	out := loginWith(t, s, "secret")

	result, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("login returned no result: %v", out)
	}
	if sid, _ := result["sid"].(string); sid == "" {
		t.Error("login returned an empty sid")
	}
	if got := s.LoginCount(); got != 1 {
		t.Errorf("LoginCount() = %d, want 1", got)
	}
}

func TestLoginRejectsWrongPassword(t *testing.T) {
	s := NewServer(t, Options{Password: "secret"})
	if out := loginWith(t, s, "wrong"); out["error"] == nil {
		t.Errorf("login with wrong password succeeded: %v", out)
	}
	if got := s.LoginCount(); got != 0 {
		t.Errorf("LoginCount() = %d after a failed login, want 0", got)
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
	hash, err := auth.LoginHash("root", cipher, nonce, "")
	if err != nil {
		t.Fatalf("LoginHash: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	out := post(t, s.URL(), map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "login",
		"params": map[string]any{"username": "root", "hash": hash},
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
	hash, err := auth.LoginHash("root", cipher, nonce, "")
	if err != nil {
		t.Fatalf("LoginHash: %v", err)
	}
	body := map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "login",
		"params": map[string]any{"username": "root", "hash": hash},
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
	result, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %v", out)
	}
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
	if got := s.AliveCount(); got != 1 {
		t.Errorf("AliveCount() = %d, want 1", got)
	}
}

func TestAliveAcceptsValidSession(t *testing.T) {
	s := NewServer(t, Options{Password: "secret"})
	result := loginWith(t, s, "secret")["result"].(map[string]any)
	sid := result["sid"].(string)

	out := post(t, s.URL(), map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "alive",
		"params": map[string]any{"sid": sid},
	})
	if out["error"] != nil {
		t.Errorf("alive with a valid sid failed: %v", out)
	}
}

// alive is observed with the sid both as an object field and as the first
// element of an array; both must work.
func TestAliveAcceptsArrayParams(t *testing.T) {
	s := NewServer(t, Options{Password: "secret"})
	result := loginWith(t, s, "secret")["result"].(map[string]any)
	sid := result["sid"].(string)

	out := post(t, s.URL(), map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "alive",
		"params": []any{sid},
	})
	if out["error"] != nil {
		t.Errorf("alive with array params failed: %v", out)
	}
}

func TestAllAlgorithms(t *testing.T) {
	for _, alg := range []int{auth.AlgMD5, auth.AlgSHA256, auth.AlgSHA512} {
		t.Run(fmt.Sprintf("alg%d", alg), func(t *testing.T) {
			s := NewServer(t, Options{Password: "secret", Alg: alg})
			gotAlg, _, _ := challenge(t, s.URL(), "root")
			if gotAlg != alg {
				t.Errorf("alg = %d, want %d", gotAlg, alg)
			}
			if out := loginWith(t, s, "secret"); out["result"] == nil {
				t.Errorf("login failed for alg %d: %v", alg, out)
			}
		})
	}
}

func TestUnknownMethod(t *testing.T) {
	s := NewServer(t, Options{Password: "secret"})
	out := post(t, s.URL(), map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "nonsense", "params": map[string]any{},
	})
	if _, ok := out["error"]; !ok {
		t.Errorf("unknown method succeeded: %v", out)
	}
}

func TestMalformedRequest(t *testing.T) {
	s := NewServer(t, Options{Password: "secret"})
	resp, err := http.Post(s.URL(), "application/json", bytes.NewReader([]byte("{not json")))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := out["error"]; !ok {
		t.Errorf("malformed request succeeded: %v", out)
	}
}
