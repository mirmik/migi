package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// One conversion at a time bounds subprocess memory even with multiple viewers.
var browserSubtitleSlots = make(chan struct{}, 1)

type browserSubtitle struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Language string `json:"language,omitempty"`
	Default  bool   `json:"default,omitempty"`
}

type subtitleBuffer struct {
	buffer bytes.Buffer
	limit  int
}

func (b *subtitleBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.buffer.Len() {
		return 0, errors.New("subtitle output too large")
	}
	return b.buffer.Write(p)
}

func subtitleCommand(ctx context.Context, name string, input io.Reader, limit int, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = input
	output := &subtitleBuffer{limit: limit}
	cmd.Stdout = output
	// Do not expose private blob paths or untrusted decoder diagnostics to clients.
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return output.buffer.Bytes(), nil
}

func (s *mediaStore) browserSubtitles(w http.ResponseWriter, r *http.Request) {
	record, err := s.browserRecord(r)
	if err != nil {
		http.Error(w, "Видео недоступно", http.StatusNotFound)
		return
	}
	if !isVideoMIME(record.MIME) {
		http.Error(w, "Субтитры доступны только для видео", http.StatusUnsupportedMediaType)
		return
	}
	// HEAD must not fetch origin content or start a converter.
	if r.Method == http.MethodHead {
		w.Header().Set("Content-Type", "application/json")
		if r.PathValue("subtitleID") != "" {
			w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
		}
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	select {
	case browserSubtitleSlots <- struct{}{}:
		defer func() { <-browserSubtitleSlots }()
	case <-ctx.Done():
		http.Error(w, "Сервис субтитров занят. Повторите попытку.", http.StatusServiceUnavailable)
		return
	}
	tracks, err := s.browserSubtitleTracks(ctx, record)
	if err != nil {
		http.Error(w, "Не удалось прочитать субтитры. Проверьте ffprobe на сервере.", http.StatusServiceUnavailable)
		return
	}
	trackID := r.PathValue("subtitleID")
	if trackID == "" {
		writeJSON(w, http.StatusOK, tracks)
		return
	}
	var selected *browserSubtitle
	for i := range tracks {
		if tracks[i].ID == trackID {
			selected = &tracks[i]
			break
		}
	}
	if selected == nil {
		http.NotFound(w, r)
		return
	}
	var data []byte
	if strings.HasPrefix(trackID, "sidecar-") {
		id := strings.TrimPrefix(trackID, "sidecar-")
		sub, readErr := s.getRecord(id, time.Now().UTC())
		if readErr != nil || !isSubtitleMIME(sub.MIME) || sub.Size > maxSubtitleBytes {
			http.NotFound(w, r)
			return
		}
		data, err = s.browserSubtitleBytes(ctx, sub)
		if err == nil {
			format := map[string]string{"text/vtt": "webvtt", "application/x-subrip": "srt", "text/x-ssa": "ass"}[sub.MIME]
			data, err = subtitleCommand(ctx, "ffmpeg", bytes.NewReader(data), int(maxSubtitleBytes), "-nostdin", "-v", "error", "-threads", "1", "-protocol_whitelist", "pipe", "-f", format, "-i", "pipe:0", "-map", "0:s:0", "-f", "webvtt", "pipe:1")
		}
	} else {
		stream := strings.TrimPrefix(trackID, "stream-")
		data, err = subtitleCommand(ctx, "ffmpeg", nil, int(maxSubtitleBytes), "-nostdin", "-v", "error", "-threads", "1", "-protocol_whitelist", "file,pipe", "-format_whitelist", "mov,matroska,webm", "-i", s.browserPath(record), "-map", "0:"+stream, "-f", "webvtt", "pipe:1")
	}
	if err != nil {
		http.Error(w, "Не удалось загрузить субтитры. Повторите попытку.", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}

func (s *mediaStore) browserSubtitleTracks(ctx context.Context, record mediaStoredObject) ([]browserSubtitle, error) {
	tracks := make([]browserSubtitle, 0)
	for _, ref := range record.Subtitles {
		tracks = append(tracks, browserSubtitle{ID: "sidecar-" + ref.ID, Label: ref.Label, Language: subtitleLanguage(ref.Language), Default: ref.Default})
	}
	// Origins already declare separate subtitle objects. Never download a remote
	// movie just to discover embedded tracks; local copies can be inspected in place.
	if record.RemoteOrigin != nil || len(tracks) > 0 {
		return tracks, nil
	}
	data, err := subtitleCommand(ctx, "ffprobe", nil, 256<<10, "-v", "error", "-threads", "1", "-protocol_whitelist", "file,pipe", "-format_whitelist", "mov,matroska,webm", "-select_streams", "s", "-show_entries", "stream=index,codec_name:stream_tags=language,title,handler_name:stream_disposition=default", "-of", "json", s.browserPath(record))
	if err != nil {
		return nil, err
	}
	var probe struct {
		Streams []struct {
			Index int    `json:"index"`
			Codec string `json:"codec_name"`
			Tags  struct {
				Language string `json:"language"`
				Title    string `json:"title"`
			} `json:"tags"`
			Disposition struct {
				Default int `json:"default"`
			} `json:"disposition"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, err
	}
	for _, stream := range probe.Streams {
		switch stream.Codec {
		case "subrip", "ass", "ssa", "mov_text", "webvtt", "text":
		default:
			continue
		}
		if len(tracks) >= maxVideoSubtitles {
			break
		}
		language := subtitleLanguage(stream.Tags.Language)
		label := stream.Tags.Title
		if label == "" {
			label = map[string]string{"ru": "Русские", "en": "English", "ja": "日本語", "uk": "Українські"}[language]
		}
		if label == "" {
			label = language
		}
		if label == "" {
			label = "Субтитры"
		}
		if !validMediaText(label, 128) {
			label = "Субтитры"
		}
		tracks = append(tracks, browserSubtitle{ID: "stream-" + strconv.Itoa(stream.Index), Label: label, Language: language, Default: stream.Disposition.Default != 0})
	}
	return tracks, nil
}

func subtitleLanguage(language string) string {
	language = strings.ToLower(language)
	if mapped := map[string]string{"rus": "ru", "eng": "en", "jpn": "ja", "ukr": "uk", "fra": "fr", "fre": "fr", "deu": "de", "ger": "de", "spa": "es", "ita": "it", "por": "pt", "zho": "zh", "chi": "zh", "kor": "ko"}[language]; mapped != "" {
		return mapped
	}
	if language == "und" || !subtitleLanguagePattern.MatchString(language) {
		return ""
	}
	return language
}

func (s *mediaStore) browserSubtitleBytes(ctx context.Context, record mediaStoredObject) ([]byte, error) {
	var data []byte
	if record.RemoteOrigin == nil {
		file, err := os.Open(s.browserPath(record))
		if err != nil {
			return nil, err
		}
		defer file.Close()
		data, err = io.ReadAll(io.LimitReader(file, maxSubtitleBytes+1))
		if err != nil {
			return nil, err
		}
	} else {
		request, stream, err := s.waitOriginStream(ctx, record, &originByteRange{1, 0, record.Size})
		if err != nil {
			return nil, err
		}
		stop := context.AfterFunc(ctx, func() { s.completeOriginRequest(request, context.Cause(ctx)) })
		defer stop()
		data, err = io.ReadAll(io.LimitReader(stream.Body, maxSubtitleBytes+1))
		if err != nil || int64(len(data)) != record.Size || fmt.Sprintf("%x", sha256.Sum256(data)) != record.SHA256 {
			s.completeOriginRequest(request, errMediaOriginRejected)
			return nil, errMediaOriginRejected
		}
		s.completeOriginRequest(request, nil)
	}
	if int64(len(data)) != record.Size || int64(len(data)) > maxSubtitleBytes || fmt.Sprintf("%x", sha256.Sum256(data)) != record.SHA256 {
		return nil, errMediaOriginRejected
	}
	return data, nil
}
