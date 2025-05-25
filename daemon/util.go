package daemon

import (
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"

	"github.com/dnr/styx/common/cdig"
	"github.com/dnr/styx/erofs"
	"github.com/dnr/styx/pb"
)

func verifyParams(p *pb.GlobalParams) error {
	if p.DigestAlgo != cdig.Algo {
		return fmt.Errorf("built-in digest algo %s != %s; rebuild or use different params",
			cdig.Algo, p.DigestAlgo)
	} else if p.DigestBits != cdig.Bits {
		return fmt.Errorf("built-in digest bits %d != %d; rebuild or use different params",
			cdig.Bits, p.DigestBits)
	}
	return nil
}

func sphpsFromLoc(b []byte) []SphPrefix {
	b = b[6:]
	out := make([]SphPrefix, len(b)/sphPrefixBytes)
	for i := range out {
		out[i] = SphPrefixFromBytes(b[i*sphPrefixBytes : (i+1)*sphPrefixBytes])
	}
	return out
}

func writeToTempFile(b []byte) (string, error) {
	f, err := os.CreateTemp("", "styx-diff")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err = f.Write(b); err != nil {
		return "", err
	}
	return f.Name(), nil
}

func makeManifestSph(sph Sph) Sph {
	// the "manifest sph" for a sph is the same with one bit flipped (will affect _end_ of base32
	// string form). note that this is its own inverse.
	sph[0] ^= 1
	return sph
}

type countReader struct {
	r io.Reader
	c int64
}

func (cr *countReader) Read(p []byte) (int, error) {
	n, err := cr.r.Read(p)
	cr.c += int64(n)
	return n, err
}

func underDir(p, dir string) bool {
	return len(p) >= len(dir) && p[:len(dir)] == dir && (len(p) == len(dir) || dir == "/" || p[len(dir)] == '/')
}

func isErofsMount(p string) (bool, error) {
	var st unix.Statfs_t
	err := unix.Statfs(p, &st)
	return st.Type == erofs.EROFS_MAGIC, err
}

func getMountNs(pid int) (string, error) {
	path := fmt.Sprintf("/proc/%d/ns/mnt", pid)
	return os.Readlink(path)
}

func havePrivateMountNs() (bool, error) {
	myMountNs, err := getMountNs(os.Getpid())
	if err != nil {
		return false, err
	}
	parentMountNs, err := getMountNs(os.Getppid())
	if err != nil {
		return false, err
	}
	return myMountNs != parentMountNs, nil
}
