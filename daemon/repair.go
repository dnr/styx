package daemon

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"go.etcd.io/bbolt"

	"github.com/dnr/styx/common"
)

func (s *Server) handleRepairReq(ctx context.Context, r *RepairReq) (*Status, error) {
	// allow this even before "initialized"

	if r.Presence {
		for slab := uint16(0); slab < manifestSlabOffset; slab++ {
			tag, _ := s.SlabInfo(slab)
			backingPath := filepath.Join(s.cfg.CachePath, fscachePath(s.cfg.CacheDomain, tag))
			if _, err := os.Stat(backingPath); os.IsNotExist(err) {
				break
			}
			s.repairPresence(slab, backingPath)
		}
	}

	return nil, nil
}

func (s *Server) repairPresence(slab uint16, path string) {
	blk := fmt.Sprintf("-b%d", s.blockShift.Size())
	// TODO: use FIEMAP ioctl directly
	out, err := exec.Command(common.FilefragBin, "-evs", blk, path).Output()
	if err != nil {
		return
	}
	re := regexp.MustCompile(`\s*\d+:\s*(\d+)\.\.\s*(\d+):.*`)

	have := make(map[uint32]bool)
	for _, l := range strings.Split(string(out), "\n") {
		m := re.FindStringSubmatch(l)
		if len(m) < 3 {
			continue
		}
		start, err1 := strconv.Atoi(m[1])
		end, err2 := strconv.Atoi(m[2])
		if err1 != nil || err2 != nil {
			continue
		}
		for i := start; i <= end; i++ {
			have[uint32(i)] = true
		}
	}

	s.db.Update(func(tx *bbolt.Tx) error {
		sb := tx.Bucket(slabBucket).Bucket(slabKey(slab))

		var all []uint32
		dbhave := make(map[uint32]bool)
		cur := sb.Cursor()
		var k []byte
		for k, _ = cur.First(); k != nil && addrFromKey(k) < presentMask; k, _ = cur.Next() {
			all = append(all, addrFromKey(k))
		}
		for ; k != nil; k, _ = cur.Next() {
			dbhave[addrFromKey(k)&^presentMask] = true
		}

		for _, addr := range all {
			if !have[addr] && dbhave[addr] {
				log.Println("repair slab", slab, "addr", addr, "marked present but missing from block map")
				sb.Delete(addrKey(addr | presentMask))
			} else if have[addr] && !dbhave[addr] {
				log.Println("repair slab", slab, "addr", addr, "in block map but not present")
				sb.Put(addrKey(addr|presentMask), []byte{})
			}
		}

		return nil
	})
}
