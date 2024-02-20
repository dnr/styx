package styx

const (
	// tag for cachefiled. not sure if it's useful to have more than one.
	cacheTag = "styx0"

	domainId = "styx"

	dbFilename = "styx.bolt"
)

var (
	// binary paths (can be overridden by ldflags)
	gzipBin = "gzip"
	nixBin  = "nix"
	xzBin   = "xz"
	zstdBin = "zstd"
)
