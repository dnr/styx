package ci

import (
	"time"

	"github.com/dnr/styx/manifester"
)

const (
	workflowType = "ci" // matches function name

	taskQueue      = "charon"
	heavyTaskQueue = "charon-heavy"
)

type (
	CiArgs struct {
		// constants

		// what to watch and build
		Channel  string
		StyxRepo RepoConfig

		// where to copy it
		CopyDest            string
		ManifestUpstream    string
		PublicCacheUpstream string

		// state
		LastRelID      string   `json:",omitempty"` // "nixos-23.11.7609.5c2ec3a5c2ee"
		LastStyxCommit string   `json:",omitempty"`
		PrevNames      []string `json:",omitempty"`
		LastGC         int64    `json:",omitempty"` // unix seconds
	}

	RepoConfig struct {
		Repo   string
		Branch string
	}

	pollChannelReq struct {
		Channel   string
		LastRelID string
	}
	pollChannelRes struct {
		RelID string
	}

	pollRepoReq struct {
		Config     RepoConfig
		LastCommit string
	}
	pollRepoRes struct {
		Commit string
	}

	buildReq struct {
		// global args
		Args *CiArgs

		// build args
		RelID      string
		StyxCommit string
	}
	buildRes struct {
		FakeError     string
		Names         []string
		ManifestStats manifester.Stats
		NewLastGC     int64  `json:",omitempty"`
		GCSummary     string `json:",omitempty"`
	}
	buildErrDetails struct {
		Logs string
	}

	notifyReq struct {
		Args                *CiArgs
		RelID               string
		StyxCommit          string
		Error               string
		ErrorDetails        *buildErrDetails
		BuildElapsed        time.Duration
		PrevNames, NewNames []string
		ManifestStats       manifester.Stats
		GCSummary           string `json:",omitempty"`
	}
)
