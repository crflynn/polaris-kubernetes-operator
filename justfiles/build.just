# Keep commands alphabetized.
img := env_var_or_default("IMG", "controller:latest")

# Build the manager binary.
build: manifests generate fmt vet
    go build -o bin/manager cmd/main.go

# Build a consolidated install manifest.
build-installer: manifests generate
    mkdir -p dist
    cd config/manager && "{{kustomize}}" edit set image controller={{img}}
    "{{kustomize}}" build config/default > dist/install.yaml


# Run the controller from the local checkout.
run: manifests generate fmt vet
    go run ./cmd/main.go
