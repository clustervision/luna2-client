#!/bin/bash

# The version comes from VERSION at the repository root, as in the pipeline.
VERSION=$(tr -d '[:space:]' < "$(dirname "$0")/../../VERSION")
rpmbuild --define "luna_version ${VERSION%-*}" --define "luna_release ${VERSION#*-}" \
    --buildroot `pwd`/src -ba luna2-client.spec
