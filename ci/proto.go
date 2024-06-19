package ci

const (
	workflowType = "ci" // matches function name

	taskQueue      = "charon"
	heavyTaskQueue = "charon-heavy"
)

type (
	CiArgs struct {
		// constants
		Channel    string
		ConfigURL  string
		SignKeySSM string
		CopyDest   string

		// state
		LastRelID string // "nixos-23.11.7609.5c2ec3a5c2ee"
	}

	pathAndSize struct {
		Path string `json:"p"`
		Size int64  `json:"s"`
	}

	pollReq struct {
		Channel   string
		LastRelID string
	}
	pollRes struct {
		RelID string
	}

	buildReq struct {
		// building
		Channel   string
		RelID     string
		ConfigURL string

		// copying
		SignKeySSM string
		CopyDest   string
	}
	buildRes struct {
		StorePaths []pathAndSize
		// TODO: add stats?
	}

	manifestReq struct {
		StorePaths []pathAndSize
	}
	manifestRes struct {
		// TODO: add stats?
	}
)
