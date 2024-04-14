package common

var (
	// binary paths (can be overridden by ldflags)
	GzipBin = "gzip"
	NixBin  = "nix"
	XzBin   = "xz"
	ZstdBin = "zstd"

	Version = "dev"

	// Context for signatures
	ManifestContext     = "styx-manifest-1"
	DaemonParamsContext = "styx-daemon-params-1"
)
