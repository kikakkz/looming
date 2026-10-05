// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/kikakkz/looming/gateway/internal/front/port"
)

// guideClientTimeout bounds the guide fetch: the page is an onboarding
// surface, and the TTL cache plus last-known-good cover transient
// failures — a wedged topologyd must not stall the front.
const guideClientTimeout = 10 * time.Second

// guideMaxBodyBytes caps the guide response read (the snapshot is a
// few hundred bytes; a runaway payload is a fault, not a guide).
const guideMaxBodyBytes = 1 << 20

// errGuideOrigin marks every transport/decode/non-200 failure from the
// topology origin: the page falls back to last-known-good or 503.
var errGuideOrigin = errors.New("gateway: topology guide origin failure")

// guideDTO is the independently-defined gateway-side shape of
// topology's /v1/internal/guide snapshot (AD-34: the topology
// component owns the wire; this is the consumer's copy of the
// contract).
type guideDTO struct {
	ClusterName    string   `json:"cluster_name"`
	AccessPublic   bool     `json:"access_public"`
	CLIDownloadURL string   `json:"cli_download_url"`
	IdentityURL    string   `json:"identity_url"`
	GatewayURL     string   `json:"gateway_url"`
	Steps          []string `json:"steps"`
	RegisterHint   string   `json:"register_hint"`
}

// GuideClient is the GuidePublisher's driving adapter (topology-l1 §6):
// the topology service's internal guide endpoint over the service
// token, with no caching — the page layer owns freshness policy.
type GuideClient struct {
	baseURL string
	token   string
	http    *http.Client
}

// NewGuideClient wires the client.
func NewGuideClient(baseURL, token string) *GuideClient {
	return &GuideClient{
		baseURL: baseURL,
		token:   token,
		http:    &http.Client{Timeout: guideClientTimeout},
	}
}

var _ port.GuideSource = (*GuideClient)(nil)

// FetchGuide returns the current guide snapshot. Any non-200, decode
// failure, or transport error fails as an errGuideOrigin wrap — the
// caller decides between cached and 503.
func (c *GuideClient) FetchGuide(ctx context.Context) (port.Guide, error) {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return port.Guide{}, fmt.Errorf("%w: bad base URL: %v", errGuideOrigin, err)
	}
	u.Path = "/v1/internal/guide"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return port.Guide{}, fmt.Errorf("%w: build guide request: %v", errGuideOrigin, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return port.Guide{}, fmt.Errorf("%w: guide fetch: %v", errGuideOrigin, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return port.Guide{}, fmt.Errorf("%w: guide status %d", errGuideOrigin, resp.StatusCode)
	}
	var dto guideDTO
	if err := json.NewDecoder(io.LimitReader(resp.Body, guideMaxBodyBytes)).Decode(&dto); err != nil {
		return port.Guide{}, fmt.Errorf("%w: guide decode: %v", errGuideOrigin, err)
	}
	return port.Guide{
		ClusterName:    dto.ClusterName,
		AccessPublic:   dto.AccessPublic,
		CLIDownloadURL: dto.CLIDownloadURL,
		IdentityURL:    dto.IdentityURL,
		GatewayURL:     dto.GatewayURL,
		Steps:          dto.Steps,
		RegisterHint:   dto.RegisterHint,
	}, nil
}
