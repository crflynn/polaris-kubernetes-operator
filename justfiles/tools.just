# Keep imports and commands alphabetized.
localbin := justfile_directory() + "/bin"
go-tool := "go tool -modfile=tools/go.mod"
kustomize := "kustomize"
golangci-lint := localbin + "/golangci-lint"
envtest-k8s-version := "1.37.0"

# Build the configured golangci-lint binary from the Go-managed tool.
golangci-lint-custom:
    @mkdir -p "{{localbin}}"
    @{{go-tool}} golangci-lint custom --destination "{{localbin}}" --name golangci-lint

