# Keep commands alphabetized.
# Format Go sources.
fmt:
    go fmt ./...

# Generate Go deepcopy implementations.
generate:
    @{{go-tool}} controller-gen object:headerFile="hack/boilerplate.go.txt",year={{`date +%Y`}} paths="./..."

# Run golangci-lint without changing files.
lint: golangci-lint-custom
    @"{{golangci-lint}}" run

# Validate the golangci-lint configuration.
lint-config: golangci-lint-custom
    @"{{golangci-lint}}" config verify

# Run golangci-lint and apply supported fixes.
lint-fix: golangci-lint-custom
    @"{{golangci-lint}}" run --fix

# Generate Kubernetes manifests from controller markers.
manifests:
    @{{go-tool}} controller-gen rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases


# Run the Go vet analyzer.
vet:
    go vet ./...
