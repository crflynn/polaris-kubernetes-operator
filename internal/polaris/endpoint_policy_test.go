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
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestEndpointPolicyAllowsConfiguredHTTPSAuthoritiesOnly(t *testing.T) {
	t.Parallel()

	policy, err := NewEndpointPolicy([]string{"polaris.example.com", "auth.example.com:8443"}, false)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name    string
		url     string
		wantErr bool
	}{
		{name: "configured host", url: "https://polaris.example.com/api"},
		{name: "configured host and port", url: "https://auth.example.com:8443/oauth"},
		{name: "unlisted host", url: "https://attacker.example/api", wantErr: true},
		{name: "unlisted port", url: "https://polaris.example.com:8443/api", wantErr: true},
		{name: "http is disabled", url: "http://polaris.example.com/api", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			parsed, err := parseBaseURL(tc.url, "endpoint")
			if err != nil {
				t.Fatal(err)
			}
			err = policy.validate(parsed, "endpoint")
			if (err != nil) != tc.wantErr {
				t.Fatalf("validate(%q) error = %v, wantErr %v", tc.url, err, tc.wantErr)
			}
		})
	}
}

func TestNewClientRejectsUnlistedAuthenticationEndpoint(t *testing.T) {
	t.Parallel()

	policy, err := NewEndpointPolicy([]string{"polaris.example.com"}, false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewClient(Config{
		ManagementURL: testPolarisManagementURL,
		Auth: AuthConfig{
			ClientID:     "client",
			ClientSecret: "secret",
			TokenURL:     "https://attacker.example/token",
		},
		EndpointPolicy: policy,
	})
	if err == nil || !strings.Contains(err.Error(), "token URL host is not in the operator endpoint allowlist") {
		t.Fatalf("NewClient() error = %v, want allowlist rejection", err)
	}
}

func TestEndpointPolicyBlocksRedirectToUnlistedHost(t *testing.T) {
	t.Parallel()

	redirectTargetHit := make(chan struct{}, 1)
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirectTargetHit <- struct{}{}
		w.WriteHeader(http.StatusOK)
	}))
	defer redirectTarget.Close()

	polarisServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, redirectTarget.URL+"/steal", http.StatusTemporaryRedirect)
	}))
	defer polarisServer.Close()

	policy, err := NewEndpointPolicy([]string{strings.TrimPrefix(polarisServer.URL, "http://")}, true)
	if err != nil {
		t.Fatal(err)
	}
	apiClient, err := NewClient(Config{
		ManagementURL:  polarisServer.URL,
		Auth:           AuthConfig{BearerToken: "test-token"},
		EndpointPolicy: policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := apiClient.Check(context.Background()); err == nil {
		t.Fatal("Check() succeeded after an unlisted-host redirect")
	}
	select {
	case <-redirectTargetHit:
		t.Fatal("redirect target received the authenticated request")
	default:
	}
}

func TestEndpointPolicyLimitsRedirects(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	polarisServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Redirect(w, r, r.URL.RequestURI(), http.StatusTemporaryRedirect)
	}))
	defer polarisServer.Close()

	policy, err := NewEndpointPolicy([]string{strings.TrimPrefix(polarisServer.URL, "http://")}, true)
	if err != nil {
		t.Fatal(err)
	}
	apiClient, err := NewClient(Config{
		ManagementURL:  polarisServer.URL,
		Auth:           AuthConfig{BearerToken: testToken},
		EndpointPolicy: policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := apiClient.Check(context.Background()); err == nil {
		t.Fatal("Check() succeeded after redirect limit was exceeded")
	}
	if got := requests.Load(); got != maxHTTPRedirects {
		t.Fatalf("Polaris endpoint requests = %d, want %d", got, maxHTTPRedirects)
	}
}

func TestNewEndpointPolicyRejectsMalformedAllowlistEntries(t *testing.T) {
	t.Parallel()

	for _, entry := range []string{"https://polaris.example.com", "user@polaris.example.com", "polaris.example.com/path", "polaris.example.com:bad"} {
		if _, err := NewEndpointPolicy([]string{entry}, false); err == nil {
			t.Errorf("NewEndpointPolicy(%q) accepted malformed entry", entry)
		}
	}
}
func TestEndpointPolicyEmptyAllowlistAllowsAnyHTTPSHost(t *testing.T) {
	t.Parallel()

	policy, err := NewEndpointPolicy(nil, false)
	if err != nil {
		t.Fatal(err)
	}
	httpsEndpoint, err := parseBaseURL("https://polaris.example.com/api", "management URL")
	if err != nil {
		t.Fatal(err)
	}
	if err := policy.validate(httpsEndpoint, "management URL"); err != nil {
		t.Fatalf("empty allowlist rejected HTTPS host: %v", err)
	}

	httpEndpoint, err := parseBaseURL("http://polaris.example.com/api", "management URL")
	if err != nil {
		t.Fatal(err)
	}
	if err := policy.validate(httpEndpoint, "management URL"); err == nil {
		t.Fatal("empty allowlist allowed HTTP without the explicit opt-in")
	}
}
