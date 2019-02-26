#!/usr/bin/env bash
set -o errexit
set -o nounset
set -o pipefail

TAG=pipeline13
CMDS="crier deck hook horologium plank pipeline build sinker tide"

## now loop through the above array
for i in $CMDS
do
  echo "building ${i}"
  pushd ${i}
    # Need to build without dynamic linking to linux libraries to run in containers
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64  go build -a -ldflags '-extldflags "-static"' ./...
    docker build -t jenkinsxio/${i}:$TAG .
    docker push jenkinsxio/${i}:$TAG
  popd
done

