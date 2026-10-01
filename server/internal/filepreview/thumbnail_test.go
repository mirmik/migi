package filepreview

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/png"
	"io"
	"testing"
)

func TestThumbnailSizeCacheAndEviction(t *testing.T) {
	var original bytes.Buffer
	if err := png.Encode(&original, image.NewRGBA(image.Rect(0, 0, 1600, 1000))); err != nil {
		t.Fatal(err)
	}
	var cache Cache
	opens := 0
	open := func() (io.ReadCloser, error) { opens++; return io.NopCloser(bytes.NewReader(original.Bytes())), nil }
	body, err := cache.Get(t.Context(), "first", open)
	if err != nil {
		t.Fatal(err)
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil || format != "jpeg" || config.Width != 320 || config.Height != 200 || len(body) > MaxBytes {
		t.Fatalf("thumbnail: %+v %s %d %v", config, format, len(body), err)
	}
	_, err = cache.Get(t.Context(), "first", open)
	if err != nil || opens != 2 {
		t.Fatalf("cache reopened source: %d %v", opens, err)
	}
	for i := 0; i < maxCacheEntries; i++ {
		if _, err := cache.Get(t.Context(), fmt.Sprint(i), open); err != nil {
			t.Fatal(err)
		}
	}
	if len(cache.entries) != maxCacheEntries || cache.bytes > maxCacheBytes {
		t.Fatal("unbounded cache")
	}
	if _, present := cache.entries["first"]; present {
		t.Fatal("old thumbnail not evicted")
	}
}

func TestRejectsCorruptAndEnormousImagesBeforeDecode(t *testing.T) {
	// A valid PNG header with huge dimensions must be rejected before a full
	// decoder can allocate its pixel buffer.
	var huge bytes.Buffer
	huge.WriteString("\x89PNG\r\n\x1a\n")
	header := append([]byte("IHDR"), make([]byte, 13)...)
	binary.BigEndian.PutUint32(header[4:8], 1<<20)
	binary.BigEndian.PutUint32(header[8:12], 1<<20)
	header[12], header[13] = 8, 6
	_ = binary.Write(&huge, binary.BigEndian, uint32(13))
	huge.Write(header)
	_ = binary.Write(&huge, binary.BigEndian, crc32.ChecksumIEEE(header))
	for _, body := range [][]byte{[]byte("broken"), []byte("<svg></svg>"), huge.Bytes()} {
		var cache Cache
		opens := 0
		_, err := cache.Get(t.Context(), "bad", func() (io.ReadCloser, error) {
			opens++
			return io.NopCloser(bytes.NewReader(body)), nil
		})
		if !errors.Is(err, ErrUnsupported) || opens != 1 {
			t.Fatalf("unsafe input accepted: %v, opens=%d", err, opens)
		}
	}
}

func TestCancelledRequestDoesNotOpenSource(t *testing.T) {
	var cache Cache
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := cache.Get(ctx, "cancelled", func() (io.ReadCloser, error) { t.Fatal("cancelled request opened source"); return nil, nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}
