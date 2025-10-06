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

set -e

source $(dirname ${BASH_SOURCE})/common.sh

header_text "running go test"

if [[ -n ${ARTIFACTS:-} ]]; then
  GINKGO_ARGS="-ginkgo.junit-report=junit-report.xml"
fi

header_text "installing envtest tools@${ENVTEST_K8S_VERSION} with setup-envtest if necessary"
ENVTEST_K8S_VERSION=${ENVTEST_K8S_VERSION:-"1.30.0"}
tmp_bin=/tmp/cr-tests-bin
(
    # don't presume to install for the user
    GOBIN=${tmp_bin} go install sigs.k8s.io/controller-runtime/tools/setup-envtest@release-0.21
)
export KUBEBUILDER_ASSETS="$(${tmp_bin}/setup-envtest use --use-env -p path "${ENVTEST_K8S_VERSION}")"

result=0
go test -v -race ${P_FLAG} ${MOD_OPT} ./... --ginkgo.fail-fast ${GINKGO_ARGS} \
  && ( cd providers/cluster-inventory-api; go test -v -race ${P_FLAG} ${MOD_OPT} ./... --ginkgo.fail-fast ${GINKGO_ARGS} ) \
  || result=$?

if [[ -n ${ARTIFACTS:-} ]]; then
  mkdir -p ${ARTIFACTS}
  for file in `find . -name *junit-report.xml`; do
    new_file=${file#./}
    new_file=${new_file%/junit-report.xml}
    new_file=${new_file//"/"/"-"}
    mv "$file" "$ARTIFACTS/junit_${new_file}.xml"
  done
fi

exit $result
