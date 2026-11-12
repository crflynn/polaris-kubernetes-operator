/*
Copyright 2026 Polaris Kubernetes Operator contributors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package polaris

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// EndpointPolicy restricts endpoints that receive Polaris credentials.
type EndpointPolicy struct {
	allowedAuthorities map[string]struct{}
	allowHTTP          bool
}

// NewEndpointPolicy constructs a policy from exact hostnames or host:port authorities.
// An empty allowlist denies every endpoint.
func NewEndpointPolicy(allowedHosts []string, allowHTTP bool) (*EndpointPolicy, error) {
	policy := &EndpointPolicy{
		allowedAuthorities: make(map[string]struct{}, len(allowedHosts)),
		allowHTTP:          allowHTTP,
	}
	for _, raw := range allowedHosts {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		authority, err := normalizeAuthority(raw, "https")
		if err != nil {
			return nil, fmt.Errorf("invalid allowed Polaris endpoint %q: %w", raw, err)
		}
		policy.allowedAuthorities[authority] = struct{}{}
	}
	return policy, nil
}

func (p *EndpointPolicy) validate(endpoint *url.URL, name string) error {
	if p == nil {
		return nil
	}
	if endpoint.User != nil {
		return fmt.Errorf("%s must not contain URL credentials", name)
	}
	if endpoint.Scheme != schemeHTTPS && (!p.allowHTTP || endpoint.Scheme != schemeHTTP) {
		return fmt.Errorf("%s must use https", name)
	}
	authority, err := normalizeAuthority(endpoint.Host, endpoint.Scheme)
	if err != nil {
		return fmt.Errorf("invalid %s authority: %w", name, err)
	}
	if _, ok := p.allowedAuthorities[authority]; !ok {
		return fmt.Errorf("%s host is not in the operator endpoint allowlist", name)
	}
	return nil
}

func normalizeAuthority(raw, scheme string) (string, error) {
	parsed, err := url.Parse("//" + raw)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("expected a hostname with an optional port")
	}
	hostname := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if hostname == "" {
		return "", errors.New("hostname is empty")
	}
	port := parsed.Port()
	if port == "" {
		switch scheme {
		case schemeHTTPS:
			port = "443"
		case schemeHTTP:
			port = "80"
		default:
			return "", fmt.Errorf("unsupported scheme %q", scheme)
		}
	}
	return net.JoinHostPort(hostname, port), nil
}
