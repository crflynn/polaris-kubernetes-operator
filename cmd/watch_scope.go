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
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type namespaceWatchScope struct {
	namespaces []string
	selector   labels.Selector
	restricted bool
}

func parseNamespaceWatchScope(
	watchNamespaces, namespaceSelector string,
	namespaceScope bool,
	podNamespace string,
) (namespaceWatchScope, error) {
	scope := namespaceWatchScope{}
	seen := make(map[string]struct{})
	if namespaceScope {
		if podNamespace == "" {
			return scope, fmt.Errorf("POD_NAMESPACE is required when NAMESPACE_SCOPE is enabled")
		}
		if err := addWatchNamespace(&scope.namespaces, seen, podNamespace); err != nil {
			return scope, err
		}
	}

	for namespace := range strings.SplitSeq(watchNamespaces, ",") {
		namespace = strings.TrimSpace(namespace)
		if namespace == "" {
			continue
		}
		if err := addWatchNamespace(&scope.namespaces, seen, namespace); err != nil {
			return scope, err
		}
	}

	if strings.TrimSpace(namespaceSelector) != "" {
		selector, err := labels.Parse(namespaceSelector)
		if err != nil {
			return scope, fmt.Errorf("parse WATCH_NAMESPACE_SELECTOR: %w", err)
		}
		scope.selector = selector
	}
	scope.restricted = namespaceScope || len(scope.namespaces) > 0 || scope.selector != nil
	return scope, nil
}

func addWatchNamespace(namespaces *[]string, seen map[string]struct{}, namespace string) error {
	if problems := validation.IsDNS1123Label(namespace); len(problems) > 0 {
		return fmt.Errorf("invalid watched namespace %q: %s", namespace, strings.Join(problems, "; "))
	}
	if _, ok := seen[namespace]; ok {
		return nil
	}
	seen[namespace] = struct{}{}
	*namespaces = append(*namespaces, namespace)
	return nil
}

func namespaceCacheOptions(
	ctx context.Context,
	restConfig *rest.Config,
	scope namespaceWatchScope,
) (cache.Options, error) {
	if !scope.restricted {
		return cache.Options{}, nil
	}

	matchedNamespaces := []string(nil)
	if scope.selector != nil {
		discoveryClient, err := client.New(restConfig, client.Options{Scheme: scheme})
		if err != nil {
			return cache.Options{}, fmt.Errorf("create namespace discovery client: %w", err)
		}
		list := &corev1.NamespaceList{}
		if err := discoveryClient.List(ctx, list, client.MatchingLabelsSelector{Selector: scope.selector}); err != nil {
			return cache.Options{}, fmt.Errorf("list namespaces matching WATCH_NAMESPACE_SELECTOR: %w", err)
		}
		matchedNamespaces = make([]string, 0, len(list.Items))
		for i := range list.Items {
			matchedNamespaces = append(matchedNamespaces, list.Items[i].Name)
		}
	}
	return namespaceCacheOptionsFor(scope, matchedNamespaces)
}

func namespaceCacheOptionsFor(scope namespaceWatchScope, matchedNamespaces []string) (cache.Options, error) {
	options := cache.Options{}
	if !scope.restricted {
		return options, nil
	}

	namespaces := make(map[string]cache.Config, len(scope.namespaces)+len(matchedNamespaces))
	for _, namespace := range scope.namespaces {
		namespaces[namespace] = cache.Config{}
	}
	for _, namespace := range matchedNamespaces {
		namespaces[namespace] = cache.Config{}
	}
	if len(namespaces) == 0 {
		return options, fmt.Errorf("namespace watch scope matched no namespaces")
	}
	options.DefaultNamespaces = namespaces
	return options, nil
}
