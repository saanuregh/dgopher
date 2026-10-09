package decode

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"fmt"
	"io"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/s2"
	"github.com/klauspost/compress/zstd"
	"github.com/pierrec/lz4/v4"
)

// decompress undoes a compression, up to MaxOutput bytes.
func decompress(f Format, b []byte) ([]byte, error) {
	in := bytes.NewReader(b)
	var r io.Reader
	switch f {
	case Gzip:
		gz, err := gzip.NewReader(in)
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		r = gz
	case Zlib:
		z, err := zlib.NewReader(in)
		if err != nil {
			return nil, err
		}
		defer z.Close()
		r = z
	case Zstd:
		z, err := zstd.NewReader(in, zstd.WithDecoderMaxMemory(MaxOutput))
		if err != nil {
			return nil, err
		}
		defer z.Close()
		r = z
	case LZ4:
		r = lz4.NewReader(in)
	case Snappy:
		if bytes.HasPrefix(b, []byte("\xff\x06\x00\x00sNaPpY")) {
			r = s2.NewReader(in, s2.ReaderMaxBlockSize(4<<20))
			break
		}
		// A block without the framing, as most libraries write it.
		n, err := s2.DecodedLen(b)
		if err != nil {
			return nil, err
		}
		if n > MaxOutput {
			return nil, fmt.Errorf("it would decompress to %d bytes, more than %d", n, MaxOutput)
		}
		return s2.Decode(nil, b)
	case Brotli:
		r = brotli.NewReader(in)
	default:
		return nil, fmt.Errorf("%s is not a compression", f)
	}
	out, err := io.ReadAll(io.LimitReader(r, MaxOutput+1))
	if err != nil {
		return nil, err
	}
	if len(out) > MaxOutput {
		return nil, fmt.Errorf("it decompresses to more than %d bytes", MaxOutput)
	}
	return out, nil
}
