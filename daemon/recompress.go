package daemon

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os/exec"
	"path"
	"regexp"

	"github.com/dnr/styx/common"
	"github.com/dnr/styx/manifester"
	"github.com/dnr/styx/pb"
)

var (
	reManPage   = regexp.MustCompile(`^/share/man/.*[.]gz$`)
	reLinuxKoXz = regexp.MustCompile(`^/lib/modules/[^/]+/kernel/.*[.]ko[.]xz$`)

	recompressManPage = []string{manifester.ExpandGz}
	// note: currently largest kernel module on my system (excluding kheaders) is
	// amdgpu.ko.xz at 3.4mb, 54 chunks (64kb), and expands to 24.4mb, which is
	// reasonable to pass through the chunk differ.
	// TODO: maybe get these args from looking at the base? or the chunk differ can look at
	// req and return them? or try several values and take the matching one?
	recompressLinuxKo = []string{manifester.ExpandXz, "--check=crc32", "--lzma2=dict=1MiB"}
)

func isManPageGz(ent *pb.Entry) bool {
	return ent.Type == pb.EntryType_REGULAR && reManPage.MatchString(ent.Path)
}
func isLinuxKoXz(ent *pb.Entry) bool {
	// kheaders.ko.xz is mostly an embedded .tar.xz file (yes, again), so expanding it won't help.
	return ent.Type == pb.EntryType_REGULAR && reLinuxKoXz.MatchString(ent.Path) &&
		path.Base(ent.Path) != "kheaders.ko.xz"
}

func getRecompressArgs(ent *pb.Entry) []string {
	switch {
	case isManPageGz(ent):
		return recompressManPage
	case isLinuxKoXz(ent):
		return recompressLinuxKo
	default:
		return nil
	}
}

func doDiffDecompress(ctx context.Context, data []byte, args []string) ([]byte, error) {
	if len(args) == 0 {
		return data, nil
	}
	switch args[0] {
	case manifester.ExpandGz:
		gz, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		return io.ReadAll(gz)

	case manifester.ExpandXz:
		xz := exec.Command(common.XzBin, "-d")
		xz.Stdin = bytes.NewReader(data)
		return xz.Output()

	default:
		return nil, fmt.Errorf("unknown expander %q", args[0])
	}
}

func doDiffRecompress(ctx context.Context, data []byte, args []string) ([]byte, error) {
	if len(args) == 0 {
		return data, nil
	}
	switch args[0] {
	case manifester.ExpandGz:
		gz := exec.Command(common.GzipBin, "-nc")
		gz.Stdin = bytes.NewReader(data)
		return gz.Output()

	case manifester.ExpandXz:
		xz := exec.Command(common.XzBin, append([]string{"-c"}, args[1:]...)...)
		xz.Stdin = bytes.NewReader(data)
		return xz.Output()

	default:
		return nil, fmt.Errorf("unknown expander %q", args[0])
	}
}
