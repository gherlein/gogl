package services_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gherlein/gogl/src/mock"
	"github.com/gherlein/gogl/src/services"
	"github.com/gherlein/gogl/src/transport"
)

func TestSystemInfo(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	s.LoadFixture(mock.SystemGroup, mock.MethodGetInfo, json.RawMessage(
		`{"model":"gl-sft1200","firmware_version":"4.3.28","mac":"aa:bb:cc:dd:ee:00","uptime":86400}`))

	got, err := services.NewSystemService(newTransport(t, s)).Info(context.Background())
	if err != nil {
		t.Fatalf("Info error: %v", err)
	}
	if got.Model != "gl-sft1200" {
		t.Errorf("Model = %q, want gl-sft1200", got.Model)
	}
	if got.Firmware != "4.3.28" {
		t.Errorf("Firmware = %q, want 4.3.28", got.Firmware)
	}
	if got.Uptime != 86400 {
		t.Errorf("Uptime = %d, want 86400", got.Uptime)
	}
}

func TestSystemInfoPropagatesError(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	s.LoadFixture(mock.SystemGroup, mock.MethodGetInfo, json.RawMessage(`{}`))
	s.FailNext(mock.SystemGroup, mock.MethodGetInfo, mock.CodeNotFound, "injected")

	_, err := services.NewSystemService(newTransport(t, s)).Info(context.Background())
	if err == nil {
		t.Fatal("Info succeeded, want error")
	}
	var rpcErr *transport.Error
	if !errors.As(err, &rpcErr) {
		t.Errorf("error is %T, want a wrapped *transport.Error", err)
	}
}

func TestSystemInfoHonoursContext(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	s.LoadFixture(mock.SystemGroup, mock.MethodGetInfo, json.RawMessage(`{}`))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := services.NewSystemService(newTransport(t, s)).Info(ctx); err == nil {
		t.Error("Info with a cancelled context succeeded")
	}
}
