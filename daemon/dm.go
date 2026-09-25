package daemon

import (
	"context"
	"errors"
	"fmt"

	"github.com/anatol/devmapper.go"
	"github.com/google/uuid"
)

func (s *Server) setupDm(dmName string, flags uint32, tab devmapper.Table) (dmPath string, err error) {
	s.serializeSlabOps.Lock()
	defer s.serializeSlabOps.Unlock()
	if err := context.Cause(s.shutdownCtx); err != nil {
		return "", err
	}
	return s.setupDmLocked(dmName, flags, tab)
}

// setupDmLocked is also called while setting up a clone slab, which already
// holds serializeSlabOps.
func (s *Server) setupDmLocked(dmName string, flags uint32, tab devmapper.Table) (dmPath string, err error) {

	reusing := false
	if dmPath, err = findDmByName(dmName); err == nil {
		// try to reuse previous
		if err = devmapper.Suspend(dmName); err != nil {
			return "", fmt.Errorf("dm suspend %q: %w", dmName, err)
		}
		reusing = true
		defer func() {
			if err != nil && reusing {
				// if we failed to load and we were reusing, try to resume old
				if resumeErr := devmapper.Resume(dmName); resumeErr != nil {
					err = errors.Join(err, fmt.Errorf("dm restore %q: %w", dmName, resumeErr))
				}
			}
		}()
	} else {
		var devNo uint64
		if devNo, err = devmapper.Create(dmName, uuid.NewString()); err != nil {
			return "", fmt.Errorf("dm create %q: %w", dmName, err)
		}
		dmPath = devmapper.Path(devNo)
	}
	defer s.markForUdev(dmPath)()
	if err = devmapper.Load(dmName, flags, tab); err != nil {
		return "", fmt.Errorf("dm load %q: %w", dmName, err)
	} else if err = devmapper.Resume(dmName); err != nil {
		return "", fmt.Errorf("dm resume %q: %w", dmName, err)
	}
	reusing = false
	return dmPath, nil
}

func findDmByName(dmName string) (string, error) {
	if di, err := devmapper.InfoByName(dmName); err == nil {
		return devmapper.Path(di.DevNo), nil
	} else {
		return "", err
	}
}
