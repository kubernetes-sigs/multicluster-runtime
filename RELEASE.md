# Release Process

The multicluster-runtime Project is released on an as-needed basis, roughly
following controller-runtime releases. The process is as follows:

1. An issue is proposing a new release with a changelog since the last release.
1. All [OWNERS](OWNERS) must LGTM this release.
1. Set the version variables:
   ```sh
   export MAJOR=0
   export MINOR=23
   export PATCH=3
   ```
1. Create a release branch with `git checkout -b release-${MAJOR}.${MINOR}` from `main`
   and push it to GitHub `git push origin release-${MAJOR}.${MINOR}` (skip for patch releases
   where the branch already exists).
1. An OWNER runs `hack/release-commit.sh v${MAJOR}.${MINOR}.${PATCH}` on the release branch.
   This updates all provider and example module dependencies and creates a release commit
   with tags for the main module and each provider.
1. Open a PR for the release branch and get it merged.
1. Push all tags created by the script:
   ```sh
   git push origin v${MAJOR}.${MINOR}.${PATCH} \
     providers/cluster-inventory-api/v${MAJOR}.${MINOR}.${PATCH} \
     providers/cluster-api/v${MAJOR}.${MINOR}.${PATCH} \
     providers/file/v${MAJOR}.${MINOR}.${PATCH} \
     providers/kind/v${MAJOR}.${MINOR}.${PATCH}
   ```
1. The main tag is promoted to a release on GitHub with the changelog attached.
1. The release issue is closed.
