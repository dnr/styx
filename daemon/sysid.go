package daemon

import (
	"regexp"

	"github.com/dnr/styx/common/sysid"
	"github.com/dnr/styx/pb"
)

var (
	libcRE = regexp.MustCompile(`^(glibc-[\d.-]+|musl-[\d.]+)$`)
	// grub has both a host and target
	grubRE = regexp.MustCompile(`^grub-[\d.-]+$`)
)

func (s *server) sysIdFromManifest(m *pb.Manifest) sysid.Id {
	ni := m.GetMeta().GetNarinfo()
	if ni == nil {
		return sysid.Unknown
	}
	// FIXME: ni.References
}
