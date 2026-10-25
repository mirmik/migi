package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const maxAudioMoovBytes = 8 << 20

// MP4 sample offsets remain valid when an unselected audio 'trak' is replaced
// by a same-size 'free' box. Only box types and the selected track's enabled
// flags change; video/audio samples, file size and byte ranges stay untouched.
// Fragmented MP4 is excluded because moof boxes also reference track IDs.

var errAudioContainer = errors.New("unsupported MP4 audio layout")
var browserAudioSlots = make(chan struct{}, 1)

type browserAudioTrack struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Language    string `json:"language,omitempty"`
	Codec       string `json:"codec"`
	boxOffset   int64
	flagsOffset int64
	flags       [3]byte
}
type mp4Box struct {
	offset, size, header int64
	kind                 string
}

func (b mp4Box) payload() int64 { return b.offset + b.header }
func mp4BoxHeader(data []byte, offset, end int64) (mp4Box, error) {
	if len(data) < 8 || offset < 0 || end-offset < 8 {
		return mp4Box{}, errAudioContainer
	}
	size := int64(binary.BigEndian.Uint32(data[:4]))
	header := int64(8)
	if size == 1 {
		if len(data) < 16 {
			return mp4Box{}, errAudioContainer
		}
		raw := binary.BigEndian.Uint64(data[8:16])
		if raw > uint64(end-offset) {
			return mp4Box{}, errAudioContainer
		}
		size = int64(raw)
		header = 16
	} else if size == 0 {
		size = end - offset
	}
	if size < header || size > end-offset {
		return mp4Box{}, errAudioContainer
	}
	return mp4Box{offset, size, header, string(data[4:8])}, nil
}
func mp4Children(data []byte, start, end int64) ([]mp4Box, error) {
	if start < 0 || end > int64(len(data)) || start > end {
		return nil, errAudioContainer
	}
	var boxes []mp4Box
	for start < end {
		if len(boxes) >= 4096 {
			return nil, errAudioContainer
		}
		b, err := mp4BoxHeader(data[start:end], start, end)
		if err != nil {
			return nil, err
		}
		boxes = append(boxes, b)
		start += b.size
	}
	return boxes, nil
}
func mp4Child(data []byte, parent mp4Box, kind string) (mp4Box, error) {
	boxes, err := mp4Children(data, parent.payload(), parent.offset+parent.size)
	if err != nil {
		return mp4Box{}, err
	}
	for _, b := range boxes {
		if b.kind == kind {
			return b, nil
		}
	}
	return mp4Box{}, errAudioContainer
}
func mp4Field(data []byte, b mp4Box, relative, size int64) ([]byte, error) {
	pos := b.payload() + relative
	if relative < 0 || size < 0 || pos > b.offset+b.size-size {
		return nil, errAudioContainer
	}
	return data[pos : pos+size], nil
}
func mp4AudioTracks(reader io.ReaderAt, size int64) ([]browserAudioTrack, error) {
	var moov mp4Box
	for offset, count := int64(0), 0; offset < size; count++ {
		if count >= 128 {
			return nil, errAudioContainer
		}
		header := make([]byte, min(int64(16), size-offset))
		if _, err := reader.ReadAt(header, offset); err != nil {
			return nil, err
		}
		b, err := mp4BoxHeader(header, offset, size)
		if err != nil {
			return nil, err
		}
		if b.kind == "moov" {
			moov = b
			break
		}
		offset += b.size
	}
	if moov.size == 0 || moov.size > maxAudioMoovBytes {
		return nil, errAudioContainer
	}
	data := make([]byte, moov.size)
	if _, err := reader.ReadAt(data, moov.offset); err != nil {
		return nil, err
	}
	boxes, err := mp4Children(data, moov.header, moov.size)
	if err != nil {
		return nil, err
	}
	if _, err := mp4Child(data, mp4Box{size: moov.size, header: moov.header}, "mvhd"); err != nil {
		return nil, err
	}
	tracks := make([]browserAudioTrack, 0)
	seen := make(map[uint32]bool)
	for _, trak := range boxes {
		if trak.kind == "mvex" {
			return nil, errAudioContainer
		} // Fragment track IDs cannot be removed in-place.
		if trak.kind != "trak" {
			continue
		}
		mdia, err := mp4Child(data, trak, "mdia")
		if err != nil {
			return nil, err
		}
		hdlr, err := mp4Child(data, mdia, "hdlr")
		if err != nil {
			return nil, err
		}
		handler, err := mp4Field(data, hdlr, 8, 4)
		if err != nil {
			return nil, err
		}
		if string(handler) != "soun" {
			continue
		}
		if len(tracks) >= 32 {
			return nil, errAudioContainer
		}
		tkhd, err := mp4Child(data, trak, "tkhd")
		if err != nil {
			return nil, err
		}
		full, err := mp4Field(data, tkhd, 0, 4)
		if err != nil {
			return nil, err
		}
		idOffset := int64(12)
		if full[0] == 1 {
			idOffset = 20
		} else if full[0] != 0 {
			return nil, errAudioContainer
		}
		idBytes, err := mp4Field(data, tkhd, idOffset, 4)
		if err != nil {
			return nil, err
		}
		id := binary.BigEndian.Uint32(idBytes)
		if id == 0 || seen[id] {
			return nil, errAudioContainer
		}
		seen[id] = true
		mdhd, err := mp4Child(data, mdia, "mdhd")
		if err != nil {
			return nil, err
		}
		version, err := mp4Field(data, mdhd, 0, 1)
		if err != nil {
			return nil, err
		}
		langOffset := int64(20)
		if version[0] == 1 {
			langOffset = 32
		} else if version[0] != 0 {
			return nil, errAudioContainer
		}
		langBytes, err := mp4Field(data, mdhd, langOffset, 2)
		if err != nil {
			return nil, err
		}
		packed := binary.BigEndian.Uint16(langBytes)
		language := subtitleLanguage(string([]byte{byte(packed>>10&31) + 0x60, byte(packed>>5&31) + 0x60, byte(packed&31) + 0x60}))
		minf, err := mp4Child(data, mdia, "minf")
		if err != nil {
			return nil, err
		}
		stbl, err := mp4Child(data, minf, "stbl")
		if err != nil {
			return nil, err
		}
		stsd, err := mp4Child(data, stbl, "stsd")
		if err != nil {
			return nil, err
		}
		sample, err := mp4Field(data, stsd, 4, 12)
		if err != nil {
			return nil, err
		}
		if binary.BigEndian.Uint32(sample[:4]) != 1 {
			return nil, errAudioContainer
		}
		codec := string(sample[8:12])
		label := map[string]string{"ru": "Русский", "en": "Английский", "ja": "Японский", "uk": "Украинский", "fr": "Французский", "de": "Немецкий"}[language]
		if label == "" {
			label = language
		}
		if label == "" {
			label = fmt.Sprintf("Дорожка %d", len(tracks)+1)
		}
		tracks = append(tracks, browserAudioTrack{ID: "track-" + strconv.FormatUint(uint64(id), 10), Label: label, Language: language, Codec: codec, boxOffset: moov.offset + trak.offset + 4, flagsOffset: moov.offset + tkhd.payload() + 1, flags: [3]byte{full[1], full[2], full[3]}})
	}
	return tracks, nil
}

type audioBytePatch struct {
	offset int64
	data   []byte
}

func audioPatches(tracks []browserAudioTrack, id string) ([]audioBytePatch, error) {
	var patches []audioBytePatch
	found := false
	for _, track := range tracks {
		if track.ID == id {
			flags := track.flags
			flags[2] |= 3
			patches = append(patches, audioBytePatch{track.flagsOffset, flags[:]})
			found = true
		} else {
			patches = append(patches, audioBytePatch{track.boxOffset, []byte("free")})
		}
	}
	if !found {
		return nil, os.ErrNotExist
	}
	return patches, nil
}
func patchAudioBytes(data []byte, offset int64, patches []audioBytePatch) {
	for _, patch := range patches {
		start := max(offset, patch.offset)
		end := min(offset+int64(len(data)), patch.offset+int64(len(patch.data)))
		if start < end {
			copy(data[start-offset:end-offset], patch.data[start-patch.offset:end-patch.offset])
		}
	}
}

type audioPatchedReader struct {
	reader  io.ReaderAt
	patches []audioBytePatch
}

func (r audioPatchedReader) ReadAt(p []byte, offset int64) (int, error) {
	n, err := r.reader.ReadAt(p, offset)
	patchAudioBytes(p[:n], offset, r.patches)
	return n, err
}

type audioPatchedWriter struct {
	writer  io.Writer
	patches []audioBytePatch
	offset  int64
}

func (w *audioPatchedWriter) Write(p []byte) (int, error) {
	data := p
	for _, patch := range w.patches {
		if patch.offset < w.offset+int64(len(p)) && patch.offset+int64(len(patch.data)) > w.offset {
			data = append([]byte(nil), p...)
			patchAudioBytes(data, w.offset, w.patches)
			break
		}
	}
	n, err := w.writer.Write(data)
	w.offset += int64(n)
	return n, err
}

type originAudioReader struct {
	store  *mediaStore
	ctx    context.Context
	record mediaStoredObject
}

func (r originAudioReader) ReadAt(p []byte, offset int64) (int, error) {
	length := int64(len(p))
	if length == 0 {
		return 0, nil
	}
	if offset < 0 || offset > r.record.Size-length || length > maxAudioMoovBytes {
		return 0, errAudioContainer
	}
	request, stream, err := r.store.waitOriginStream(r.ctx, r.record, &originByteRange{1, offset, length})
	if err != nil {
		return 0, err
	}
	stop := context.AfterFunc(r.ctx, func() { r.store.completeOriginRequest(request, context.Cause(r.ctx)) })
	defer stop()
	if stream.Range == nil {
		r.store.completeOriginRequest(request, errMediaOriginRejected)
		return 0, errMediaOriginRejected
	}
	data, err := io.ReadAll(io.LimitReader(stream.Body, length+1))
	if err != nil || int64(len(data)) != length || fmt.Sprintf("%x", sha256.Sum256(data)) != stream.Digest {
		r.store.completeOriginRequest(request, errMediaOriginRejected)
		return 0, errMediaOriginRejected
	}
	r.store.completeOriginRequest(request, nil)
	return copy(p, data), nil
}
func browserAudioMIME(mime string) bool { return mime == "video/mp4" || mime == "video/quicktime" }
func (s *mediaStore) browserAudioTracks(ctx context.Context, record mediaStoredObject) ([]browserAudioTrack, error) {
	s.audioMu.Lock()
	cached, ok := s.audioCache[record.SHA256]
	s.audioMu.Unlock()
	if ok {
		return cached, nil
	}
	if !browserAudioMIME(record.MIME) {
		return nil, errAudioContainer
	}
	select {
	case browserAudioSlots <- struct{}{}:
		defer func() { <-browserAudioSlots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	var reader io.ReaderAt
	if record.RemoteOrigin == nil {
		file, err := os.Open(s.browserPath(record))
		if err != nil {
			return nil, err
		}
		defer file.Close()
		reader = file
	} else {
		reader = originAudioReader{s, ctx, record}
	}
	tracks, err := mp4AudioTracks(reader, record.Size)
	if err != nil {
		return nil, err
	}
	s.audioMu.Lock()
	if len(s.audioCache) >= 32 {
		s.audioCache = nil
	}
	if s.audioCache == nil {
		s.audioCache = make(map[string][]browserAudioTrack)
	}
	s.audioCache[record.SHA256] = tracks
	s.audioMu.Unlock()
	return tracks, nil
}
func (s *mediaStore) browserAudio(w http.ResponseWriter, r *http.Request) {
	record, err := s.browserRecord(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if !isVideoMIME(record.MIME) {
		http.Error(w, "Звук доступен только для видео", 415)
		return
	}
	if r.Method == http.MethodHead {
		w.Header().Set("Content-Type", "application/json")
		return
	}
	if !browserAudioMIME(record.MIME) {
		writeJSON(w, 200, map[string]any{"tracks": []browserAudioTrack{}, "supported": false})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	tracks, err := s.browserAudioTracks(ctx, record)
	if errors.Is(err, errAudioContainer) {
		writeJSON(w, 200, map[string]any{"tracks": []browserAudioTrack{}, "supported": false})
		return
	}
	if err != nil {
		http.Error(w, "Не удалось прочитать звуковые дорожки. Повторите попытку.", 503)
		return
	}
	writeJSON(w, 200, map[string]any{"tracks": tracks, "supported": true})
}
func requestedAudio(r *http.Request) (string, error) {
	values, ok := r.URL.Query()["audio"]
	if !ok {
		return "", nil
	}
	if len(values) != 1 || !strings.HasPrefix(values[0], "track-") {
		return "", errAudioContainer
	}
	number, err := strconv.ParseUint(strings.TrimPrefix(values[0], "track-"), 10, 32)
	if err != nil || number == 0 || values[0] != "track-"+strconv.FormatUint(number, 10) {
		return "", errAudioContainer
	}
	return values[0], nil
}
