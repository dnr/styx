package daemon

const (
	dbFilename = "styx.bolt"

	compactFile = "COMPACT"

	slabPrefix         = "_slab_"
	slabImagePrefix    = "_slabimg_"
	manifestSlabPrefix = "_manifests_"

	isManifestPrefix = "M/"

	fakeCacheBind = "localhost:7444"
)

var (
	metaBucket     = []byte("meta")
	chunkBucket    = []byte("chunk")
	slabBucket     = []byte("slab")
	imageBucket    = []byte("image")
	manifestBucket = []byte("manifest")
	catalogFBucket = []byte("catalogf") // name + hash -> [sysid]
	catalogRBucket = []byte("catalogr") // hash -> name
	gcstateBucket  = []byte("gcstate")

	metaSchema = []byte("schema")
	metaParams = []byte("params")
)
