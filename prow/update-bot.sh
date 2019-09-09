#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

# Get the new version - since we're pushing via Prow's own bazel-based logic, we don't actually know the version ahead
# of time. So get the abbreviated commit.
new_version="v$(date -u '+%Y%m%d')-$(git rev-parse --short=7 HEAD)"

jx step create pr regex --regex "(?m)^\s+repository: gcr.io/jenkinsxio/prow\s+tag: (?P<version>.*)" --version ${new_version} --files prow/values.yaml --repo https://github.com/jenkins-x-charts/prow.git
