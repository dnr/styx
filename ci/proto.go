package ci

// TODO: proto between workflow/activity/builder

const (
	workflowType = "ci" // matches function name

	taskQueue      = "charon"
	heavyTaskQueue = "charon-heavy"
)

type (
	CiArgs struct {
		// constants
		Channel string

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

	somethingArgs struct {
	}
)
