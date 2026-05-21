# Release Process

The multicluster-runtime Project is released on an as-needed basis, roughly
following controller-runtime releases. The process is as follows:

1. An issue is proposing a new release with a changelog since the last release.
1. All [OWNERS](OWNERS) must LGTM this release.
1. Set the version variables:
   ```sh
   export MAJOR=0
   export MINOR=24
   export PATCH=1
   ```
1. An OWNER runs `hack/release-commit.sh v${MAJOR}.${MINOR}.${PATCH}` on a branch cut from
   the target base (`main` for a major/minor release, the existing `release-${MAJOR}.${MINOR}`
   branch for a patch release). This updates all provider and example module dependencies
   and creates a release commit with tags for the main module and each provider, all pointing
   at that commit.
1. Open a PR against the target base (`main` or the existing release branch) and get it
   merged. The PR **must** be merged via rebase or fast-forward so the release commit's SHA
   is preserved — squash or non-fast-forward merges will rewrite the SHA and invalidate the
   local tags.
1. For a major/minor release, cut the release branch from the merged release commit on `main`
   and push it to GitHub:
   ```sh
   git checkout -b release-${MAJOR}.${MINOR} main
   git push origin release-${MAJOR}.${MINOR}
   ```
   (Skip this step for patch releases — the release commit is already on the existing release
   branch.)
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
