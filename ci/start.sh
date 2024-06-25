#!/bin/sh

# TODO: convert this to terraform that writes out a local script with a provisioner
# so that we can share bucket names, etc.

temporal workflow start --task-queue charon --type ci --workflow-id ci-release --input '{
  "Channel": "nixos-23.11",
  "StyxRepo": {"Repo": "https://github.com/dnr/styx/", "Branch": "release"},
  "CopyDest": "s3://styx-1/nixcache/?region=us-east-1&compression=zstd&parallel-compression=true",
  "ManifestUpstream": "https://styx-1.s3.amazonaws.com/nixcache/",
  "PublicCacheUpstream": "https://cache.nixos.org/",
}'
