package coffee

import (
	"encoding/binary"
	"errors"
)

// Frames are the wire format of the engine (DOCS.md → Protocol → Frames):
//
//	"CTF1" | u32 json_len | json | u32 blob_count | blob_count × (u32 len | bytes)
//
// All integers are little-endian.

var frameMagic = [4]byte{'C', 'T', 'F', '1'}

var errBadFrame = errors.New("coffee: malformed frame from the engine")

func encodeFrame(json []byte, blobs [][]byte) []byte {
	size := 12 + len(json)
	for _, b := range blobs {
		size += 4 + len(b)
	}
	out := make([]byte, 0, size)
	out = append(out, frameMagic[:]...)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(json)))
	out = append(out, json...)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(blobs)))
	for _, b := range blobs {
		out = binary.LittleEndian.AppendUint32(out, uint32(len(b)))
		out = append(out, b...)
	}
	return out
}

// decodeFrame splits a frame. The returned slices alias buf.
func decodeFrame(buf []byte) (json []byte, blobs [][]byte, err error) {
	if len(buf) < 12 || [4]byte(buf[:4]) != frameMagic {
		return nil, nil, errBadFrame
	}
	jl := uint64(binary.LittleEndian.Uint32(buf[4:]))
	p := 8 + jl
	if p+4 > uint64(len(buf)) {
		return nil, nil, errBadFrame
	}
	json = buf[8:p]
	n := binary.LittleEndian.Uint32(buf[p:])
	p += 4
	if uint64(n)*4 > uint64(len(buf))-p {
		return nil, nil, errBadFrame
	}
	blobs = make([][]byte, 0, n)
	for i := uint32(0); i < n; i++ {
		if p+4 > uint64(len(buf)) {
			return nil, nil, errBadFrame
		}
		l := uint64(binary.LittleEndian.Uint32(buf[p:]))
		p += 4
		if p+l > uint64(len(buf)) {
			return nil, nil, errBadFrame
		}
		blobs = append(blobs, buf[p:p+l:p+l])
		p += l
	}
	return json, blobs, nil
}
