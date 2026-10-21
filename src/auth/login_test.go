// External test package: mock imports auth, so auth's internal tests cannot
// import mock without a cycle.
package auth_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
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

	_, err := a.Login(context.Background())
	if err == nil {
		t.Fatal("Login with the wrong password succeeded")
	}
	if !errors.Is(err, auth.ErrUnauthorized) {
		t.Errorf("error = %v, want ErrUnauthorized", err)
	}
}

// The whole reason for two challenges: a nonce that dies in well under a second
// must still yield a successful login, because the nonce is fetched after the
// slow crypt step rather than before it.
func TestLoginSucceedsWithShortNonceTTL(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret", NonceTTL: 200 * time.Millisecond})
	a := auth.NewAuthenticator(http.DefaultClient, s.URL(), "root", "secret")

	if _, err := a.Login(context.Background()); err != nil {
		t.Fatalf("Login error with a 200ms nonce TTL: %v", err)
	}
}

// The cipher is cached across logins, so a second login must not repeat the
// expensive crypt.
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

func TestLoginAllAlgorithms(t *testing.T) {
	for _, alg := range []int{auth.AlgMD5, auth.AlgSHA256, auth.AlgSHA512} {
		s := mock.NewServer(t, mock.Options{Password: "secret", Alg: alg})
		a := auth.NewAuthenticator(http.DefaultClient, s.URL(), "root", "secret")
		if _, err := a.Login(context.Background()); err != nil {
			t.Errorf("Login failed for alg %d: %v", alg, err)
		}
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

func TestLoginAgainstUnreachableHost(t *testing.T) {
	// 192.0.2.0/24 is reserved for documentation and never routable.
	a := auth.NewAuthenticator(
		&http.Client{Timeout: 100 * time.Millisecond},
		"http://192.0.2.1/rpc", "root", "secret",
	)
	if _, err := a.Login(context.Background()); err == nil {
		t.Error("Login against an unreachable host succeeded")
	}
}

func TestNewAuthenticatorDefaultsHTTPClient(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	a := auth.NewAuthenticator(nil, s.URL(), "root", "secret")

	if _, err := a.Login(context.Background()); err != nil {
		t.Errorf("Login with a nil http.Client: %v", err)
	}
}

// Firmware 4.3.28 on the GL-SFT1200 announces hash-method "sha256" and expects the
// login digest in SHA-256. Firmware predating the field omits it and expects MD5.
// Both must work: honoring the field is what makes gogl usable on either.
func TestLoginHonoursHashMethod(t *testing.T) {
	tests := []struct {
		name       string
		hashMethod string
		alg        int
	}{
		{"field omitted, legacy MD5", "", auth.AlgSHA512},
		{"explicit md5", auth.HashMethodMD5, auth.AlgSHA512},
		{"sha256 as on the SFT1200", auth.HashMethodSHA256, auth.AlgSHA256},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := mock.NewServer(t, mock.Options{
				Password:   "secret",
				Alg:        tt.alg,
				HashMethod: tt.hashMethod,
			})
			a := auth.NewAuthenticator(http.DefaultClient, s.URL(), "root", "secret")

			sid, err := a.Login(context.Background())
			if err != nil {
				t.Fatalf("Login: %v", err)
			}
			if sid == "" {
				t.Error("empty sid")
			}
		})
	}
}

// A router advertising a digest gogl does not implement must fail loudly rather
// than sending an MD5 that the router will reject as a bad password.
func TestLoginRejectsUnknownHashMethod(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret", HashMethod: "sha3-512"})
	a := auth.NewAuthenticator(http.DefaultClient, s.URL(), "root", "secret")

	_, err := a.Login(context.Background())
	if !errors.Is(err, auth.ErrUnsupportedHashMethod) {
		t.Errorf("error = %v, want ErrUnsupportedHashMethod", err)
	}
}

// The exact pairing observed on the SFT1200: alg 5 crypt with a sha256 digest.
func TestLoginAgainstSFT1200Profile(t *testing.T) {
	s := mock.NewServer(t, mock.Options{
		Password:   "secret",
		Alg:        auth.AlgSHA256,
		HashMethod: auth.HashMethodSHA256,
	})
	a := auth.NewAuthenticator(http.DefaultClient, s.URL(), "root", "secret")

	if _, err := a.Login(context.Background()); err != nil {
		t.Fatalf("Login against the SFT1200 profile: %v", err)
	}
}

// The firmware locks the account after a handful of failed logins and reports the
// remaining wait. That must surface as its own error: retrying makes it worse, and
// the generic "Access denied" sends you hunting for a password problem instead.
//
// Learned the hard way against real hardware, by burning a dozen logins sweeping
// digest variants.
func TestLoginReportsRateLimit(t *testing.T) {
	s := mock.NewServer(t, mock.Options{
		Password:         "secret",
		MaxLoginFailures: 2,
		LockoutWait:      593,
	})
	a := auth.NewAuthenticator(http.DefaultClient, s.URL(), "root", "wrong")

	// Burn past the limit.
	var err error
	for i := 0; i < 4; i++ {
		_, err = a.Login(context.Background())
	}

	if !errors.Is(err, auth.ErrLoginRateLimited) {
		t.Fatalf("error = %v, want ErrLoginRateLimited", err)
	}
	if !strings.Contains(err.Error(), "593") {
		t.Errorf("error does not report the wait in seconds: %v", err)
	}
	// A duration is friendlier than a raw second count when the wait is minutes.
	if !strings.Contains(err.Error(), "9m53s") {
		t.Errorf("error does not report the wait as a duration: %v", err)
	}
}

// Once locked out, even a correct password is refused, and the error still says why.
func TestLoginRateLimitBlocksCorrectPassword(t *testing.T) {
	s := mock.NewServer(t, mock.Options{
		Password:         "secret",
		MaxLoginFailures: 1,
		LockoutWait:      600,
	})

	wrong := auth.NewAuthenticator(http.DefaultClient, s.URL(), "root", "wrong")
	for i := 0; i < 3; i++ {
		_, _ = wrong.Login(context.Background())
	}

	right := auth.NewAuthenticator(http.DefaultClient, s.URL(), "root", "secret")
	_, err := right.Login(context.Background())
	if !errors.Is(err, auth.ErrLoginRateLimited) {
		t.Errorf("error = %v, want ErrLoginRateLimited even with the right password", err)
	}
}
