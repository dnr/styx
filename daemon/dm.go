package daemon

import (
	"fmt"

	"github.com/anatol/devmapper.go"
	"github.com/google/uuid"
)

func (s *Server) setupDm(dmName string, flags uint32, tab devmapper.Table) (dmPath string, err error) {
	if dmPath, err = findDmByName(dmName); err == nil {
		// try to reuse previous
		if err = devmapper.Suspend(dmName); err != nil {
			return "", fmt.Errorf("dm suspend %q: %w", dmName, err)
		}
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
	return dmPath, nil
}

func findDmByName(dmName string) (string, error) {
	if di, err := devmapper.InfoByName(dmName); err == nil {
		return devmapper.Path(di.DevNo), nil
	} else {
		return "", err
	}
}
