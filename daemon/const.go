package daemon

const (
	dbFilename = "styx.bolt"

	compactFile = "COMPACT"

	slabSubdir = "slabs"

	isManifestPrefix = "M/"

	fakeCacheBind = "localhost:7444"

	// matches udev rules in module
	udevMarkerDir = "/run/styx/markers"
)

var (
	metaBucket      = []byte("meta")
	chunkBucket     = []byte("chunk")
	slabBucket      = []byte("slab")
	imageBucket     = []byte("image")
	manifestBucket  = []byte("manifest")
	catalogFBucket  = []byte("catalogf") // name + hash -> [sysid]
	catalogRBucket  = []byte("catalogr") // hash -> name
	gcstateBucket   = []byte("gcstate")
	fakeCacheBucket = []byte("fakecache")

	metaSchema = []byte("schema")
	metaParams = []byte("params")
)
