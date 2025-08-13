#! /usr/bin/env bash
# Copyright 2025 The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -o errexit

TAG=${1}
GIT_TAG_OPTIONS=${GIT_TAG_OPTIONS:-}
GIT_COMMIT_OPTIONS=${GIT_COMMIT_OPTIONS:-}
GIT_COMMIT_PUSH=${GIT_COMMITS_PUSH:-}
GIT_COMMIT_REMOTE=${GIT_COMMITS_REMOTE:-}

echo "Updating all modules ..."

find "providers" "examples" -name go.mod | while read -r GOMOD; do
    module=$(dirname "${GOMOD}")
    if [ "$(git tag -l "${module}/${TAG}")" ]; then
        echo "${module}/${LTAG} already exists. Do you really need to run this script?"
        exit 1
    fi

    pushd "${module}"
    go get "sigs.k8s.io/multicluster-runtime@${TAG}"
    go mod tidy
    popd
done

echo "Creating release commit ..."

git add ./providers/ ./examples/
git commit -m "Prepare release for sigs.k8s.io/multicluster-runtime@${TAG}" ${GIT_COMMIT_OPTIONS}

if [ -n "$GIT_COMMIT_PUSH" ] && [ -n "$GIT_COMMIT_REMOTE" ]; then
    echo "Pushing release commit to ${GIT_COMMIT_REMOTE} ..."
    git push "${GIT_COMMIT_REMOTE}"
fi

echo "Creating release tags ..."

# set up main tag
git tag -am "${TAG}" "${TAG}" ${GIT_TAG_OPTIONS}
echo "Created tag ${TAG}."

# create provider tags
find "providers" -name go.mod | while read -r GOMOD; do
    module=$(dirname "${GOMOD}")
    git tag -am "${module}/${TAG}" "${module}/${TAG}" ${GIT_TAG_OPTIONS}
    echo "Created tag ${module}/${TAG}."
done

echo ""
echo "Tags have been successfully created on the release commit."
echo "As a next step, open a PR for your branch in kubernetes-sigs/multicluster-runtime."
echo "Once the PR has been merged, push all git tags created above."
