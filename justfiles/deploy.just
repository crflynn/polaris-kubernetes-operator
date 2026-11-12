# Keep commands alphabetized.
kubectl := env_var_or_default("KUBECTL", "kubectl")
ignore-not-found := env_var_or_default("IGNORE_NOT_FOUND", "false")


# Deploy the controller to the current kubectl context.
deploy: manifests
    cd config/manager && "{{kustomize}}" edit set image controller={{img}}
    "{{kustomize}}" build config/default | "{{kubectl}}" apply -f -


# Install CRDs into the current kubectl context.
install: manifests
    @out="$("{{kustomize}}" build config/crd 2>/dev/null || true)"; \
    if [ -n "$out" ]; then echo "$out" | "{{kubectl}}" apply -f -; else echo "No CRDs to install; skipping."; fi



# Remove the controller deployment from the current kubectl context.
undeploy:
    "{{kustomize}}" build config/default | "{{kubectl}}" delete --ignore-not-found={{ignore-not-found}} -f -

# Remove CRDs from the current kubectl context.
uninstall: manifests
    @out="$("{{kustomize}}" build config/crd 2>/dev/null || true)"; \
    if [ -n "$out" ]; then echo "$out" | "{{kubectl}}" delete --ignore-not-found={{ignore-not-found}} -f -; else echo "No CRDs to delete; skipping."; fi
