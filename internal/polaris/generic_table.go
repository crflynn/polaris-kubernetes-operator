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
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// ErrUnsupported indicates that the configured Polaris endpoint does not expose
// the Generic Table API.
var ErrUnsupported = errors.New("polaris Generic Table API is unsupported")

// GenericTable is the non-secret representation of a Polaris generic table.
type GenericTable struct {
	Name         string            `json:"name"`
	Format       string            `json:"format"`
	BaseLocation string            `json:"base-location,omitempty"`
	Doc          string            `json:"doc,omitempty"`
	Properties   map[string]string `json:"properties,omitempty"`
}

// GenericTableClient uses an authenticated Polaris client for generic-table
// catalog operations.
type GenericTableClient struct {
	client *Client
}

// NewGenericTableClient creates a Generic Table API client using the supplied
// Polaris connection and authentication settings.
func NewGenericTableClient(client *Client) *GenericTableClient {
	return &GenericTableClient{client: client}
}

type genericTableResponse struct {
	Table GenericTable `json:"table"`
}

// GetTable loads a generic table from a Polaris catalog namespace.
func (c *GenericTableClient) GetTable(ctx context.Context, catalogName string, namespace []string, tableName string) (GenericTable, error) {
	if c == nil || c.client == nil || c.client.catalogURL == nil {
		return GenericTable{}, ErrUnsupported
	}
	var response genericTableResponse
	err := c.client.doJSON(ctx, http.MethodGet, genericTableResourceURL(c.client.catalogURL, c.client.namespaceSeparator, catalogName, namespace, tableName), nil, &response)
	if isGenericTableUnsupported(err) {
		return GenericTable{}, fmt.Errorf("%w: %v", ErrUnsupported, err)
	}
	return response.Table, err
}

// CreateTable creates a generic table in a Polaris catalog namespace.
func (c *GenericTableClient) CreateTable(ctx context.Context, catalogName string, namespace []string, table GenericTable) (GenericTable, error) {
	if c == nil || c.client == nil || c.client.catalogURL == nil {
		return GenericTable{}, ErrUnsupported
	}
	var response genericTableResponse
	endpoint := genericTablesResourceURL(c.client.catalogURL, c.client.namespaceSeparator, catalogName, namespace)
	err := c.client.doJSON(ctx, http.MethodPost, endpoint, table, &response)
	if isGenericTableUnsupported(err) {
		return GenericTable{}, fmt.Errorf("%w: %v", ErrUnsupported, err)
	}
	return response.Table, err
}

// DeleteTable removes a generic table from the Polaris catalog. Polaris may
// retain the table's backing files; this operation only removes catalog state.
func (c *GenericTableClient) DeleteTable(ctx context.Context, catalogName string, namespace []string, tableName string) error {
	if c == nil || c.client == nil || c.client.catalogURL == nil {
		return ErrUnsupported
	}
	err := c.client.doJSON(ctx, http.MethodDelete, genericTableResourceURL(c.client.catalogURL, c.client.namespaceSeparator, catalogName, namespace, tableName), nil, nil)
	if isGenericTableUnsupported(err) {
		return fmt.Errorf("%w: %v", ErrUnsupported, err)
	}
	return err
}

func genericTablesResourceURL(base *url.URL, namespaceSeparator, catalogName string, namespace []string) *url.URL {
	path := strings.Join(namespace, namespaceSeparator)
	return resourceSegmentsURL(base, "polaris", "v1", catalogName, "namespaces", path, "generic-tables")
}

func genericTableResourceURL(base *url.URL, namespaceSeparator, catalogName string, namespace []string, tableName string) *url.URL {
	collection := genericTablesResourceURL(base, namespaceSeparator, catalogName, namespace)
	return resourceSegmentsURL(collection, tableName)
}

func isGenericTableUnsupported(err error) bool {
	var httpErr *HTTPError
	return errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotImplemented
}
