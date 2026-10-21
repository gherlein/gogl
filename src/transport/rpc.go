package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gherlein/gogl/src/auth"
)

const (
	// DefaultKeepaliveInterval stays well under the device's ~35s session idle
	// timeout. A negative KeepaliveInterval disables the keepalive entirely,
	// which tests use to force expiry.
	DefaultKeepaliveInterval = 20 * time.Second

	// DefaultMaxConcurrent bounds in-flight requests. The SFT1200 is a small SoC
	// and drops requests under load.
	DefaultMaxConcurrent = 4

	DefaultTimeout = 10 * time.Second

	// CodeAccessDenied is what the router returns for a stale or absent sid.
	CodeAccessDenied = -32000
)

// Config configures an RPC transport.
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
	auth *auth.Authenticator

	// sessionMu guards sid and generation. Login is performed while holding it,
	// which is deliberate: it blocks concurrent callers for the duration of one
	// crypt, and is what guarantees a burst produces one login rather than N.
	sessionMu  sync.Mutex
	sid        string
	generation uint64

	sem    chan struct{}
	nextID atomic.Int64

	closeOnce sync.Once
	done      chan struct{}
	wg        sync.WaitGroup
}

// New builds a transport. It does not contact the router.
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

	sid, generation, err := r.session(ctx)
	if err != nil {
		return err
	}

	err = r.do(ctx, sid, group, method, args, out)

	var rpcErr *Error
	if errors.As(err, &rpcErr) && rpcErr.Code == CodeAccessDenied {
		// The session died between the last keepalive and now. Force one
		// re-login and retry once.
		//
		// Bounded at one attempt deliberately: an unbounded loop against a wrong
		// password becomes a login flood aimed at a very small SoC.
		sid, _, loginErr := r.renew(ctx, generation)
		if loginErr != nil {
			return loginErr
		}
		return r.do(ctx, sid, group, method, args, out)
	}
	return err
}

// session returns a usable sid and the generation it belongs to, logging in if
// there is none.
func (r *RPC) session(ctx context.Context) (string, uint64, error) {
	r.sessionMu.Lock()
	if r.sid != "" {
		sid, generation := r.sid, r.generation
		r.sessionMu.Unlock()
		return sid, generation, nil
	}
	r.sessionMu.Unlock()

	return r.renew(ctx, 0)
}

// renew acquires a new session unless another goroutine has already replaced the
// one the caller found stale.
//
// staleGeneration is the generation the caller was using. If the current
// generation has already moved past it, someone else did the work and the caller
// simply takes the result. This is what keeps a burst of expired calls to one
// login.
func (r *RPC) renew(ctx context.Context, staleGeneration uint64) (string, uint64, error) {
	r.sessionMu.Lock()
	defer r.sessionMu.Unlock()

	if r.sid != "" && r.generation > staleGeneration {
		return r.sid, r.generation, nil
	}

	sid, err := r.auth.Login(ctx)
	if err != nil {
		return "", 0, err
	}
	r.sid = sid
	r.generation++
	return r.sid, r.generation, nil
}

// invalidate clears sid if it is still the one that failed, so the next Call
// logs in rather than reusing a session the router has forgotten.
func (r *RPC) invalidate(sid string) {
	r.sessionMu.Lock()
	defer r.sessionMu.Unlock()
	if r.sid == sid {
		r.sid = ""
	}
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

	envelope, err := r.roundTrip(ctx, body)
	if err != nil {
		return fmt.Errorf("gogl: %s.%s: %w", group, method, err)
	}

	// One request per round trip, so a mismatched id means the response does not
	// belong to this call. There is no multiplexing to reconcile it against.
	if envelope.ID != id {
		return fmt.Errorf("gogl: %s.%s: response id %d does not match request id %d",
			group, method, envelope.ID, id)
	}

	if envelope.Error != nil {
		if envelope.Error.Code == CodeAccessDenied {
			r.invalidate(sid)
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

type responseEnvelope struct {
	ID     int64           `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (r *RPC) roundTrip(ctx context.Context, body []byte) (*responseEnvelope, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.cfg.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var envelope responseEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &envelope, nil
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
			r.sessionMu.Lock()
			sid := r.sid
			r.sessionMu.Unlock()
			if sid == "" {
				continue
			}

			ctx, cancel := context.WithTimeout(context.Background(), r.cfg.KeepaliveInterval)
			if err := r.alive(ctx, sid); err != nil {
				// A failed keepalive is not fatal. Clearing the sid means the
				// next Call logs in cleanly instead of spending a round trip
				// discovering the session is gone.
				r.invalidate(sid)
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

	envelope, err := r.roundTrip(ctx, body)
	if err != nil {
		return err
	}
	if envelope.Error != nil {
		return fmt.Errorf("gogl: keepalive rejected with code %d", envelope.Error.Code)
	}
	return nil
}
