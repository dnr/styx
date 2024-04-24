package daemon

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/google/btree"
	"github.com/nix-community/go-nix/pkg/nixbase32"
	"github.com/nix-community/go-nix/pkg/storepath"
)

type (
	Sph [storepath.PathHashSize]byte

	btItem struct {
		rest string
		hash Sph
		// TODO: get "system" information in here
	}

	catalog struct {
		lock sync.Mutex
		// bt is sorted map of btITem (sorted by rest)
		bt *btree.BTreeG[btItem]
		// m is map of hash -> rest
		m map[Sph]string
	}

	catalogResult struct {
		reqName  string
		baseName string
		matchLen int
		baseHash Sph
	}
)

func (s Sph) String() string {
	return nixbase32.EncodeToString(s[:])
}

func itemLess(a, b btItem) bool {
	return a.rest < b.rest || (a.rest == b.rest && bytes.Compare(a.hash[:], b.hash[:]) < 0)
}

func newCatalog() *catalog {
	return &catalog{
		bt: btree.NewG[btItem](4, itemLess),
		m:  make(map[Sph]string),
	}
}

// name is "hash-name"
func (c *catalog) add(name string) error {
	c.lock.Lock()
	defer c.lock.Unlock()

	hash, rest, found := strings.Cut(name, "-")
	if !found {
		return fmt.Errorf("bad store path name %q", name)
	} else if nixbase32.DecodedLen(len(hash)) > len(btItem{}.hash) {
		return fmt.Errorf("bad hash %q", hash)
	}
	item := btItem{rest: rest}
	n, err := nixbase32.Decode(item.hash[:], []byte(hash))
	if err != nil || n != len(item.hash) {
		return fmt.Errorf("bad hash %q", hash)
	}
	c.bt.ReplaceOrInsert(item)
	c.m[item.hash] = rest
	return nil
}

// hash -> rest of name
func (c *catalog) findName(reqHash Sph) string {
	c.lock.Lock()
	defer c.lock.Unlock()
	return c.m[reqHash]
}

// given a hash, find another hash that we think is the most similar candidate
func (c *catalog) findBase(reqHash Sph) (catalogResult, error) {
	c.lock.Lock()
	defer c.lock.Unlock()

	reqName := c.m[reqHash]
	if len(reqName) == 0 {
		return catalogResult{}, errors.New("store path hash not found")
	} else if len(reqName) < 3 {
		return catalogResult{}, errors.New("name too short")
	} else if reqName == "source" {
		// TODO: need contents similarity for this one
		return catalogResult{}, errors.New("can't handle 'source'")
	}

	// The "name" part of store paths sometimes has a nice pname-version split like
	// "rsync-3.2.6". But also can be something like "rtl8723bs-firmware-2017-04-06-xz" or
	// "sane-desc-generate-entries-unsupported-scanners.patch" or
	// "python3.10-websocket-client-1.4.1" or "lz4-1.9.4-dev" or of course just "source".
	//
	// So given another store path name, how do we find suitable candidates? We're looking for
	// something where just the version has changed, or maybe an exact match of the name. Let's
	// look at segments separated by dashes.  We can definitely reject anything that doesn't
	// share at least one segment. We should also reject anything that doesn't have the same
	// number of segments, since those are probably other outputs or otherwise separate things.
	// Then we can pick one that has the most segments in common.

	dashes := findDashes(reqName)
	var start string
	if len(dashes) == 0 {
		start = reqName
	} else {
		start = reqName[:dashes[0]+1]
	}

	var bestmatch int
	var best btItem

	// look at everything that matches up to the first dash
	c.bt.AscendRange(
		btItem{rest: start},
		btItem{rest: start + "\xff"},
		func(i btItem) bool {
			if i.hash != reqHash && len(findDashes(i.rest)) == len(dashes) {
				// take last best instead of first since it's probably more recent
				if match := matchLen(reqName, i.rest); match >= bestmatch {
					bestmatch = match
					best = i
				}
			}
			return true
		})

	if best.rest == "" {
		return catalogResult{}, errors.New("no diff base for " + reqName)
	}

	return catalogResult{
		reqName:  reqName,
		baseName: best.rest,
		matchLen: bestmatch,
		baseHash: best.hash,
	}, nil
}

func findDashes(s string) []int {
	var dashes []int
	for i := 0; i < len(s); {
		j := strings.IndexByte(s[i:], '-')
		if j < 0 {
			break
		}
		dashes = append(dashes, i+j)
		i += j + 1
	}
	return dashes
}

func matchLen(a, b string) int {
	i := 0
	for ; i < len(a) && i < len(b) && a[i] == b[i]; i++ {
	}
	return i
}
