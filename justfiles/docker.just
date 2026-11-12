# Keep commands alphabetized.
container-tool := env_var_or_default("CONTAINER_TOOL", "docker")
platforms := env_var_or_default("PLATFORMS", "linux/amd64,linux/arm64")

# Build the manager container image.
docker-build:
    {{container-tool}} build -t {{img}} .

# Build and push a multi-platform manager image.
docker-buildx:
    @set -euo pipefail; \
    {{container-tool}} buildx create --name polaris-kubernetes-operator-builder || true; \
    {{container-tool}} buildx use polaris-kubernetes-operator-builder; \
    {{container-tool}} buildx build --platform={{platforms}} --output "type=image,name={{img}},push=true,oci-mediatypes=true" .; \
    {{container-tool}} buildx rm polaris-kubernetes-operator-builder || true

# Push the manager container image.
docker-push:
    {{container-tool}} push {{img}}
