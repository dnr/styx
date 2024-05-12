package common

var (
	// binary paths (can be overridden by ldflags)
	NixBin      = "nix"
	XzBin       = "xz"
	ModprobeBin = "modprobe"

	Version = "dev"

	// Context for signatures
	ManifestContext     = "styx-manifest-1"
	DaemonParamsContext = "styx-daemon-params-1"
)
