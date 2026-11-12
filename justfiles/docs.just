# Keep commands alphabetized.

# Serve Go package documentation.
docs-pkgsite:
    @{{go-tool}} pkgsite -http=localhost:8080 .
