// Package hashing computes the cheap sampled prehash (xxh3) used to narrow
// duplicate candidates and the full BLAKE3 hash that confirms them.
package hashing

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"os"

	"github.com/zeebo/blake3"
	"github.com/zeebo/xxh3"
)

// SampleSize is read from the start, middle and end of a file. Sampling all
// three catches media files that share identical headers.
const SampleSize = 16 << 10

// Prehash returns a 16-byte xxh3 of three samples. For files no bigger than
// the samples combined it reads the whole file and also returns the full
// BLAKE3 hash, saving a second read.
func Prehash(path string, size int64) (pre, full []byte, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	if size <= 3*SampleSize {
		b, err := io.ReadAll(io.LimitReader(f, size+1))
		if err != nil {
			return nil, nil, err
		}
		if int64(len(b)) != size {
			return nil, nil, errChanged
		}
		h := xxh3.Hash128(b).Bytes()
		sum := blake3.Sum256(b)
		return h[:], sum[:], nil
	}
	buf := make([]byte, 3*SampleSize+8)
	for i, off := range []int64{0, size/2 - SampleSize/2, size - SampleSize} {
		if _, err := f.ReadAt(buf[i*SampleSize:(i+1)*SampleSize], off); err != nil {
			return nil, nil, err
		}
	}
	binary.LittleEndian.PutUint64(buf[3*SampleSize:], uint64(size))
	h := xxh3.Hash128(buf).Bytes()
	return h[:], nil, nil
}

type changedError struct{}

func (changedError) Error() string { return "file changed while reading" }

var errChanged = changedError{}

// Full returns the BLAKE3-256 hash of the whole file, checking ctx between
// chunks so a long hash can be cancelled.
func Full(ctx context.Context, path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h := blake3.New()
	buf := make([]byte, 1<<20)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, err := f.Read(buf)
		h.Write(buf[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	return h.Sum(nil), nil
}

// SameContent compares two files byte for byte.
func SameContent(a, b string) (bool, error) {
	fa, err := os.Open(a)
	if err != nil {
		return false, err
	}
	defer fa.Close()
	fb, err := os.Open(b)
	if err != nil {
		return false, err
	}
	defer fb.Close()
	ba, bb := make([]byte, 1<<20), make([]byte, 1<<20)
	for {
		na, ea := io.ReadFull(fa, ba)
		nb, eb := io.ReadFull(fb, bb)
		if na != nb || !bytes.Equal(ba[:na], bb[:nb]) {
			return false, nil
		}
		doneA := ea == io.EOF || ea == io.ErrUnexpectedEOF
		doneB := eb == io.EOF || eb == io.ErrUnexpectedEOF
		if doneA || doneB {
			return doneA && doneB, nil
		}
		if ea != nil {
			return false, ea
		}
		if eb != nil {
			return false, eb
		}
	}
}
