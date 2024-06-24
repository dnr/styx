#!/bin/sh

# TODO: convert this to terraform that writes out a local script with a provisioner?

# match values in terraform:

: "${TEMPORAL_NAMESPACE:=charon}"
temporal workflow start --task-queue charon --type ci --workflow-id ci-release --input '{
  "Channel": "nixos-23.11",
  "ConfigURL": "https://github.com/dnr/styx/archive/refs/heads/ci-release.tar.gz",
  "SignKeySSM": "styx-charon-signkey-test-1",
  "CopyDest": "s3://styx-1/nixcache/?region=us-east-1&compression=zstd&parallel-compression=true",
  "LastRelID": "",
  "ManifestUpstream": "https://styx-1.s3.amazonaws.com/nixcache/",
  "PublicCacheUpstream": "https://cache.nixos.org/",
}'
