// SPDX-License-Identifier: Apache-2.0

package domain

import (
	"errors"
	"fmt"
)

// Access errors name the phase-2 issue that owns each reserved
// transport shape (topology-l1 §5: rejected at apply with the owning
// issue named).
var (
	ErrInvalidAccessMode  = errors.New("topology: invalid access mode")
	ErrTransportNotPhase1 = errors.New("topology: transport reserved for phase 2")
	ErrInvalidEndpoint    = errors.New("topology: invalid endpoint kind")
)

// AccessMode is the guide-page visibility toggle (topology-l1 §7):
// public serves the onboarding guide unauthenticated; private is
// invite-only.
type AccessMode string

const (
	ModePublic  AccessMode = "public"
	ModePrivate AccessMode = "private"
)

// Transport is the ingress transport vocabulary. Phase 1 carries only
// direct (plain IPs, trusted network); the rest are reserved in the
// schema and rejected with their owning phase-2 issue named (#109-#112).
type Transport string

const (
	TransportDirect  Transport = "direct"
	TransportVIP     Transport = "vip"     // #109 gateway HA
	TransportACME    Transport = "acme"    // #110 automated TLS
	TransportDNS     Transport = "dns"     // #111 org DNS
	TransportOverlay Transport = "overlay" // #112 cross-NAT
)

// Endpoint is the phase-1 endpoint kind the guide renders: a plain IP
// endpoint or an HTTP URL (topology-l1 §3).
type Endpoint string

const (
	EndpointIP   Endpoint = "ip"
	EndpointHTTP Endpoint = "http"
)

// Access is the topology's access section: the guide-page mode plus the
// phase-1 ingress shape (topology-l1 §2).
type Access struct {
	Mode      AccessMode
	Transport Transport
	Endpoint  Endpoint
}

// NewAccess validates the access triple at the boundary. Reserved
// transports fail with the owning phase-2 issue named in the message.
func NewAccess(mode, transport, endpoint string) (Access, error) {
	a := Access{Mode: AccessMode(mode), Transport: Transport(transport), Endpoint: Endpoint(endpoint)}
	if err := a.Validate(); err != nil {
		return Access{}, err
	}
	return a, nil
}

// Validate enforces the phase-1 access vocabulary. Hand-built values
// (fields set without NewAccess) are re-checked here — Declare calls it
// at the use-case boundary.
func (a Access) Validate() error {
	switch a.Mode {
	case ModePublic, ModePrivate:
	default:
		return fmt.Errorf("%w: %q", ErrInvalidAccessMode, a.Mode)
	}
	switch a.Transport {
	case TransportDirect:
	case TransportVIP:
		return fmt.Errorf("%w: %q is owned by #109 (gateway HA)", ErrTransportNotPhase1, a.Transport)
	case TransportACME:
		return fmt.Errorf("%w: %q is owned by #110 (automated TLS)", ErrTransportNotPhase1, a.Transport)
	case TransportDNS:
		return fmt.Errorf("%w: %q is owned by #111 (org DNS)", ErrTransportNotPhase1, a.Transport)
	case TransportOverlay:
		return fmt.Errorf("%w: %q is owned by #112 (cross-NAT)", ErrTransportNotPhase1, a.Transport)
	default:
		return fmt.Errorf("%w: unknown transport %q", ErrTransportNotPhase1, a.Transport)
	}
	switch a.Endpoint {
	case EndpointIP, EndpointHTTP:
	default:
		return fmt.Errorf("%w: %q", ErrInvalidEndpoint, a.Endpoint)
	}
	return nil
}
