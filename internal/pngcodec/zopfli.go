package pngcodec

import (
	"bytes"
	"compress/zlib"
	"io"

	"github.com/foobaz/go-zopfli/zopfli"
)

// The encoder's longest-match cache alone takes 24 bytes per input byte.
const maxZopfliImageData = 8 << 20

func recompressWithZopfli(data []byte) []byte {
	first, last, compressed := imageData(data)
	raw := inflateImageData(compressed, maxZopfliImageData)
	if len(raw) == 0 {
		return data
	}

	settings := zopfli.DefaultOptions()
	settings.NumIterations = 3
	// Extra iterations pay off on small streams without slowing large photos.
	if len(raw) <= 256<<10 {
		settings.NumIterations = 15
	}
	var output bytes.Buffer
	if err := zopfli.ZlibCompress(&settings, raw, &output); err != nil || output.Len()+12 >= last-first {
		return data
	}
	if !bytes.Equal(raw, inflateImageData(bytes.NewReader(output.Bytes()), len(raw))) {
		return data
	}
	return replaceImageData(data, first, last, output.Bytes())
}

func inflateImageData(compressed io.Reader, limit int) []byte {
	reader, err := zlib.NewReader(compressed)
	if err != nil {
		return nil
	}
	defer reader.Close()
	raw, err := io.ReadAll(io.LimitReader(reader, int64(limit)+1))
	if err != nil || len(raw) > limit {
		return nil
	}
	return raw
}
