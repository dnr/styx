package common

var (
	// binary paths (can be overridden by ldflags)
	NixBin      = "nix"
	GzipBin     = "gzip"
	XzBin       = "xz"
	FilefragBin = "filefrag"

	// replaced by ldflags
	Version = "dev"

	// Context for signatures
	ManifestContext     = "styx-manifest-1"
	DaemonParamsContext = "styx-daemon-params-1"

	DigestAlgo = "sha256"
)
