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
	"errors"
	"net/http"
	"net/url"
	"strings"
)

// IcebergObject is the registration metadata exposed by Iceberg load responses.
type IcebergObject struct {
	MetadataLocation string `json:"metadata-location"`
}

type registerIcebergObjectRequest struct {
	Name             string `json:"name"`
	MetadataLocation string `json:"metadata-location"`
}

func icebergNamespaceURL(base *url.URL, namespaceSeparator, catalogName string, namespace []string, resource ...string) *url.URL {
	segments := []string{"v1", catalogName, "namespaces", strings.Join(namespace, namespaceSeparator)}
	segments = append(segments, resource...)
	return resourceSegmentsURL(base, segments...)
}

// GetTable loads the current Polaris registration for an Iceberg table.
func (c *Client) GetTable(ctx context.Context, catalogName string, namespace []string, tableName string) (IcebergObject, error) {
	if c.catalogURL == nil {
		return IcebergObject{}, errors.New("catalog URL is not configured")
	}
	var result IcebergObject
	err := c.doJSON(ctx, http.MethodGet, icebergNamespaceURL(c.catalogURL, c.namespaceSeparator, catalogName, namespace, "tables", tableName), nil, &result)
	return result, err
}

// RegisterTable registers an existing Iceberg metadata file without replacing an existing table.
func (c *Client) RegisterTable(ctx context.Context, catalogName string, namespace []string, tableName, metadataLocation string) (IcebergObject, error) {
	if c.catalogURL == nil {
		return IcebergObject{}, errors.New("catalog URL is not configured")
	}
	var result IcebergObject
	endpoint := icebergNamespaceURL(c.catalogURL, c.namespaceSeparator, catalogName, namespace, "register")
	request := registerIcebergObjectRequest{Name: tableName, MetadataLocation: metadataLocation}
	err := c.doJSON(ctx, http.MethodPost, endpoint, request, &result)
	return result, err
}

// UnregisterTable detaches a table from Polaris while preserving its data and metadata files.
func (c *Client) UnregisterTable(ctx context.Context, catalogName string, namespace []string, tableName string) error {
	if c.catalogURL == nil {
		return errors.New("catalog URL is not configured")
	}
	endpoint := icebergNamespaceURL(c.catalogURL, c.namespaceSeparator, catalogName, namespace, "tables", tableName, "unregister")
	return c.doJSON(ctx, http.MethodPost, endpoint, nil, nil)
}

// DeleteTable drops an Iceberg table registration. It does not guarantee data-file deletion.
func (c *Client) DeleteTable(ctx context.Context, catalogName string, namespace []string, tableName string) error {
	if c.catalogURL == nil {
		return errors.New("catalog URL is not configured")
	}
	endpoint := icebergNamespaceURL(c.catalogURL, c.namespaceSeparator, catalogName, namespace, "tables", tableName)
	return c.doJSON(ctx, http.MethodDelete, endpoint, nil, nil)
}

// GetView loads the current Polaris registration for an Iceberg view.
func (c *Client) GetView(ctx context.Context, catalogName string, namespace []string, viewName string) (IcebergObject, error) {
	if c.catalogURL == nil {
		return IcebergObject{}, errors.New("catalog URL is not configured")
	}
	var result IcebergObject
	err := c.doJSON(ctx, http.MethodGet, icebergNamespaceURL(c.catalogURL, c.namespaceSeparator, catalogName, namespace, "views", viewName), nil, &result)
	return result, err
}

// RegisterView registers an existing Iceberg view metadata file without replacing an existing view.
func (c *Client) RegisterView(ctx context.Context, catalogName string, namespace []string, viewName, metadataLocation string) (IcebergObject, error) {
	if c.catalogURL == nil {
		return IcebergObject{}, errors.New("catalog URL is not configured")
	}
	var result IcebergObject
	endpoint := icebergNamespaceURL(c.catalogURL, c.namespaceSeparator, catalogName, namespace, "register-view")
	request := registerIcebergObjectRequest{Name: viewName, MetadataLocation: metadataLocation}
	err := c.doJSON(ctx, http.MethodPost, endpoint, request, &result)
	return result, err
}

// DeleteView drops an Iceberg view registration.
func (c *Client) DeleteView(ctx context.Context, catalogName string, namespace []string, viewName string) error {
	if c.catalogURL == nil {
		return errors.New("catalog URL is not configured")
	}
	endpoint := icebergNamespaceURL(c.catalogURL, c.namespaceSeparator, catalogName, namespace, "views", viewName)
	return c.doJSON(ctx, http.MethodDelete, endpoint, nil, nil)
}
