# Apache Polaris Kubernetes Operator

This project is a Kubebuilder-based Go operator for managing Apache Polaris through its Management Service and Catalog Service APIs.

The operator manages `PolarisConnection`, `PolarisCatalog`, namespaces, principals, roles, role bindings, catalog-role grants, and policies through Polaris' authenticated APIs.

The operator does not write directly to the Polaris metastore.

## Project status

Connection, catalog, governance, policy, and namespace reconciliation are implemented. Table, view, and generic-table ownership remains future work.

## Prerequisites

- Go 1.26.5, Just 1.42.4, Helm 3.17.3, Kind 0.33.0, kubectl 1.37.0, Kustomize 5.8.1, and K9s 0.51.0, all pinned in `mise.toml`.
- Docker Engine or another Docker-compatible container runtime for building images and running the Kind-backed suite.
- A Polaris endpoint is needed only for live acceptance; the Kind E2E suite uses an in-cluster mock Polaris API.

Install the pinned tools with:

```sh
mise install
```

`controller-gen` and `setup-envtest` are Go tools pinned in `tools/go.mod` and invoked through the project recipes.

## Development

Run generated code and CRD manifest generation:
Run formatting, static checks, and tests:
Build the operator:
The `just test` recipe downloads the envtest binaries required by controller-runtime.
`just test` enforces a 30% repository-wide statement coverage minimum; `just coverage-check` checks the existing `cover.out` profile, and `just coverage-report` writes `coverage.html` for manual opening.

Run the Kind-backed end-to-end suite:
The e2e recipe creates the isolated `polaris-kubernetes-operator-test-e2e` Kind cluster, builds and loads the manager image, smoke-installs the Helm chart, validates examples against the installed CRD schemas using server-side dry run, checks manager metrics, and reconciles connection and catalog resources against an in-cluster mock Polaris API. It deletes the cluster on exit.

## Releasing

Pull-request CI checks every non-merge commit header against `<type>[optional scope][!]: <description>`.
Use Conventional Commits on the default branch: `feat:` creates a minor release, `fix:` creates a patch release, and a `!` breaking-change marker creates a major release.
Release Please opens or updates a release PR with the version and changelog changes.
Review and merge that PR to create the `vX.Y.Z` tag and GitHub Release; the tag triggers validation and publication of the container image and Helm chart.

The workflow runs automatically on default-branch pushes. To run it manually
with GitHub CLI:

```sh
gh workflow run release-please.yml \
  --ref "$(gh repo view --json defaultBranchRef --jq '.defaultBranchRef.name')"
```

Then review the Release Please PR and merge it, for example with
`gh pr merge <number> --squash`. No `just` release recipe is needed; Release
Please runs in GitHub Actions, and GitHub CLI only dispatches the workflow or
merges its PR.

Set the repository Actions secret `RELEASE_PLEASE_TOKEN` to a token with
`contents: write`, `issues: write`, and `pull requests: write`. Release Please
uses it to create the PR, tag, and GitHub Release; using a separate token also
allows the tag push to start the existing artifact publishing workflow.

## Acceptance on a real cluster

Confirm `kubectl config current-context` points to the intended cluster before installation. Install the chart using the instructions below, create the referenced Polaris Secret and a configured `PolarisConnection`, then inspect its status with `kubectl get polarisconnections -A` or run `mise exec -C . -- k9s` to inspect the active cluster. The k9s context comes from the current kubeconfig.

## Install the operator with Helm

The chart installs the operator and its CRDs; it does not install the Polaris server. Point the operator at an existing Polaris endpoint by creating a `PolarisConnection`.

Set `OWNER` to the GitHub account that owns the published OCI packages before running the command.

```sh
helm install polaris-operator ./charts/polaris-kubernetes-operator \
  --namespace polaris-system --create-namespace \
  --set image.repository=ghcr.io/${OWNER}/polaris-kubernetes-operator \
  --set image.tag=0.1.0
```

The chart and CRD lifecycle, configurable values, and packaging instructions are documented in [`charts/polaris-kubernetes-operator/README.md`](charts/polaris-kubernetes-operator/README.md).

## Resource examples

Practical connection, catalog, governance, and policy manifests are in [`examples/`](examples/README.md). The examples use Secret references rather than embedding credentials and document their prerequisites and apply order.

## PolarisConnection

`PolarisConnection` is namespaced and references credentials in Secrets in the same namespace.

Client-credential authentication requires:

- `spec.managementURL`.
- `spec.tokenURL`.
- `spec.auth.clientCredentials.secretRef`.
- Secret keys named `client-id` and `client-secret`, unless overridden.

Bearer-token authentication can be used with `spec.auth.bearerTokenSecretRef`.

The optional `spec.catalogURL` enables a second readiness check against `/v1/config`.

`spec.realm` selects the request realm; `spec.realmHeader` controls the client header name and defaults to `Polaris-Realm`. The controller sends it on OAuth token and authenticated API requests. Polaris 1.8's official Helm chart configures the valid realm list and whether the header is required, but does not document a setting to rename it; a custom name requires a compatible server resolver or proxy.

The controller reads the server's `namespace-separator` from `/v1/config` and reports the decoded value in `status.namespaceSeparator`.

Polaris generic tables are beta and are not advertised in `/v1/config`'s endpoint list. They are disabled by default; set `spec.enableBetaGenericTables: true` only after confirming that the target Polaris server exposes this API.

Custom CA bundles are supported through `spec.tls.caSecretRef`.

The controller never writes credentials or access tokens into status, logs, events, or Polaris properties.

Example:

```yaml
apiVersion: polaris.apache.org/v1alpha1
kind: PolarisConnection
metadata:
  name: polaris
  namespace: polaris-system
spec:
  managementURL: https://polaris.example.com/api/management/v1
  catalogURL: https://polaris.example.com/api/catalog
  tokenURL: https://polaris.example.com/api/catalog/v1/oauth/tokens
  realm: POLARIS
  auth:
    clientCredentials:
      secretRef:
        name: polaris-admin
```

Create the referenced Secret separately:

## PolarisCatalog

`PolarisCatalog` is namespaced and references a `PolarisConnection` in the same namespace.
It supports internal catalogs and experimental external Iceberg REST catalogs.

For external catalogs, set `spec.externalCatalog.authentication.type` to `OAUTH` or `BEARER`.
OAuth credentials use Secret keys `client-id` and `client-secret` by default; bearer authentication references a Secret key directly.
Secrets must be in the same namespace as the `PolarisCatalog`.
Secret values are sent to Polaris only during create and are never written to Kubernetes status, logs, or catalog properties.

Polaris does not support updating external catalog connection authentication through `UpdateCatalog`.
Changing the referenced Secret revision or remote connection configuration sets `Ready=False`; delete and recreate the catalog to apply the change.

`spec.properties` must include `default-base-location`.
Do not put credentials or tokens in catalog properties; use Polaris-managed storage configuration.

Deleting a `PolarisCatalog` deletes the remote catalog.
Polaris only allows deletion when the catalog is empty; a failed remote delete keeps the Kubernetes finalizer and reports `Ready=False`.

Samples: `config/samples/polaris_v1alpha1_polariscatalog.yaml` and `config/samples/polaris_v1alpha1_polariscatalog_external.yaml`.

## PolarisNamespace

`PolarisNamespace` models an ordered namespace path under a `PolarisCatalog`, such as `[analytics, curated]`. It is namespaced and the catalog reference must resolve in the same Kubernetes namespace.

`ownershipMode` defaults to `Observe`: the controller requires the namespace to exist, reports property drift, and never mutates or deletes it. `Adopt` also requires it to exist; the first reconcile records its identity without mutation, then the controller may reconcile the explicitly managed property keys. `Enforce` creates a missing namespace and reconciles those properties.

Namespace property updates are key-scoped. Keys in `spec.properties` are managed; keys previously managed by the resource are removed when omitted later. Other Polaris-managed or externally set keys are preserved.

Remote deletion is orphaned by default. Set `ownershipMode: Enforce` and `deletionPolicy: Delete` to request deletion; Polaris rejects deletion of a non-empty namespace, and the finalizer remains until deletion succeeds. This does not cascade to child namespaces or catalog content.

The connection controller reads the configured `namespace-separator` from Polaris `/v1/config` and records the decoded value in `PolarisConnection.status.namespaceSeparator`; the namespace client uses it to encode nested namespace paths.

Sample: `config/samples/polaris_v1alpha1_polarisnamespace.yaml`.

## PolarisTable

`PolarisTable` observes or registers an Iceberg table at an existing namespace path. It does not create object-store files, write Iceberg metadata, or reconcile table commits; those remain the responsibility of the table engine.

`ownershipMode` defaults to `Observe`. `Observe` and `Adopt` require the table to exist and never mutate it. `Enforce` requires `metadataLocation` and registers that existing metadata file only when the table is absent. It never enables Polaris' registration `overwrite` option. A later metadata-location difference is reported as drift rather than committed over an engine change.

Deletion is orphaned by default. With `ownershipMode: Enforce`, `deletionPolicy: Unregister` detaches the registration while preserving metadata and data files; `deletionPolicy: Delete` drops the Polaris table registration and does not guarantee file cleanup. Both actions use a finalizer.

Sample: `config/samples/polaris_v1alpha1_polaristable.yaml`. The referenced metadata file must already exist and be readable by Polaris.

## PolarisView

`PolarisView` follows the same observe/adopt/register contract as `PolarisTable`: `Enforce` registers a supplied existing `metadataLocation` only when absent, and no mode rewrites view metadata or reconciles view-engine commits. Metadata-location drift is reported without mutation.

View deletion is orphaned by default. `deletionPolicy: Delete` with `ownershipMode: Enforce` explicitly drops the Polaris view registration through a finalizer. The reviewed Polaris API has no view-unregister operation.

Sample: `config/samples/polaris_v1alpha1_polarisview.yaml`.

## PolarisGenericTable

Polaris 1.8 marks Generic Tables beta. `PolarisConnection.spec.enableBetaGenericTables` is an explicit opt-in because the API is not advertised in `/v1/config`; the operator does not infer server support from an endpoint probe.

`PolarisGenericTable` observes or creates a Polaris-native generic table. `Observe` and `Adopt` require an existing object and never mutate it. `Enforce` creates an absent object, but cannot update an existing one: Polaris exposes no update operation, so field differences are reported as drift rather than repaired by drop-and-create.

Deletion is orphaned by default. `deletionPolicy: Delete` with `ownershipMode: Enforce` explicitly removes the Polaris catalog entry through a finalizer; Polaris does not guarantee that this deletes backing files.

Sample: `config/samples/polaris_v1alpha1_polarisgenerictable.yaml`.



## Principals

`PolarisPrincipal` creates and manages a Polaris principal through the management API.
It writes the one-time client ID and secret returned by Polaris to a controller-owned,
same-namespace Kubernetes Secret using the `client-id` and `client-secret` keys.
Credential values are not stored in the custom resource status.

```yaml
apiVersion: polaris.apache.org/v1alpha1
kind: PolarisPrincipal
metadata:
  name: analytics-reader
  namespace: default
spec:
  connectionRef: polaris
  principalName: analytics-reader
  properties:
    description: Read-only analytics identity
  credentialsSecretName: analytics-reader-credentials
  credentialsSecretTemplate:
    metadata:
      annotations:
        reflector.v1.k8s.emberstack.com/reflection-allowed: "true"
        reflector.v1.k8s.emberstack.com/reflection-allowed-namespaces: analytics
```

Routine reconciliation never rotates credentials. To rotate them explicitly, set
`spec.credentialRotationRequestID` to a positive integer greater than the last
attempted request ID, for example `1`, then increase it for every subsequent
rotation. The ID is recorded before the non-idempotent Polaris rotation request;
if the request or Secret write fails, use a new higher ID to retry. Deleting the
credentials Secret does not trigger rotation; restore it from a secure backup or
request an explicit rotation.

`spec.credentialsSecretTemplate.metadata` optionally supplies labels and annotations for that Secret. The controller reconciles keys specified by the template, removes previously managed keys when they are omitted, and preserves other Secret metadata. The Secret name, namespace, data, and owner reference remain controller-managed.

For EmberStack Reflector, set `reflection-allowed` and an explicit `reflection-allowed-namespaces` annotation in the template. The source Secret is copied only if Reflector automatic mirroring is configured; otherwise, create mirror Secrets with Reflector's `reflects` annotation. Treat reflection permissions as permission to distribute the credentials to those namespaces. An empty allowed-namespace value permits all namespaces.

Deleting a `PolarisPrincipal` deletes the remote Polaris principal. Treat this
resource as destructive and protect it with the same controls used for other
identity resources.

## Principal roles

`PolarisPrincipalRole` manages a Polaris role definition and its non-secret
properties. `PolarisPrincipalRoleBinding` assigns a principal to that role.
It does not manage role definitions.

The optional `spec.federated` field is set at creation. Polaris does not support
changing it later, so a mismatch against an existing role is reported as
`Ready=False` rather than silently changing its identity configuration.
Deleting this resource deletes the remote role. Remove principal assignments
first if Polaris rejects deleting an assigned role.

Sample: `config/samples/polaris_v1alpha1_polarisprincipalrole.yaml`.

## Catalog roles

`PolarisCatalogRole` manages role properties within a referenced `PolarisCatalog`.
The controller records the remote catalog, connection, and role names in status
before creating the role so later identity changes cannot silently orphan it.
Deleting a catalog role deletes its associated grants in Polaris; remove role assignments first if Polaris rejects deleting an assigned role.

The role must be deleted before its catalog. If the referenced catalog changes
its Polaris name or connection, reconciliation reports `Ready=False`; restore
the recorded identity to continue managing or deleting the role.

Sample: `config/samples/polaris_v1alpha1_polariscatalogrole.yaml`.

## Principal role bindings

`PolarisPrincipalRoleBinding` manages one principal-to-principal-role assignment.
Both references must resolve in the same namespace and use the same
`PolarisConnection`. The controller checks Polaris membership before assigning,
so routine resync does not issue duplicate PUTs. Deleting the binding revokes
the remote assignment; the principal and role resources are not deleted.

The binding records the remote principal, role, and connection identities in
status before changing membership. Restore those referenced identities if they
change after the assignment is recorded.

Sample: `config/samples/polaris_v1alpha1_polarisprincipalrolebinding.yaml`.

## Catalog role bindings

`PolarisCatalogRoleBinding` assigns one catalog role to one principal role.
Both role resources must be Ready and use the same `PolarisConnection`.
Reconciliation checks the Polaris assignment set before issuing a PUT; deleting
the binding revokes only that assignment.

The binding records the referenced role names, catalog name, and connection in
status before changing Polaris. Restore those identities if a referenced role
changes. Delete bindings before deleting the role resources.

Sample: `config/samples/polaris_v1alpha1_polariscatalogrolebinding.yaml`.

## Catalog role grants

`PolarisCatalogRoleGrant` manages one privilege on a catalog, namespace, table,
view, or policy. Its referenced `PolarisCatalogRole` must be Ready. Privilege
names are passed through to Polaris, which validates support for the target
resource and server version. Semantic-model grants remain unavailable until
their beta API can be version-gated.

Namespace targets use an ordered `namespace` array, for example
`[database, schema]`; table, view, and policy grants also require their matching
name field. A grant's identity is immutable; delete and recreate the resource
to target a different securable or privilege.

Deleting a grant revokes only that grant by default. Set `spec.cascade: true`
only when Polaris should also revoke grants on subresources. This is an explicit
destructive choice and applies when this resource is deleted.

Sample: `config/samples/polaris_v1alpha1_polariscatalogrolegrant.yaml`.

## Policies

`PolarisPolicy` manages a policy stored under an existing Polaris catalog namespace.
Its referenced `PolarisCatalog` must be Ready, and its `PolarisConnection` must
configure `catalogURL` with a Polaris server that supports the policy API.
Polaris validates `spec.content` according to the selected `spec.type`.
The type and remote identity are immutable; description and content updates use
Polaris' `current-policy-version` optimistic-concurrency contract. The current
remote version is reported in `status.policyVersion`.

`PolarisPolicyBinding` manages one policy attachment. `targetType` is `catalog`,
`namespace`, or `table-like`; table and view attachments both use `table-like`
with the target name appended to the ordered `namespace` path. Updating
`parameters` replaces the mapping parameters. Binding identity is immutable,
and deleting a binding detaches only that mapping.

Delete all `PolarisPolicyBinding` resources before deleting their policy.
`spec.detachAllOnDelete: true` explicitly asks Polaris to remove any remaining
remote mappings, including mappings not represented by Kubernetes resources.

Samples: `config/samples/polaris_v1alpha1_polarispolicy.yaml` and
`config/samples/polaris_v1alpha1_polarispolicybinding.yaml`.


## Future scope

Planned resource families are documented in the adjacent workspace plan at `../agents/outputs/polaris/plan.md`.

`PolarisNamespace`, `PolarisTable`, `PolarisView`, and the beta-gated `PolarisGenericTable` now cover namespace and controlled data-plane registration workflows.

Remaining planned work includes semantic models, shares, and evaluating an optional server controller; see the adjacent workspace plan.

## License

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

