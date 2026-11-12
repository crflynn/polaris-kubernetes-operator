# Keep commands alphabetized.
helm := env_var_or_default("HELM", "helm")

# Lint the operator Helm chart.
helm-lint:
    "{{helm}}" lint charts/polaris-kubernetes-operator

# Package the operator Helm chart for distribution.
helm-package: helm-lint
    mkdir -p dist/charts
    "{{helm}}" package charts/polaris-kubernetes-operator --destination dist/charts

# Render the operator Helm chart.
helm-template:
    "{{helm}}" template polaris-operator charts/polaris-kubernetes-operator --namespace polaris-system

# Copy generated CRDs into the Helm chart's CRD directory.
sync-chart-crds: manifests
    cp config/crd/bases/*.yaml charts/polaris-kubernetes-operator/crds/
