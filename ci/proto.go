package ci

const (
	workflowType = "ci" // matches function name

	taskQueue      = "charon"
	heavyTaskQueue = "charon-heavy"
)

type (
	CiArgs struct {
		// constants
		Channel             string
		ConfigURL           string
		SignKeySSM          string
		CopyDest            string
		ManifestUpstream    string
		PublicCacheUpstream string

		// state
		LastRelID string // "nixos-23.11.7609.5c2ec3a5c2ee"
	}

	pollReq struct {
		Channel   string
		LastRelID string
	}
	pollRes struct {
		RelID string
	}

	buildReq struct {
		// global args
		Args *CiArgs

		// build args
		RelID string
	}
	buildRes struct {
		// TODO: add stats?
	}
)
