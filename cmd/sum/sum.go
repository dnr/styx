package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/dnr/styx/common/errgroup"
	"github.com/dnr/styx/common/shift"
	"github.com/jotfs/fastcdc-go"
)

func DefaultChunkShift(fileSize int64) shift.Shift {
	// aim for 64-256 chunks/file
	switch {
	case fileSize <= 256<<16: // 16 MiB
		return 16
	case fileSize <= 256<<18: // 64 MiB
		return 18
	default:
		return shift.MaxChunkShift
	}
}

func main() {
	blk := shift.Shift(12)

	var totalbyblock atomic.Int64

	var byfilelock sync.Mutex
	byfile := make(map[[sha256.Size]byte]int64)
	var bychunklock sync.Mutex
	bychunk := make(map[[sha256.Size]byte]int64)
	var bycdclock sync.Mutex
	bycdc := make(map[[sha256.Size]byte]int64)

	eg := errgroup.WithContext(context.Background())
	eg.SetLimit(300)

	for _, p := range os.Args[1:] {
		eg.Go(func() error {
			abs, err := filepath.Abs(p)
			if err != nil {
				return fmt.Errorf("abs %w", err)
			}
			return fs.WalkDir(os.DirFS("/"), abs[1:], func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return fmt.Errorf("pre %q %w", path, err)
				}
				if !d.Type().IsRegular() {
					return nil
				}
				fi, err := d.Info()
				if err != nil {
					return fmt.Errorf("info %q %w", path, err)
				}
				if fi.Size() <= 224 {
					return nil
				}
				size := blk.Roundup(fi.Size())
				totalbyblock.Add(size)

				b, err := os.ReadFile("/" + path)
				bb := b
				if err != nil {
					return err
				}

				filehash := sha256.Sum256(b)
				byfilelock.Lock()
				byfile[filehash] = size
				byfilelock.Unlock()

				cs := DefaultChunkShift(size)
				for len(b) > 0 {
					rest := min(int64(len(b)), cs.Size())
					chash := sha256.Sum256(b[:rest])
					b = b[rest:]
					bychunklock.Lock()
					bychunk[chash] = rest
					bychunklock.Unlock()
				}

				chunker, err := fastcdc.NewChunker(bytes.NewReader(bb), fastcdc.Options{
					MinSize:     16 * 1024,
					MaxSize:     256 * 1024,
					AverageSize: 64 * 1024,
				})
				if err != nil {
					return err
				}
				for {
					chunk, err := chunker.Next()
					if err == io.EOF {
						break
					}
					if err != nil {
						return err
					}

					chash := sha256.Sum256(chunk.Data)
					bycdclock.Lock()
					bycdc[chash] = int64(chunk.Length)
					bycdclock.Unlock()
				}

				return nil
			})
		})
	}
	err := eg.Wait()
	if err != nil {
		log.Fatalln(err)
	}

	fmt.Printf("pkg only: %d\n", totalbyblock.Load()/1024)

	var uniquebyfile int64
	for _, v := range byfile {
		uniquebyfile += v
	}
	fmt.Printf("by file: %d\n", uniquebyfile/1024)

	var uniquebychunk int64
	for _, v := range bychunk {
		uniquebychunk += v
	}
	fmt.Printf("by chnk: %d\n", uniquebychunk/1024)

	var uniquebycdc int64
	for _, v := range bycdc {
		uniquebycdc += v
	}
	fmt.Printf("by cdc: %d\n", uniquebycdc/1024)
}
