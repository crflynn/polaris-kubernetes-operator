# Polaris Kubernetes Operator Helm chart

This chart installs the Polaris Kubernetes Operator and its CustomResourceDefinitions. It does not install an Apache Polaris server. The operator requires a reachable Polaris endpoint, configured with `PolarisConnection` resources.

## Install

Set `OWNER` in your shell to the GitHub account that owns the published OCI
packages before running these commands.

For a source checkout:

```sh
helm install polaris-operator ./charts/polaris-kubernetes-operator \
  --namespace polaris-system --create-namespace \
  --set image.repository=ghcr.io/${OWNER}/polaris-kubernetes-operator \
  --set image.tag=0.1.0 \
  --set-string 'allowedHosts[0]=polaris.example.com'
```

For the published OCI chart, install the release directly from GHCR:

```sh
helm install polaris-operator "oci://ghcr.io/${OWNER}/charts/polaris-kubernetes-operator" \
  --version 0.1.0 --namespace polaris-system --create-namespace \
  --set image.repository="ghcr.io/${OWNER}/polaris-kubernetes-operator" \
  --set image.tag=0.1.0 \
  --set-string 'allowedHosts[0]=polaris.example.com'
```

The tagged release workflow publishes the container and chart under the owner
of the GitHub repository that triggered the release. Set `OWNER` to that
GitHub account when installing. The image is
`ghcr.io/${OWNER}/polaris-kubernetes-operator:X.Y.Z`; the chart is
`oci://ghcr.io/${OWNER}/charts/polaris-kubernetes-operator` with version
`X.Y.Z`. They use separate GHCR repositories to avoid tag collisions.

For a packaged chart, replace the source path with the chart archive, such as `./polaris-kubernetes-operator-0.1.0.tgz`.

## Configure

The chart defaults to cluster-wide watches to preserve existing behavior. `namespaceScope: true` adds the release namespace to the watch scope. `watchNamespaces` adds explicit namespace names, and `watchNamespaceSelector` selects namespaces by Kubernetes label selector. These settings can be combined. Namespace-selector matches are resolved at startup; restart the operator after namespace labels change.

When any namespace scope is configured, the chart creates RoleBindings for the release namespace (when `namespaceScope` is enabled) and every name in `watchNamespaces`. For selector matches, set `rbac.scopeNamespaces` to the matching namespace names so the chart can create their RoleBindings; Helm rendering fails if `rbac.create=true`, a selector is set, and this list is empty. Keep that list synchronized with the selector. The selector also requires cluster-scoped `get`/`list` access to Namespace objects; the chart grants only those permissions. If `rbac.create=false`, provision equivalent bindings and Namespace permissions yourself.

`allowedHosts` is an exact allowlist of hostnames or `hostname:port` authorities that can receive Polaris credentials. It is empty by default, which denies all Polaris connections. HTTPS is required; `allowInsecureHTTP` opts into HTTP and should be limited to isolated development environments. A hostname without a port matches HTTPS on port 443; specify the port for non-default endpoints.

Example:

```yaml
namespaceScope: true
watchNamespaces:
  - analytics
  - platform
allowedHosts:
  - polaris.example.com
  - auth.example.com
```

Set `image.repository` and `image.tag` to the published operator image. `image.digest` can pin an immutable image digest instead of a tag.

## CRD lifecycle

Helm installs CRDs from the chart's `crds/` directory before rendering the remaining resources. Helm does not upgrade or delete these CRDs. When upgrading the chart, apply updated CRDs separately before upgrading the operator; uninstalling the chart leaves CRDs and custom resources intact.

The files in `crds/` are generated from `config/crd/bases/`. Run `just sync-chart-crds` whenever API schemas change.

## Validate and publish

Run the local chart checks and package the chart with:

```sh
just helm-lint
just helm-template
just helm-package
```

Pushing a `vX.Y.Z` Git tag runs the release workflow after tests, lint, E2E, generated-file, and chart validation pass. The workflow publishes the multi-platform image as `ghcr.io/<repository-owner>/polaris-kubernetes-operator:X.Y.Z` and the chart to `oci://ghcr.io/<repository-owner>/charts/polaris-kubernetes-operator` with version `X.Y.Z`. The owner is derived from the GitHub repository, so forks publish under their own owner. These use separate GHCR repositories because a chart and container image cannot use the same tag in the same repository. Configure the GitHub `release` environment with required reviewers and protect `v*` tags in repository rulesets; the workflow needs permission to publish packages under the repository owner.
