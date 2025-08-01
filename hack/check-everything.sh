#!/usr/bin/env bash

#  Copyright 2018 The Kubernetes Authors.
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

set -o errexit
set -o nounset
set -o pipefail

hack_dir=$(dirname ${BASH_SOURCE})
source ${hack_dir}/common.sh

tmp_root=/tmp
kb_root_dir=$tmp_root/kubebuilder

export GOTOOLCHAIN="go$(make go-version)"

# Run verification scripts.
${hack_dir}/verify.sh

# Envtest.
ENVTEST_K8S_VERSION=${ENVTEST_K8S_VERSION:-"1.30.0"}

header_text "installing envtest tools@${ENVTEST_K8S_VERSION} with setup-envtest if necessary"
tmp_bin=/tmp/cr-tests-bin
(
    # don't presume to install for the user
    GOBIN=${tmp_bin} go install sigs.k8s.io/controller-runtime/tools/setup-envtest@release-0.21
)
export KUBEBUILDER_ASSETS="$(${tmp_bin}/setup-envtest use --use-env -p path "${ENVTEST_K8S_VERSION}")"

# Run tests.
${hack_dir}/test-all.sh

header_text "confirming examples compile (via go install)"
pushd examples/kind; go install ${MOD_OPT} .; popd
pushd examples/namespace; go install ${MOD_OPT} .; popd
pushd examples/cluster-api; go install ${MOD_OPT} .; popd
pushd examples/cluster-inventory-api; go install ${MOD_OPT} .; popd

echo "passed"
exit 0
