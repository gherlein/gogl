package mock

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gherlein/gogl/src/types"
)

// Group and method names.
//
// PROVISIONAL: these are placeholders pending capture from a live SFT1200 (see
// docs/plan.md Phase 0). GL.iNet's official 4.x API reference is no longer
// public. They are declared here, and once per service file, so that correcting
// them against real hardware touches a handful of lines rather than a sweep.
// All CONFIRMED against a GL-SFT1200 running firmware 4.3.28 (2026-07-28). GL.iNet
// calls a reservation a "static bind".
const (
	ClientGroup = "clients"
	SystemGroup = "system"

	// NetworkGroup and ReservationGroup are both "lan": the firmware puts the
	// interface configuration and the static binds in one group.
	NetworkGroup     = "lan"
	ReservationGroup = "lan"
	DHCPLeaseGroup   = "network"

	// HostsGroup carries the router's hosts file, which is where DNS names live.
	HostsGroup = "dns"

	// WirelessGroup carries SSIDs and passphrases.
	WirelessGroup = "wifi"

	MethodGetHost = "get_host"
	MethodSetHost = "set_host"

	// MethodGetConfig and MethodSetWirelessConfig read and write wireless identity.
	MethodGetConfig         = "get_config"
	MethodSetWirelessConfig = "set_config"

	MethodSetConfigList = "set_config"

	MethodGetList        = "get_list"
	MethodGetInfo        = "get_info"
	MethodGetConfigList  = "get_config_list"
	MethodGetStaticBinds = "get_static_bind_list"
	MethodAddStaticBind  = "add_static_bind"
	MethodSetStaticBind  = "set_static_bind"
	MethodRemoveBind     = "remove_static_bind"
	MethodGetDHCPLeases  = "get_dhcp_leases"
)

// removeModeAll is the mode value that clears every binding at once. The mock
// honors it so a test can prove gogl never sends it.
const removeModeAll = 1

// reservationsKey is the field lan.get_static_bind_list wraps the list in.
const reservationsKey = "static_bind_list"

// reservationTable mirrors the wire shape the services use.
type reservationTable struct {
	Res []types.Reservation `json:"static_bind_list"`
}

// LoadFixture registers the result payload returned for group.method.
func (s *Server) LoadFixture(group, method string, payload json.RawMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state[group+"."+method] = payload
}

// SetReservations seeds the mock's reservation table.
func (s *Server) SetReservations(res []types.Reservation) {
	if res == nil {
		res = []types.Reservation{}
	}
	payload, err := json.Marshal(reservationTable{Res: res})
	if err != nil {
		s.t.Errorf("mock: marshal reservations: %v", err)
		return
	}
	s.LoadFixture(ReservationGroup, MethodGetStaticBinds, payload)
}

// Reservations returns the mock's current reservation table so tests can assert
// on what was written, not only on what a call returned.
func (s *Server) Reservations() []types.Reservation {
	s.mu.Lock()
	payload, ok := s.state[ReservationGroup+"."+MethodGetStaticBinds]
	s.mu.Unlock()
	if !ok {
		return nil
	}

	var table reservationTable
	if err := json.Unmarshal(payload, &table); err != nil {
		s.t.Errorf("mock: unmarshal reservations: %v", err)
		return nil
	}
	return table.Res
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
		s.writeError(w, req.ID, CodeBadRequest, "call params must be [sid, group, method, args]")
		return
	}

	var sid, group, method string
	if err := json.Unmarshal(params[0], &sid); err != nil {
		s.writeError(w, req.ID, CodeBadRequest, "bad sid")
		return
	}
	if err := json.Unmarshal(params[1], &group); err != nil {
		s.writeError(w, req.ID, CodeBadRequest, "bad group")
		return
	}
	if err := json.Unmarshal(params[2], &method); err != nil {
		s.writeError(w, req.ID, CodeBadRequest, "bad method")
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.sessionValidLocked(sid) {
		s.writeErrorLocked(w, req.ID, CodeAccessDenied, "Access denied")
		return
	}
	s.sidSeen = time.Now()

	key := group + "." + method
	if fault, ok := s.failures[key]; ok {
		delete(s.failures, key)
		s.writeErrorLocked(w, req.ID, fault.Code, fault.Message)
		return
	}

	// The static-bind writes mutate the stored table, so that writes are
	// observable through Reservations().
	if group == ReservationGroup && isBindWrite(method) {
		if len(params) < 4 {
			s.writeErrorLocked(w, req.ID, CodeBadRequest, method+" requires args")
			return
		}
		s.applyBindWriteLocked(w, req, method, params[3])
		return
	}

	// wifi.set_config writes one interface's fields, identified by iface_name.
	if group == WirelessGroup && method == MethodSetWirelessConfig {
		if len(params) < 4 {
			s.writeErrorLocked(w, req.ID, CodeBadRequest, "set_config requires args")
			return
		}
		var args struct {
			IfaceName  string  `json:"iface_name"`
			SSID       *string `json:"ssid"`
			Key        *string `json:"key"`
			Encryption *string `json:"encryption"`
			Hidden     *bool   `json:"hidden"`
			Enabled    *bool   `json:"enabled"`

			Device  string  `json:"device"`
			Channel *int    `json:"channel"`
			HTMode  *string `json:"htmode"`
			HWMode  *string `json:"hwmode"`
			TXPower *string `json:"txpower"`
		}
		if err := json.Unmarshal(params[3], &args); err != nil {
			s.writeErrorLocked(w, req.ID, CodeBadRequest, "bad set_config args")
			return
		}
		// The firmware scopes these fields two ways: interface-scoped fields require
		// iface_name, radio-scoped ones require device. Reproducing that pairing rule
		// means a test cannot pass on a call the device would reject for omitting the
		// key that scopes the write.
		radioScoped := args.Channel != nil || args.HTMode != nil ||
			args.HWMode != nil || args.TXPower != nil
		ifaceScoped := args.SSID != nil || args.Key != nil ||
			args.Encryption != nil || args.Hidden != nil || args.Enabled != nil

		if ifaceScoped && args.IfaceName == "" {
			s.writeErrorLocked(w, req.ID, CodeBadRequest, "Invalid params (iface_name required)")
			return
		}
		if radioScoped && args.Device == "" {
			s.writeErrorLocked(w, req.ID, CodeBadRequest, "Invalid params (device required)")
			return
		}
		if !ifaceScoped && !radioScoped {
			s.writeErrorLocked(w, req.ID, CodeBadRequest, "Invalid params (nothing to set)")
			return
		}
		if args.SSID != nil && (*args.SSID == "" || len(*args.SSID) > types.MaxSSIDLength) {
			s.writeErrorLocked(w, req.ID, CodeBadRequest, "Invalid params (ssid length)")
			return
		}
		if args.Key != nil && (len(*args.Key) < types.MinKeyLength || len(*args.Key) > types.MaxKeyLength) {
			s.writeErrorLocked(w, req.ID, CodeBadRequest, "Invalid params (key length)")
			return
		}
		if radioScoped {
			if !s.applyRadioWriteLocked(args.Device, args.Channel, args.HTMode, args.HWMode, args.TXPower) {
				s.writeErrorLocked(w, req.ID, CodeNotFound, "device not found")
				return
			}
		}
		if ifaceScoped {
			if !s.applyWirelessWriteLocked(args.IfaceName, args.SSID, args.Key,
				args.Encryption, args.Hidden, args.Enabled) {
				s.writeErrorLocked(w, req.ID, CodeNotFound, "interface not found")
				return
			}
		}
		s.writeResultLocked(w, req.ID, map[string]any{"err_code": 0})
		return
	}

	// dns.set_host replaces the stored host-file content.
	if group == HostsGroup && method == MethodSetHost {
		if len(params) < 4 {
			s.writeErrorLocked(w, req.ID, CodeBadRequest, "set_host requires args")
			return
		}
		var args struct {
			Content string `json:"content"`
		}
		if err := json.Unmarshal(params[3], &args); err != nil {
			s.writeErrorLocked(w, req.ID, CodeBadRequest, "bad set_host args")
			return
		}
		// Reproduce the firmware's content validation, not just its happy path.
		//
		// This is here because its absence let a real bug ship: gogl wrote a marker
		// line containing parentheses, every test passed, and the device answered
		// -32602. A mock that accepts whatever its author sends cannot catch that,
		// and the author of the mock is the author of the code under test.
		if i := strings.IndexAny(args.Content, "()="); i >= 0 {
			s.writeErrorLocked(w, req.ID, CodeBadRequest,
				fmt.Sprintf("Invalid params (content contains %q at offset %d)", args.Content[i], i))
			return
		}
		stored, err := json.Marshal(map[string]string{"content": args.Content})
		if err != nil {
			s.t.Errorf("mock: marshal host file: %v", err)
			s.writeErrorLocked(w, req.ID, CodeBadRequest, "mock marshal failure")
			return
		}
		s.state[HostsGroup+"."+MethodGetHost] = stored
		s.writeResultLocked(w, req.ID, []any{})
		return
	}

	// lan.set_config replaces the named interface in the stored list.
	if group == NetworkGroup && method == MethodSetConfigList {
		if len(params) < 4 {
			s.writeErrorLocked(w, req.ID, CodeBadRequest, "set_config requires args")
			return
		}
		s.applyNetworkWriteLocked(w, req, params[3])
		return
	}

	payload, ok := s.state[key]
	if !ok {
		s.writeErrorLocked(w, req.ID, CodeNotFound, "no fixture for "+key)
		return
	}
	s.writeResultLocked(w, req.ID, json.RawMessage(payload))
}

// SetHostFile seeds the mock's host-file content.
func (s *Server) SetHostFile(content string) {
	payload, err := json.Marshal(map[string]string{"content": content})
	if err != nil {
		s.t.Errorf("mock: marshal host file: %v", err)
		return
	}
	s.LoadFixture(HostsGroup, MethodGetHost, payload)
}

// HostFile returns the mock's current host-file content, so a test can assert on
// what was written rather than only on what a call returned.
func (s *Server) HostFile() string {
	s.mu.Lock()
	payload, ok := s.state[HostsGroup+"."+MethodGetHost]
	s.mu.Unlock()
	if !ok {
		return ""
	}
	var wrapper struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(payload, &wrapper); err != nil {
		s.t.Errorf("mock: unmarshal host file: %v", err)
		return ""
	}
	return wrapper.Content
}

// Network returns the interface list the mock currently reports.
func (s *Server) Network() []types.Network {
	s.mu.Lock()
	payload, ok := s.state[NetworkGroup+"."+MethodGetConfigList]
	s.mu.Unlock()
	if !ok {
		return nil
	}
	var wrapper struct {
		Interfaces []types.Network `json:"interfaces"`
	}
	if err := json.Unmarshal(payload, &wrapper); err != nil {
		s.t.Errorf("mock: unmarshal interfaces: %v", err)
		return nil
	}
	return wrapper.Interfaces
}

func isBindWrite(method string) bool {
	switch method {
	case MethodAddStaticBind, MethodSetStaticBind, MethodRemoveBind:
		return true
	}
	return false
}

// bindArgs is the argument shape the three static-bind writes take.
type bindArgs struct {
	Name string `json:"name"`
	MAC  string `json:"mac"`
	IP   string `json:"ip"`
	Mode int    `json:"mode"`
}

// applyBindWriteLocked mutates the stored table per the firmware's semantics.
// Callers must hold s.mu.
func (s *Server) applyBindWriteLocked(w http.ResponseWriter, req *request, method string, raw json.RawMessage) {
	var args bindArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		s.writeErrorLocked(w, req.ID, CodeBadRequest, "bad "+method+" args")
		return
	}

	current := s.reservationsLocked()

	switch method {
	case MethodAddStaticBind:
		current = append(current, types.Reservation{Name: args.Name, MAC: args.MAC, IP: args.IP})

	case MethodSetStaticBind:
		// The firmware matches on MAC and silently does nothing for an unknown one.
		for i := range current {
			if strings.EqualFold(current[i].MAC, args.MAC) {
				current[i] = types.Reservation{Name: args.Name, MAC: args.MAC, IP: args.IP}
				break
			}
		}

	case MethodRemoveBind:
		if args.Mode == removeModeAll {
			current = nil
			break
		}
		kept := make([]types.Reservation, 0, len(current))
		for _, r := range current {
			if !strings.EqualFold(r.MAC, args.MAC) {
				kept = append(kept, r)
			}
		}
		current = kept
	}

	s.storeReservationsLocked(current)
	s.writeResultLocked(w, req.ID, map[string]any{})
}

// reservationsLocked reads the stored table. Callers must hold s.mu.
func (s *Server) reservationsLocked() []types.Reservation {
	payload, ok := s.state[ReservationGroup+"."+MethodGetStaticBinds]
	if !ok {
		return nil
	}
	var table reservationTable
	if err := json.Unmarshal(payload, &table); err != nil {
		s.t.Errorf("mock: unmarshal reservations: %v", err)
		return nil
	}
	return table.Res
}

// storeReservationsLocked writes the table back. Callers must hold s.mu.
func (s *Server) storeReservationsLocked(res []types.Reservation) {
	if res == nil {
		res = []types.Reservation{}
	}
	payload, err := json.Marshal(reservationTable{Res: res})
	if err != nil {
		s.t.Errorf("mock: marshal reservations: %v", err)
		return
	}
	s.state[ReservationGroup+"."+MethodGetStaticBinds] = payload
}

// applyNetworkWriteLocked replaces the addressed interface in the stored list.
// Callers must hold s.mu.
func (s *Server) applyNetworkWriteLocked(w http.ResponseWriter, req *request, raw json.RawMessage) {
	var args struct {
		Interface string `json:"interface"`
		IP        string `json:"ip"`
		Netmask   string `json:"netmask"`
		Start     string `json:"start"`
		End       string `json:"end"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		s.writeErrorLocked(w, req.ID, CodeBadRequest, "bad set_config args")
		return
	}

	var wrapper struct {
		Interfaces []types.Network `json:"interfaces"`
	}
	if payload, ok := s.state[NetworkGroup+"."+MethodGetConfigList]; ok {
		if err := json.Unmarshal(payload, &wrapper); err != nil {
			s.t.Errorf("mock: unmarshal interfaces: %v", err)
		}
	}

	updated := false
	for i := range wrapper.Interfaces {
		if wrapper.Interfaces[i].Interface != args.Interface {
			continue
		}
		wrapper.Interfaces[i].LANIP = args.IP
		wrapper.Interfaces[i].Netmask = args.Netmask
		wrapper.Interfaces[i].DHCPStart = args.Start
		wrapper.Interfaces[i].DHCPStop = args.End
		updated = true
	}
	if !updated {
		wrapper.Interfaces = append(wrapper.Interfaces, types.Network{
			Interface: args.Interface, LANIP: args.IP, Netmask: args.Netmask,
			DHCPStart: args.Start, DHCPStop: args.End, DHCPEnabled: true,
		})
	}

	payload, err := json.Marshal(wrapper)
	if err != nil {
		s.t.Errorf("mock: marshal interfaces: %v", err)
		s.writeErrorLocked(w, req.ID, CodeBadRequest, "mock marshal failure")
		return
	}
	s.state[NetworkGroup+"."+MethodGetConfigList] = payload
	s.writeResultLocked(w, req.ID, map[string]any{})
}
