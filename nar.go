package styx

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"strings"
	"time"

	"github.com/nix-community/go-nix/pkg/narinfo"
)

func GetNarFromNixDump(pathOrHash string) (io.Reader, func() error, error) {
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

func downloadNar(upstream, reqName, narPath string) (retPath string, retErr error) {
	fileHash := path.Base(narPath)
	compression := path.Ext(fileHash)
	fileHash = strings.TrimSuffix(fileHash, compression)

	start := time.Now()
	u := url.URL{Scheme: "http", Host: upstream, Path: "/" + narPath}
	res, err := http.Get(u.String())
	if err != nil {
		log.Print("download http error: ", err, " for ", u.String())
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		log.Print("download http status: ", res.Status, " for ", u.String())
		return "", fmt.Errorf("http error %s", res.Status)
	}

	f, err := os.CreateTemp("", "nar")
	name := f.Name()
	defer func() {
		if retErr != nil {
			os.Remove(name)
		}
	}()
	defer f.Close()

	var decompress *exec.Cmd
	switch compression {
	case "", "none":
		decompress = nil
	case ".xz":
		decompress = exec.Command(xzBin, "-d")
	case ".zst":
		decompress = exec.Command(zstdBin, "-d")
	default:
		return "", fmt.Errorf("unknown compression %q", compression)
	}
	if decompress != nil {
		decompress.Stdin = res.Body
		decompress.Stdout = f
		decompress.Stderr = os.Stderr
		if err = decompress.Start(); err != nil {
			log.Print("download decompress start error: ", err)
			return "", err
		}
		if err = decompress.Wait(); err != nil {
			log.Print("download decompress error: ", err)
			return "", err
		}
	} else {
		if _, err = io.Copy(f, res.Body); err != nil {
			log.Print("download copy error: ", err)
			return "", err
		}
	}
	var size int64
	if st, err := f.Stat(); err == nil {
		size = st.Size()
	}

	elapsed := time.Since(start)
	ps := decompress.ProcessState
	log.Printf("downloaded %s [%d bytes] in %s [decmp %s user, %s sys]: %.3f MB/s",
		reqName, size, elapsed, ps.UserTime(), ps.SystemTime(),
		float64(size)/elapsed.Seconds()/1e6,
	)
	return name, nil
}

func downloadNarFromInfo(upstream, storePathHash string) (string, error) {
	u := url.URL{
		Scheme: "http",
		Host:   upstream,
		Path:   "/" + storePathHash + ".narinfo",
	}
	us := u.String()
	res, err := http.Get(us)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		if res.StatusCode == http.StatusNotFound {
			return "", fmt.Errorf("not found")
		}
		return "", fmt.Errorf("http error %s", res.Status)
	}
	ni, err := narinfo.Parse(res.Body)
	if err != nil {
		return "", err
	}
	return downloadNar(upstream, ni.StorePath[44:], ni.URL)
}
