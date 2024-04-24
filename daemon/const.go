package daemon

const (
	dbFilename = "styx.bolt"

	slabPrefix       = "_slab_"
	magicImagePrefix = "_magic_"

	isManifestPrefix = "M/"
)

var (
	metaBucket     = []byte("meta")
	chunkBucket    = []byte("chunk")
	slabBucket     = []byte("slab")
	imageBucket    = []byte("image")
	manifestBucket = []byte("manifest")

	metaParams = []byte("params")
)
