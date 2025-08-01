#!/usr/bin/env bash

#  Copyright 2020 The Kubernetes Authors.
#
#  Licensed under the Apache License, Version 2.0 (the "License");
#  you may not use this file except in compliance with the License.
#  You may obtain a copy of the License at
#
#      http://www.apache.org/licenses/LICENSE-2.0
#
#  Unless required by applicable law or agreed to in writing, software
#  distributed under the License is distributed on an "AS IS" BASIS,
#  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
#  See the License for the specific language governing permissions and
#  limitations under the License.

# If you update this file, please follow
# https://suva.sh/posts/well-documented-makefiles

## --------------------------------------
## General
## --------------------------------------

SHELL:=/usr/bin/env bash
.DEFAULT_GOAL:=help

#
# Go.
#
GO_VERSION ?= 1.24.2

# Use GOPROXY environment variable if set
GOPROXY := $(shell go env GOPROXY)
ifeq ($(GOPROXY),)
GOPROXY := https://proxy.golang.org
endif
export GOPROXY

# Active module mode, as we use go modules to manage dependencies
export GO111MODULE=on

# Hosts running SELinux need :z added to volume mounts
SELINUX_ENABLED := $(shell cat /sys/fs/selinux/enforce 2> /dev/null || echo 0)

ifeq ($(SELINUX_ENABLED),1)
  DOCKER_VOL_OPTS?=:z
endif

# Tools.
TOOLS_DIR := hack/tools
TOOLS_BIN_DIR := $(abspath $(TOOLS_DIR)/bin)
GOLANGCI_LINT := $(abspath $(TOOLS_BIN_DIR)/golangci-lint)
GO_APIDIFF := $(TOOLS_BIN_DIR)/go-apidiff
CONTROLLER_GEN := $(TOOLS_BIN_DIR)/controller-gen
EXAMPLES_KIND_DIR := $(abspath examples/kind)
PROVIDERS_KIND_DIR := $(abspath providers/kind)
EXAMPLES_CLUSTER_API_DIR := $(abspath examples/cluster-api)
PROVIDERS_CLUSTER_API_DIR := $(abspath providers/cluster-api)
EXAMPLES_CLUSTER_INVENTORY_API_DIR := $(abspath examples/cluster-inventory-api)
PROVIDERS_CLUSTER_INVENTORY_API_DIR := $(abspath providers/cluster-inventory-api)
GO_INSTALL := ./hack/go-install.sh

# The help will print out all targets with their descriptions organized bellow their categories. The categories are represented by `##@` and the target descriptions by `##`.
# The awk commands is responsible to read the entire set of makefiles included in this invocation, looking for lines of the file as xyz: ## something, and then pretty-format the target and help. Then, if there's a line with ##@ something, that gets pretty-printed as a category.
# More info over the usage of ANSI control characters for terminal formatting: https://en.wikipedia.org/wiki/ANSI_escape_code#SGR_parameters
# More info over awk command: http://linuxcommand.org/lc3_adv_awk.php
.PHONY: help
help:  ## Display this help
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

## --------------------------------------
## Testing
## --------------------------------------

.PHONY: test
test: test-tools ## Run the script check-everything.sh which will check all.
	TRACE=1 ./hack/check-everything.sh

.PHONY: test-tools
test-tools:

## --------------------------------------
## Binaries
## --------------------------------------

GO_APIDIFF_VER := v0.8.2
GO_APIDIFF_BIN := go-apidiff
GO_APIDIFF := $(abspath $(TOOLS_BIN_DIR)/$(GO_APIDIFF_BIN)-$(GO_APIDIFF_VER))
GO_APIDIFF_PKG := github.com/joelanford/go-apidiff

$(GO_APIDIFF): # Build go-apidiff from tools folder.
	GOBIN=$(TOOLS_BIN_DIR) $(GO_INSTALL) $(GO_APIDIFF_PKG) $(GO_APIDIFF_BIN) $(GO_APIDIFF_VER)

CONTROLLER_GEN_VER := v0.17.1
CONTROLLER_GEN_BIN := controller-gen
CONTROLLER_GEN := $(abspath $(TOOLS_BIN_DIR)/$(CONTROLLER_GEN_BIN)-$(CONTROLLER_GEN_VER))
CONTROLLER_GEN_PKG := sigs.k8s.io/controller-tools/cmd/controller-gen

$(CONTROLLER_GEN): # Build controller-gen from tools folder.
	GOBIN=$(TOOLS_BIN_DIR) $(GO_INSTALL) $(CONTROLLER_GEN_PKG) $(CONTROLLER_GEN_BIN) $(CONTROLLER_GEN_VER)

GOLANGCI_LINT_BIN := golangci-lint
GOLANGCI_LINT_VER := $(shell cat .github/workflows/ci.yml | grep [[:space:]]version: | sed 's/.*version: //')
GOLANGCI_LINT := $(abspath $(TOOLS_BIN_DIR)/$(GOLANGCI_LINT_BIN)-$(GOLANGCI_LINT_VER))
GOLANGCI_LINT_PKG := github.com/golangci/golangci-lint/v2/cmd/golangci-lint

$(GOLANGCI_LINT): # Build golangci-lint from tools folder.
	GOBIN=$(TOOLS_BIN_DIR) $(GO_INSTALL) $(GOLANGCI_LINT_PKG) $(GOLANGCI_LINT_BIN) $(GOLANGCI_LINT_VER)

GO_MOD_CHECK_DIR := $(abspath ./hack/tools/cmd/gomodcheck)
GO_MOD_CHECK := $(abspath $(TOOLS_BIN_DIR)/gomodcheck)
GO_MOD_CHECK_IGNORE := $(abspath .gomodcheck.yaml)
.PHONY: $(GO_MOD_CHECK)
$(GO_MOD_CHECK): # Build gomodcheck
	go build -C $(GO_MOD_CHECK_DIR) -o $(GO_MOD_CHECK)

## --------------------------------------
## Linting
## --------------------------------------

.PHONY: lint
lint: $(GOLANGCI_LINT) ## Lint codebase
	$(GOLANGCI_LINT) run -v $(GOLANGCI_LINT_EXTRA_ARGS)
	cd examples/kind; $(GOLANGCI_LINT) run -v $(GOLANGCI_LINT_EXTRA_ARGS)
	cd proviers/kind; $(GOLANGCI_LINT) run -v $(GOLANGCI_LINT_EXTRA_ARGS)
	cd examples/cluster-api; $(GOLANGCI_LINT) run -v $(GOLANGCI_LINT_EXTRA_ARGS)
	cd proviers/cluster-api; $(GOLANGCI_LINT) run -v $(GOLANGCI_LINT_EXTRA_ARGS)

.PHONY: lint-fix
lint-fix: $(GOLANGCI_LINT) ## Lint the codebase and run auto-fixers if supported by the linter.
	GOLANGCI_LINT_EXTRA_ARGS=--fix $(MAKE) lint

## --------------------------------------
## Generate
## --------------------------------------

.PHONY: modules
modules: ## Runs go mod to ensure modules are up to date.
	go mod tidy
	cd $(TOOLS_DIR); go mod tidy
	cd $(EXAMPLES_KIND_DIR); go mod tidy
	cd $(PROVIDERS_KIND_DIR); go mod tidy
	cd $(EXAMPLES_CLUSTER_API_DIR); go mod tidy
	cd $(PROVIDERS_CLUSTER_API_DIR); go mod tidy
	cd $(EXAMPLES_CLUSTER_INVENTORY_API_DIR); go mod tidy
	cd $(PROVIDERS_CLUSTER_INVENTORY_API_DIR); go mod tidy

## --------------------------------------
## Cleanup / Verification
## --------------------------------------

.PHONY: clean
clean: ## Cleanup.
	$(GOLANGCI_LINT) cache clean
	$(MAKE) clean-bin

.PHONY: clean-bin
clean-bin: ## Remove all generated binaries.
	rm -rf hack/tools/bin

.PHONY: clean-release
clean-release: ## Remove the release folder
	rm -rf $(RELEASE_DIR)

.PHONY: verify-modules
verify-modules: modules $(GO_MOD_CHECK) ## Verify go modules are up to date
	@if !(git diff --quiet HEAD -- go.sum go.mod $(TOOLS_DIR)/go.mod $(TOOLS_DIR)/go.sum \
		$(EXAMPLES_KIND_DIR)/go.mod $(EXAMPLES_KIND_DIR)/go.sum \
		$(PROVIDERS_KIND_DIR)/go.mod $(PROVIDERS_KIND_DIR)/go.sum \
		$(EXAMPLES_CLUSTER_API_DIR)/go.mod $(EXAMPLES_CLUSTER_API_DIR)/go.sum \
		$(PROVIDERS_CLUSTER_API_DIR)/go.mod $(PROVIDERS_CLUSTER_API_DIR)/go.sum \
		$(EXAMPLES_CLUSTER_INVENTORY_API_DIR)/go.mod $(EXAMPLES_CLUSTER_INVENTORY_API_DIR)/go.sum \
		$(PROVIDERS_CLUSTER_INVENTORY_API_DIR)/go.mod $(PROVIDERS_CLUSTER_INVENTORY_API_DIR)/go.sum \
	); then \
		git diff; \
		echo "go module files are out of date, please run 'make modules'"; exit 1; \
	fi
	$(GO_MOD_CHECK) $(GO_MOD_CHECK_IGNORE)

APIDIFF_OLD_COMMIT ?= $(shell git rev-parse origin/main)

.PHONY: apidiff
verify-apidiff: $(GO_APIDIFF) ## Check for API differences
	$(GO_APIDIFF) $(APIDIFF_OLD_COMMIT) --print-compatible

## --------------------------------------
## Release Tooling
## --------------------------------------


.PHONY: provider-release
provider-release: ## Create a commit bumping the provider modules to the latest release tag and tag providers.
	@./hack/release-providers.sh

## --------------------------------------
## Helpers
## --------------------------------------

##@ helpers:

go-version: ## Print the go version we use to compile our binaries and images
	@echo $(GO_VERSION)

WHAT ?=
imports:
	@if [ -n "$(WHAT)" ]; then \
		$(GOLANGCI_LINT) run --enable-only=gci --fix --fast $(WHAT); \
	else \
	  for MOD in . $$(git ls-files '**/go.mod' | sed 's,/go.mod,,'); do \
		(cd $$MOD; $(GOLANGCI_LINT) run --enable-only=gci --fix --fast); \
	  done; \
	fi
