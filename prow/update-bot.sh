#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

# Get the new version - since we're pushing via Prow's own bazel-based logic, we don't actually know the version ahead
# of time. So use the same command used in push.sh to get the version.
new_version="v$(date -u '+%Y%m%d')-$(git describe --tags --always --dirty)"

jx step create pr chart --name gcr.io/jenkinsxio/prow --version ${new_version} --repo https://github.com/jenkins-x-charts/prow.git
