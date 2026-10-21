package services

import (
	"context"
	"fmt"

	"github.com/gherlein/gogl/src/transport"
	"github.com/gherlein/gogl/src/types"
)

// CONFIRMED against a GL-SFT1200 on firmware 4.3.28, 2026-07-28: clients.get_list
// returns {"clients": [...]}.
const (
	clientGroup   = "clients"
	clientGetList = "get_list"
)

type clientList struct {
	Clients []types.Client `json:"clients"`
}

type clientService struct {
	transport transport.Transport
}

// NewClientService returns a read-only view of stations known to the router.
func NewClientService(t transport.Transport) ClientService {
	return &clientService{transport: t}
}

func (s *clientService) List(ctx context.Context) ([]types.Client, error) {
	var list clientList
	if err := s.transport.Call(ctx, clientGroup, clientGetList, nil, &list); err != nil {
		return nil, fmt.Errorf("gogl: list clients: %w", err)
	}
	return list.Clients, nil
}
