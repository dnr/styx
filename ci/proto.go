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
		LastBump string
	}

	pollReq struct {
		Channel  string
		LastBump string
	}
	pollRes struct {
	}

	somethingArgs struct {
	}
)
