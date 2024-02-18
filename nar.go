package styx

import (
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
)

func getNarFromNixDump(pathOrHash string) (io.Reader, func() error, error) {
	if _, err := os.Stat(pathOrHash); os.IsNotExist(err) && len(pathOrHash) == 32 {
		// TODO: search in /nix/store for something with this prefix
		return nil, nil, errors.New("implement this")
	} else if err != nil {
		return nil, nil, err
	}
	cmd := exec.Command("nix-store", "dump", pathOrHash)
	cmd.Stderr = os.Stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	if err = cmd.Start(); err != nil {
		return nil, nil, err
	}
	return out, cmd.Wait, nil
}

func getNarFromBinaryCache(cache url.URL, hash string) (io.Reader, func() error, error) {
	return nil, nil, errors.New("copy from nix-sandwich")
}
