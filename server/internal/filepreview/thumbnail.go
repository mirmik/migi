// Package filepreview produces bounded raster thumbnails for the shared inbox.
package filepreview

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"sync"

	_ "golang.org/x/image/bmp"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const Edge = 320
const MaxBytes = 256 << 10
const maxPixels = 16_000_000
const maxCacheBytes = 8 << 20
const maxCacheEntries = 128

var ErrUnsupported = errors.New("thumbnail unavailable for this image")

// Cache is shared by web and phone routes. Only one original is decoded at a
// time; cached JPEGs and decoded source dimensions both have fixed bounds.
type Cache struct {
	once    sync.Once
	gate    chan struct{}
	entries map[string][]byte
	order   []string
	bytes   int
}

func (c *Cache) Get(ctx context.Context, key string, open func() (io.ReadCloser, error)) ([]byte, error) {
	c.once.Do(func() {
		c.gate = make(chan struct{}, 1)
		c.entries = make(map[string][]byte)
	})
	select {
	case c.gate <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-c.gate }()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if body, ok := c.entries[key]; ok {
		for i, existing := range c.order {
			if existing == key {
				c.order = append(append(c.order[:i:i], c.order[i+1:]...), key)
				break
			}
		}
		return body, nil
	}
	body, err := render(open)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for len(c.order) >= maxCacheEntries || c.bytes+len(body) > maxCacheBytes {
		old := c.order[0]
		c.order = c.order[1:]
		c.bytes -= len(c.entries[old])
		delete(c.entries, old)
	}
	c.entries[key] = body
	c.order = append(c.order, key)
	c.bytes += len(body)
	return body, nil
}

func render(open func() (io.ReadCloser, error)) ([]byte, error) {
	reader, err := open()
	if err != nil {
		return nil, err
	}
	config, _, err := image.DecodeConfig(io.LimitReader(reader, 100<<20))
	reader.Close()
	if err != nil || config.Width <= 0 || config.Height <= 0 ||
		int64(config.Width) > maxPixels/int64(config.Height) {
		return nil, ErrUnsupported
	}
	reader, err = open()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	source, _, err := image.Decode(io.LimitReader(reader, 100<<20))
	if err != nil {
		return nil, ErrUnsupported
	}
	w, h := config.Width, config.Height
	if max(w, h) > Edge {
		if w >= h {
			h, w = max(1, h*Edge/w), Edge
		} else {
			w, h = max(1, w*Edge/h), Edge
		}
	}
	thumb := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(thumb, thumb.Bounds(), &image.Uniform{C: color.RGBA{R: 25, G: 34, B: 49, A: 255}}, image.Point{}, draw.Src)
	draw.ApproxBiLinear.Scale(thumb, thumb.Bounds(), source, source.Bounds(), draw.Over, nil)
	var output bytes.Buffer
	if err := jpeg.Encode(&output, thumb, &jpeg.Options{Quality: 78}); err != nil {
		return nil, err
	}
	if output.Len() > MaxBytes {
		return nil, ErrUnsupported
	}
	return output.Bytes(), nil
}
