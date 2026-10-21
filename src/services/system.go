package services

import (
	"context"
	"fmt"

	"github.com/gherlein/gogl/src/transport"
	"github.com/gherlein/gogl/src/types"
)

// CONFIRMED against a GL-SFT1200 on firmware 4.3.28, 2026-07-28.
//
// system.get_info carries model, firmware_version and mac. system.get_status also
// exists but returns interface/wifi/service state rather than device identity, so
// it is the wrong endpoint for this service.
const (
	systemGroup   = "system"
	systemGetInfo = "get_info"
)

type systemService struct {
	transport transport.Transport
}

// NewSystemService returns a read-only view of device identity.
func NewSystemService(t transport.Transport) SystemService {
	return &systemService{transport: t}
}

func (s *systemService) Info(ctx context.Context) (*types.SystemInfo, error) {
	var info types.SystemInfo
	if err := s.transport.Call(ctx, systemGroup, systemGetInfo, nil, &info); err != nil {
		return nil, fmt.Errorf("gogl: read system info: %w", err)
	}
	return &info, nil
}
