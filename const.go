package styx

const (
	// tag for cachefiled. not sure if it's useful to have more than one.
	cacheTag = "styx0"

	domainId = "styx"

	dbFilename = "styx.bolt"

	// TODO: eventually these can be configurable but let's fix them for now for simplicity
	blockShift = 12
	blockSize  = 1 << blockShift
	chunkShift = 16
	chunkSize  = 1 << chunkShift
)

var (
	// binary paths (can be overridden by ldflags)
	gzipBin = "gzip"
	nixBin  = "nix"
	xzBin   = "xz"
	zstdBin = "zstd"
)
