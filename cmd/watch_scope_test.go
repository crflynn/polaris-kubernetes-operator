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

package main

import (
	"reflect"
	"testing"
)

const (
	watchTestNamespace         = "team-a"
	watchTestOtherNamespace    = "team-b"
	watchTestSelectorNamespace = "team-c"
	watchTestOperatorNamespace = "operators"
)

func TestParseNamespaceWatchScope(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		watchNamespaces   string
		namespaceSelector string
		namespaceScope    bool
		podNamespace      string
		wantNamespaces    []string
		wantRestricted    bool
		wantSelector      string
		wantError         bool
	}{
		{name: "cluster wide by default"},
		{
			name:            "explicit namespace list",
			watchNamespaces: watchTestNamespace + ", " + watchTestOtherNamespace + "," + watchTestNamespace,
			wantNamespaces:  []string{watchTestNamespace, watchTestOtherNamespace},
			wantRestricted:  true,
		},
		{
			name:           "operator namespace scope",
			namespaceScope: true,
			podNamespace:   watchTestOperatorNamespace,
			wantNamespaces: []string{watchTestOperatorNamespace},
			wantRestricted: true,
		},
		{
			name:              "namespace scope with explicit and selector",
			watchNamespaces:   watchTestNamespace,
			namespaceSelector: "environment in (dev,qa)",
			namespaceScope:    true,
			podNamespace:      watchTestOperatorNamespace,
			wantNamespaces:    []string{watchTestOperatorNamespace, watchTestNamespace},
			wantRestricted:    true,
			wantSelector:      "environment in (dev,qa)",
		},
		{name: "missing pod namespace", namespaceScope: true, wantError: true},
		{name: "invalid namespace", watchNamespaces: "bad/name", wantError: true},
		{name: "invalid selector", namespaceSelector: "environment in (", wantError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			scope, err := parseNamespaceWatchScope(tt.watchNamespaces, tt.namespaceSelector, tt.namespaceScope, tt.podNamespace)
			if (err != nil) != tt.wantError {
				t.Fatalf("parseNamespaceWatchScope() error = %v, wantError %v", err, tt.wantError)
			}
			if err != nil {
				return
			}
			if !reflect.DeepEqual(scope.namespaces, tt.wantNamespaces) {
				t.Errorf("namespaces = %v, want %v", scope.namespaces, tt.wantNamespaces)
			}
			if scope.restricted != tt.wantRestricted {
				t.Errorf("restricted = %v, want %v", scope.restricted, tt.wantRestricted)
			}
			gotSelector := ""
			if scope.selector != nil {
				gotSelector = scope.selector.String()
			}
			if gotSelector != tt.wantSelector {
				t.Errorf("selector = %q, want %q", gotSelector, tt.wantSelector)
			}
		})
	}
}

func TestNamespaceCacheOptionsForRestrictsNamespaces(t *testing.T) {
	t.Parallel()

	scope, err := parseNamespaceWatchScope(watchTestNamespace, "environment=dev", false, "")
	if err != nil {
		t.Fatalf("parseNamespaceWatchScope() error = %v", err)
	}
	options, err := namespaceCacheOptionsFor(scope, []string{watchTestOtherNamespace, watchTestSelectorNamespace})
	if err != nil {
		t.Fatalf("namespaceCacheOptionsFor() error = %v", err)
	}

	want := map[string]struct{}{
		watchTestNamespace:         {},
		watchTestOtherNamespace:    {},
		watchTestSelectorNamespace: {},
	}
	if len(options.DefaultNamespaces) != len(want) {
		t.Fatalf("DefaultNamespaces = %v, want %v", options.DefaultNamespaces, want)
	}
	for namespace := range options.DefaultNamespaces {
		if _, ok := want[namespace]; !ok {
			t.Errorf("unexpected watched namespace %q", namespace)
		}
	}
}

func TestNamespaceCacheOptionsForRejectsEmptyScope(t *testing.T) {
	t.Parallel()

	_, err := namespaceCacheOptionsFor(namespaceWatchScope{restricted: true}, nil)
	if err == nil {
		t.Fatal("namespaceCacheOptionsFor() error = nil, want an empty-scope error")
	}
}
