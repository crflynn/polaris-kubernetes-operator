# Keep imports and commands alphabetized.
set shell := ["bash", "-o", "pipefail", "-c"]

import "justfiles/build.just"
import "justfiles/deploy.just"
import "justfiles/docs.just"
import "justfiles/docker.just"
import "justfiles/fmt.just"
import "justfiles/helm.just"
import "justfiles/kind.just"
import "justfiles/tests.just"
import "justfiles/tools.just"

default: help

# Build the manager binary.
all: build

# List available project commands.
help:
    @just --list --unsorted
