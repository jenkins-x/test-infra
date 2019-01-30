#!/usr/bin/env bash
set -o errexit
set -o nounset
set -o pipefail

TAG=pipeline1
CMDS="crier deck hook horologium plank pipeline build sinker tide"

## now loop through the above array
for i in $CMDS
do
  echo "building ${i}"
  pushd ${i}
    GOOS=linux GOARCH=amd64  go build ./...
    docker build -t jenkinsxio/${i}:$TAG .
    docker push jenkinsxio/${i}:$TAG
  popd
done

