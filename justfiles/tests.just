# Keep commands alphabetized.
minimum-coverage := "30"


# Enforce the repository-wide statement coverage minimum.
coverage-check:
    go run ./hack/coverage -profile cover.out -minimum {{minimum-coverage}}

# Generate an HTML coverage report for manual opening.
coverage-report:
    go tool cover -html=cover.out -o coverage.html

# Download envtest Kubernetes assets into the project-local bin directory.
setup-envtest-assets:
    @echo "Setting up envtest binaries for Kubernetes version {{envtest-k8s-version}}..."
    @{{go-tool}} setup-envtest use {{envtest-k8s-version}} --bin-dir "{{localbin}}" -p path

# Run unit and envtest-backed controller tests.
test: manifests generate fmt vet setup-envtest-assets
    KUBEBUILDER_ASSETS="$({{go-tool}} setup-envtest use {{envtest-k8s-version}} --bin-dir "{{localbin}}" -p path)" go test ./... -coverprofile cover.out
    go run ./hack/coverage -profile cover.out -minimum {{minimum-coverage}}
