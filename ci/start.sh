#!/bin/sh

Channel="nixos-23.11"
: "${branch:=ci-release}"
ConfigURL="https://github.com/dnr/styx/archive/refs/heads/$branch.tar.gz"

# match values in terraform:
SignKeySSM="styx-charon-signkey-test-1"
CopyDest="s3://styx-1/nixcache/?region=us-east-1&compression=zstd&parallel-compression=true"

: "${TEMPORAL_NAMESPACE:=charon}"
temporal workflow start --task-queue charon --type ci --workflow-id ci-release --input '{
  "Channel": "'"$Channel"'",
  "ConfigURL": "'"$ConfigURL"'",
  "SignKeySSM": "'"$SignKeySSM"'",
  "CopyDest": "'"$CopyDest"'",
  "LastRelID": "",
}'
