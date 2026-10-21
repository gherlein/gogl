package services_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gherlein/gogl/src/mock"
	"github.com/gherlein/gogl/src/services"
	"github.com/gherlein/gogl/src/types"
)

// Field names and iface values as firmware 4.3.28 actually reports them.
const clientsFixture = `{"clients":[
  {"mac":"aa:bb:cc:dd:ee:01","ip":"192.168.8.13","name":"nas","online":true,"iface":"cable","total_rx":100,"total_tx":200},
  {"mac":"aa:bb:cc:dd:ee:02","ip":"192.168.8.14","name":"phone","online":true,"iface":"5G"},
  {"mac":"aa:bb:cc:dd:ee:03","online":false}
]}`

func TestClientList(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	s.LoadFixture(mock.ClientGroup, mock.MethodGetList, json.RawMessage(clientsFixture))

	got, err := services.NewClientService(newTransport(t, s)).List(context.Background())
	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("List returned %d, want 3", len(got))
	}
	if !got[0].IsWired() {
		t.Error("first client should be wired")
	}
	if got[1].Band() != "5G" {
		t.Errorf("second client Band() = %q, want 5G", got[1].Band())
	}
	if got[1].IsWired() {
		t.Error("a 5G client should not be wired")
	}
	// A client with no reported name falls back rather than showing empty.
	if got[2].Hostname() != types.UnknownHostname {
		t.Errorf("third client Hostname() = %q, want %q", got[2].Hostname(), types.UnknownHostname)
	}
	if got[2].IP != "" {
		t.Errorf("third client IP = %q, want empty", got[2].IP)
	}
}

func TestClientListEmpty(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	s.LoadFixture(mock.ClientGroup, mock.MethodGetList, json.RawMessage(`{"clients":[]}`))

	got, err := services.NewClientService(newTransport(t, s)).List(context.Background())
	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("List returned %d entries, want 0", len(got))
	}
}

func TestClientListPropagatesError(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	s.LoadFixture(mock.ClientGroup, mock.MethodGetList, json.RawMessage(`{}`))
	s.FailNext(mock.ClientGroup, mock.MethodGetList, mock.CodeNotFound, "injected")

	if _, err := services.NewClientService(newTransport(t, s)).List(context.Background()); err == nil {
		t.Error("List succeeded, want error")
	}
}

// String values for counters, as some firmware versions return them.
func TestClientListWithStrings(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	s.LoadFixture(mock.ClientGroup, mock.MethodGetList, json.RawMessage(`{"clients":[
		{"mac":"aa:bb:cc:dd:ee:01","ip":"192.168.8.13","name":"nas","online":true,"iface":"cable","total_rx":"100","total_tx":"200"}
	]}`))

	got, err := services.NewClientService(newTransport(t, s)).List(context.Background())
	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("List returned %d, want 1", len(got))
	}
	if got[0].TXBytes.Uint64() != 200 || got[0].RXBytes.Uint64() != 100 {
		t.Errorf("counters = tx %d, rx %d; want 200, 100", got[0].TXBytes.Uint64(), got[0].RXBytes.Uint64())
	}
}
