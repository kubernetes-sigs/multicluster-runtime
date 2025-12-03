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

tmp_root=${TMPDIR:-/tmp}
kb_root_dir=$tmp_root/kubebuilder
examples_install_dir="${tmp_root}/mcr-examples"

export GOTOOLCHAIN="go$(make go-version)"

# Run verification scripts.
${hack_dir}/verify.sh

# Run tests.
${hack_dir}/test-all.sh

header_text "confirming examples compile (via go install)"
mkdir -p "$examples_install_dir"
for EXAMPLE in $(ls examples); do
    pushd examples/${EXAMPLE}; GOBIN="$examples_install_dir" go install ${MOD_OPT} .; popd
done
rm -r "$examples_install_dir"

echo "passed"
exit 0
