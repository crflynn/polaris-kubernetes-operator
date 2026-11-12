# Polaris Kubernetes Operator examples

These manifests show common ways to configure the operator's custom resources. They use the `polaris-system` namespace; change it consistently if the operator and referenced Secrets are installed elsewhere.

## Examples

- `connections/client-credentials.yaml`: Polaris management and catalog APIs using OAuth client credentials.
- `connections/bearer-token-custom-ca.yaml`: bearer-token authentication and a private CA bundle. Use this connection name in dependent resources if choosing this variant.
- `catalogs/internal-s3.yaml`: an internal Polaris catalog with an S3 base location and storage configuration.
- `catalogs/external-oauth.yaml`: an external Iceberg REST catalog using OAuth client credentials.
- `catalogs/external-bearer.yaml`: an external Iceberg REST catalog using a bearer token.
- `data-plane/namespace.yaml`: creates the `finance` namespace and manages its properties.
- `data-plane/nested-namespace.yaml`: creates the nested `[finance, reporting]` namespace path.
- `data-plane/table.yaml`: registers an existing Iceberg table with a hyphenated name from its metadata location.
- `data-plane/view.yaml`: registers an existing Iceberg view from its metadata location.
- `data-plane/generic-table.yaml`: creates a non-Iceberg Polaris Generic Table; this API is beta-gated.
- `governance/analytics-reader.yaml`: principal with a templated credential Secret, principal role, catalog role, bindings, and a catalog-role grant.
- `policies/finance-retention.yaml`: a policy and namespace binding for an existing Polaris `finance` namespace.

## Before applying

Install the operator and CRDs first. Replace the example endpoints, realm, storage names, bucket locations, and catalog details with values accepted by your Polaris deployment. Keep credentials out of these manifests and out of catalog properties; the `Secret` references below are intentionally not accompanied by example Secret objects.

The table and view examples require their Iceberg metadata files to exist at the configured locations before applying the resources.
PolarisTable and PolarisView register existing Iceberg metadata files; they do not create or commit Iceberg data.
GenericTable is for non-Iceberg Polaris entries; Enforce creates a missing entry and reports drift instead of rewriting an existing one.
Polaris does not create missing namespace parents recursively; apply each parent namespace before its child.
Generic Tables require a Polaris server version that supports the beta API and `spec.enableBetaGenericTables: true` on the `PolarisConnection`.

Create the client-credentials Secret from protected local files or your cluster's secret-management workflow:

```sh
kubectl create secret generic polaris-admin -n polaris-system \
  --from-file=client-id=/secure/path/client-id \
  --from-file=client-secret=/secure/path/client-secret
```

For the bearer-token/private-CA connection, create `polaris-access-token` with key `token` and `polaris-private-ca` with key `ca.crt`. External-catalog OAuth requires a Secret named `external-warehouse-oauth` with `client-id` and `client-secret` keys; the bearer variant requires `external-warehouse-token` with key `token`. Use your secret manager to provision them; do not commit their contents.

## Apply in dependency order

1. Apply the client-credentials connection and wait for it to become ready:

   ```sh
   kubectl apply -f examples/connections/client-credentials.yaml
   kubectl wait --for=condition=Ready polarisconnection/polaris \
     -n polaris-system --timeout=5m
   ```

2. Create the internal catalog and wait until it is ready:

   ```sh
   kubectl apply -f examples/catalogs/internal-s3.yaml
   kubectl wait --for=condition=Ready polariscatalog/warehouse \
     -n polaris-system --timeout=5m
   ```

3. Create the finance namespace and wait until it is ready:

   ```sh
   kubectl apply -f examples/data-plane/namespace.yaml
   kubectl wait --for=condition=Ready polarisnamespace/finance \
     -n polaris-system --timeout=5m
   ```

4. Create the nested `[finance, reporting]` namespace and wait until it is ready:

   ```sh
   kubectl apply -f examples/data-plane/nested-namespace.yaml
   kubectl wait --for=condition=Ready polarisnamespace/finance-reporting \
     -n polaris-system --timeout=5m
   ```

5. Register the table and view after replacing their metadata locations with existing Iceberg metadata files:

   ```sh
   kubectl apply -f examples/data-plane/table.yaml
   kubectl apply -f examples/data-plane/view.yaml
   ```

6. To use the Generic Table example, confirm beta API support and enable the per-connection gate:

   ```sh
   kubectl patch polarisconnection polaris -n polaris-system --type=merge \
     -p '{"spec":{"enableBetaGenericTables":true}}'
   kubectl apply -f examples/data-plane/generic-table.yaml
   ```

7. Apply `examples/governance/analytics-reader.yaml`. The operator creates the principal's `analytics-reader-credentials` Secret; securely retrieve and handle those generated credentials according to your cluster's access policy.
8. To demonstrate an external catalog, provision its corresponding Secret and apply either `examples/catalogs/external-oauth.yaml` or `examples/catalogs/external-bearer.yaml`.
9. Apply `examples/policies/finance-retention.yaml` after the finance namespace exists in Polaris.

Resource deletion can delete or change corresponding remote Polaris objects. Review each resource's lifecycle documentation before applying these examples to a production endpoint.
