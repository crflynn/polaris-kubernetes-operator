# Keep commands alphabetized.
kind := env_var_or_default("KIND", "kind")
kind-cluster := env_var_or_default("KIND_CLUSTER", "polaris-kubernetes-operator-test-e2e")

# Delete the Kind cluster used for end-to-end tests.
cleanup-test-e2e:
    "{{kind}}" delete cluster --name "{{kind-cluster}}" --quiet

# Create a Kind cluster for end-to-end tests when one does not exist.
setup-test-e2e:
    @command -v "{{kind}}" >/dev/null 2>&1 || { echo "Kind is not installed. Run mise install."; exit 1; }
    @clusters="$("{{kind}}" get clusters)"; \
    case "$clusters" in \
        *"{{kind-cluster}}"*) \
            echo "Kind cluster '{{kind-cluster}}' already exists. Skipping creation." ;; \
        *) \
            echo "Creating Kind cluster '{{kind-cluster}}'..."; \
            "{{kind}}" create cluster --name "{{kind-cluster}}" --wait 60s ;; \
    esac
    @"{{kubectl}}" cluster-info --context "kind-{{kind-cluster}}" >/dev/null

# Run the Kind-backed end-to-end tests and remove the temporary cluster afterward.
test-e2e: setup-test-e2e manifests generate fmt vet
    trap '"{{kind}}" delete cluster --name "{{kind-cluster}}" --quiet || true' EXIT; \
    KIND="{{kind}}" KIND_CLUSTER="{{kind-cluster}}" CERT_MANAGER_INSTALL_SKIP=true go test -tags=e2e ./test/e2e/ -v -ginkgo.v

# Validate checked-in examples against the installed CRD schemas without persisting resources.
validate-examples: install
    "{{kubectl}}" wait --for=condition=Established --timeout=2m crd --all
    "{{kubectl}}" apply --dry-run=server --validate=strict --recursive -f examples/
