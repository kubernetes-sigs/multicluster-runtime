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
ROOT_DIR=$(abspath .)

#
# Go.
#
GO_VERSION ?= 1.24.9

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

CONTROLLER_GEN_VER := v0.19.0
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
$(GO_MOD_CHECK): # Build gomodcheck.
	go build -C $(GO_MOD_CHECK_DIR) -o $(GO_MOD_CHECK)

## --------------------------------------
## Linting
## --------------------------------------

.PHONY: lint
lint: WHAT ?=
lint: $(GOLANGCI_LINT) ## Lint codebase.
	@if [ -n "$(WHAT)" ]; then \
		$(GOLANGCI_LINT) run -v $(GOLANGCI_LINT_EXTRA_ARGS) $(WHAT); \
	else \
	  for MOD in . $$(git ls-files '**/go.mod' | sed 's,/go.mod,,'); do \
		(cd $$MOD; $(GOLANGCI_LINT) run -v $(GOLANGCI_LINT_EXTRA_ARGS)); \
	  done; \
	fi

.PHONY: lint-fix
lint-fix: $(GOLANGCI_LINT) ## Lint the codebase and run auto-fixers if supported by the linter.
	GOLANGCI_LINT_EXTRA_ARGS=--fix $(MAKE) lint

.PHONY: imports
imports: WHAT ?=
imports: $(GOLANGCI_LINT) ## Format module imports.
	@if [ -n "$(WHAT)" ]; then \
		$(GOLANGCI_LINT) fmt --enable gci -c $(ROOT_DIR)/.golangci.yml $(WHAT); \
	else \
	  for MOD in . $$(git ls-files '**/go.mod' | sed 's,/go.mod,,'); do \
		(cd $$MOD; $(GOLANGCI_LINT) fmt --enable gci -c $(ROOT_DIR)/.golangci.yml); \
	  done; \
	fi

## --------------------------------------
## Generate
## --------------------------------------

.PHONY: modules
modules: WHAT ?=
modules: ## Runs go mod to ensure modules are up to date.
	@if [ -n "$(WHAT)" ]; then \
		(cd $(WHAT); go mod tidy); \
	else \
	  for MOD in . $$(git ls-files '**/go.mod' | sed 's,/go.mod,,'); do \
		(cd $$MOD; go mod tidy); \
	  done; \
	fi

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
clean-release: ## Remove the release folder.
	rm -rf $(RELEASE_DIR)

.PHONY: verify-modules
verify-modules: modules $(GO_MOD_CHECK) ## Verify go modules are up to date.
	  @for MOD in . $(TOOLS_DIR) $$(git ls-files '**/go.mod' | sed 's,/go.mod,,'); do \
		pushd $$MOD >/dev/null; if !(git diff --quiet HEAD -- go.sum go.mod); then echo "[$$MOD] go modules are out of date, please run 'make modules'"; exit 1; fi; popd >/dev/null; \
	  done; \

	$(GO_MOD_CHECK) $(GO_MOD_CHECK_IGNORE)

APIDIFF_OLD_COMMIT ?= $(shell git rev-parse origin/main)

.PHONY: apidiff
verify-apidiff: $(GO_APIDIFF) ## Check for API differences.
	$(GO_APIDIFF) $(APIDIFF_OLD_COMMIT) --print-compatible

## --------------------------------------
## Release Tooling
## --------------------------------------


.PHONY: release-commit
release-commit: ## Create a commit bumping the provider modules to the latest release tag and tag providers.
	@./hack/release-commit.sh

## --------------------------------------
## Helpers
## --------------------------------------

##@ helpers:

go-version: ## Print the go version we use to compile our binaries and images.
	@echo $(GO_VERSION)

list-modules: ## Print the Go modules in this repository for GitHub Actions matrix.
	@echo -n '['; \
	git ls-files '**/go.mod' | sed 's,/go.mod,,' | awk 'BEGIN {printf "\"%s\"", "."} NR > 0 {printf ",\"%s\"", $$0}'; \
	echo ']'
