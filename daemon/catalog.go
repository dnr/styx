package daemon

import (
	"bytes"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/nix-community/go-nix/pkg/nixbase32"
	"github.com/nix-community/go-nix/pkg/storepath"
	"go.etcd.io/bbolt"
)

const (
	// Use only 80 bits for reverse references to save space in db.
	// Collisions may be possible but would only lead to suboptimal diff base choices.
	sphPrefixBytes = 10
)

type (
	Sph       [storepath.PathHashSize]byte
	SphPrefix [sphPrefixBytes]byte

	catalogResult struct {
		reqName  string
		baseName string
		baseHash Sph
		reqHash  Sph
	}
)

func ParseSph(s string) (sph Sph, sphStr string, err error) {
	sph, sphStr, _, err = parseSphAndName(s)
	return
}

func ParseSphAndName(s string) (sph Sph, sphStr, spName string, err error) {
	sph, sphStr, spName, err = parseSphAndName(s)
	if err == nil && spName == "" {
		err = mwErr(http.StatusBadRequest, "store path missing name")
	}
	return
}

func parseSphAndName(s string) (sph Sph, sphStr, spName string, err error) {
	sphStr, spName, _ = strings.Cut(s, "-")
	if len(sphStr) != 32 {
		err = mwErr(http.StatusBadRequest, "path is not a valid store path")
		return
	}
	var n int
	n, err = nixbase32.Decode(sph[:], []byte(sphStr))
	if err != nil || n != len(sph) {
		err = mwErr(http.StatusBadRequest, "path is not a valid store path")
	}
	return
}

func (res *catalogResult) usingBase() bool {
	return res.baseName != ""
}

func (s Sph) String() string {
	return nixbase32.EncodeToString(s[:])
}

func SphFromBytes(b []byte) (sph Sph) {
	copy(sph[:], b)
	return
}

func SphPrefixFromBytes(b []byte) (sphp SphPrefix) {
	copy(sphp[:], b)
	return
}

// sph prefix -> rest of name
func (s *Server) catalogFindName(tx *bbolt.Tx, reqHashPrefix SphPrefix) (Sph, string) {
	cur := tx.Bucket(catalogRBucket).Cursor()
	// Note that Seek on this prefix will find the first key that matches it.
	// It may be the "wrong" one due to a collision since we use only half the bytes.
	// That means less than ideal diffing but it won't break anything.
	k, v := cur.Seek(reqHashPrefix[:])
	if len(k) < sphPrefixBytes {
		return Sph{}, ""
	} else if !bytes.Equal(k[:sphPrefixBytes], reqHashPrefix[:]) {
		var zerop SphPrefix
		log.Printf("mismatched sphp, wanted %s, got %s",
			nixbase32.EncodeToString(bytes.Join([][]byte{reqHashPrefix[:], zerop[:]}, nil)),
			nixbase32.EncodeToString(k))
		return Sph{}, ""
	}
	return SphFromBytes(k), string(v)
}

// given a hash, find another hash that we think is the most similar candidate
func (s *Server) catalogFindBase(tx *bbolt.Tx, reqHashPrefix SphPrefix) (catalogResult, error) {
	reqHash, reqName := s.catalogFindName(tx, reqHashPrefix)
	return s.catalogFindBaseFromHashAndName(tx, reqHash, reqName)
}

func (s *Server) catalogFindBaseFromHashAndName(tx *bbolt.Tx, reqHash Sph, reqName string) (catalogResult, error) {
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

	firstDash := strings.IndexByte(reqName, '-')
	numDashes := strings.Count(reqName, "-")
	var start string
	if firstDash < 0 {
		start = reqName
	} else {
		start = reqName[:firstDash+1]
	}
	startb := []byte(start)

	var bestmatch int
	var besthash Sph
	var bestname string

	// look at everything that matches up to the first dash
	cur := tx.Bucket(catalogFBucket).Cursor()
	for k, _ := cur.Seek(startb); k != nil && bytes.HasPrefix(k, startb); k, _ = cur.Next() {
		name, hash, found := bytes.Cut(k, []byte{0})
		if !found {
			continue // this is a bug
		}
		sph := SphFromBytes(hash)
		if sph != reqHash && bytes.Count(name, []byte{'-'}) == numDashes {
			// take last best instead of first since it's probably more recent
			if match := matchLen(reqName, name); match >= bestmatch {
				bestmatch = match
				bestname = string(name)
				besthash = sph
			}
		}
	}

	if bestname == "" {
		return catalogResult{}, errors.New("no diff base for " + reqName)
	}

	return catalogResult{
		reqName:  reqName,
		baseName: bestname,
		baseHash: besthash,
		reqHash:  reqHash,
	}, nil
}

func matchLen(a string, b []byte) int {
	i := 0
	for ; i < len(a) && i < len(b) && a[i] == b[i]; i++ {
	}
	return i
}
