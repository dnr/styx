package ci

// TODO: proto between workflow/activity/builder

const (
	workflowType = "ci" // matches function name

	taskQueue      = "charon"
	heavyTaskQueue = "charon-heavy"
)

type (
	somethingArgs struct {
	}
)
