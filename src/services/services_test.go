package services_test

import (
	"testing"

	"github.com/gherlein/gogl/src/mock"
	"github.com/gherlein/gogl/src/transport"
)

// newTransport wires a Transport to a mock server. Keepalive is off: these tests
// are about service behavior, not session management, which transport's own tests
// cover.
func newTransport(t *testing.T, s *mock.Server) transport.Transport {
	t.Helper()
	tr := transport.New(transport.Config{
		URL: s.URL(), Username: s.Username(), Password: s.Password(),
		KeepaliveInterval: -1,
	})
	t.Cleanup(func() {
		if err := tr.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return tr
}

// withDomain configures a DNS domain on the mock, which reservation writes require.
func withDomain(t *testing.T, s *mock.Server, domain string) {
	t.Helper()
	s.SetHostFile(mock.FactoryHostFile +
		mock.HostFileWith(domain))
}
