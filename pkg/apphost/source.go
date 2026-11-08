package apphost

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// maxRegistryBytes bounds one application-registry read.
const maxRegistryBytes = 8 << 20

// ControlPlaneSource reads the installed `App` contributions from the control
// plane's application registry. The control plane owns the package, its digest
// and the component lifecycle; this product validates what it reads and
// surfaces only what satisfies KOSD-C-73. The read runs as the caller's
// session, so an account that may not see a component sees no application
// either (KOSD-C-59).
type ControlPlaneSource struct {
	// BaseURL is the control-plane API base URL; required.
	BaseURL *url.URL
	// Token is the caller's control-plane session.
	Token string
	// HTTP is the client every read uses; defaults to a bounded one.
	HTTP *http.Client
}

// registryAnswer is the control plane's application-registry read.
type registryAnswer struct {
	Items []Contribution `json:"items"`
}

// Contributions reads every installed `App` contribution.
func (s *ControlPlaneSource) Contributions(ctx context.Context) ([]Contribution, error) {
	if s.BaseURL == nil {
		return nil, fmt.Errorf("apphost: an upstream control plane is required")
	}
	client := s.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	target := *s.BaseURL
	target.Path = strings.TrimSuffix(target.Path, "/") + "/api/v1/apps"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("apphost: build the registry read: %w", err)
	}
	if s.Token != "" {
		request.Header.Set("Authorization", "Bearer "+s.Token)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("apphost: read the application registry: %w", err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxRegistryBytes))
	if err != nil {
		return nil, fmt.Errorf("apphost: read the application registry: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, fmt.Errorf("apphost: the application registry answered %d", response.StatusCode)
	}
	var answer registryAnswer
	if err := json.Unmarshal(payload, &answer); err != nil {
		return nil, fmt.Errorf("apphost: decode the application registry: %w", err)
	}
	return answer.Items, nil
}
