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
  --set image.tag=0.1.0
```

For the published OCI chart, install the release directly from GHCR:

```sh
helm install polaris-operator "oci://ghcr.io/${OWNER}/charts/polaris-kubernetes-operator" \
  --version 0.1.0 --namespace polaris-system --create-namespace \
  --set image.repository="ghcr.io/${OWNER}/polaris-kubernetes-operator" \
  --set image.tag=0.1.0
```

The tagged release workflow publishes the container and chart under the owner
of the GitHub repository that triggered the release. Set `OWNER` to that
GitHub account when installing. The image is
`ghcr.io/${OWNER}/polaris-kubernetes-operator:X.Y.Z`; the chart is
`oci://ghcr.io/${OWNER}/charts/polaris-kubernetes-operator` with version
`X.Y.Z`. They use separate GHCR repositories to avoid tag collisions.

For a packaged chart, replace the source path with the chart archive, such as `./polaris-kubernetes-operator-0.1.0.tgz`.

## Configure

The chart defaults to one replica, a dedicated service account, cluster-scoped permissions for the operator's watched custom resources and Secrets, and an HTTPS metrics service on port 8443. Set `image.repository` and `image.tag` to the published operator image. `image.digest` can pin an immutable image digest instead of a tag.

See `values.yaml` for supported deployment settings. Set `metrics.enabled=false` to disable the metrics endpoint, service, and metrics authorization RBAC. Set `rbac.create=false` only when equivalent permissions have been provisioned separately. If `serviceAccount.create=false`, set `serviceAccount.name` to an existing account.

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
