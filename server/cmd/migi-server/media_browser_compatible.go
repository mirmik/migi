package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"mime"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const browserCompatBudget = int64(8 << 30)
const browserCompatProfile = "aac-v1"

var browserCompatSlots = make(chan struct{}, 1)

type browserCompatStatus struct {
	State    string `json:"state"`
	Progress int    `json:"progress,omitempty"`
	Message  string `json:"message,omitempty"`
}
type browserCompatJob struct {
	status  browserCompatStatus
	touched time.Time
	cancel  context.CancelFunc
	done    chan struct{}
}
type browserCompatManifest struct {
	SourceSHA string `json:"source_sha"`
	Profile   string `json:"profile"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
}

func (s *mediaStore) compatPaths(record mediaStoredObject) (string, string) {
	key := record.SHA256 + "-" + browserCompatProfile
	return filepath.Join(s.root, ".browser-compatible", key+".mp4"), filepath.Join(s.root, ".browser-compatible", key+".json")
}
func (s *mediaStore) compatibleRecord(record mediaStoredObject) (mediaStoredObject, error) {
	path, manifestPath := s.compatPaths(record)
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return record, err
	}
	var m browserCompatManifest
	if json.Unmarshal(data, &m) != nil || m.SourceSHA != record.SHA256 || m.Profile != browserCompatProfile || !mediaSHA256Pattern.MatchString(m.SHA256) || m.Size <= 0 || m.Size > browserCompatBudget {
		return record, errors.New("invalid compatible metadata")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != m.Size {
		return record, os.ErrNotExist
	}
	_ = os.Chtimes(path, time.Now(), time.Now())
	record.browserPath = path
	record.RemoteOrigin = nil
	record.MIME = "video/mp4"
	record.Name += ".mp4"
	record.Size = m.Size
	record.SHA256 = m.SHA256
	return record, nil
}
func (s *mediaStore) browserRecord(r *http.Request) (mediaStoredObject, error) {
	record, err := s.getRecord(r.PathValue("mediaID"), time.Now().UTC())
	if err != nil {
		return record, err
	}
	if values, ok := r.URL.Query()["compatible"]; ok {
		if len(values) != 1 || values[0] != "1" || r.PathValue("kind") != "video" || !isVideoMIME(record.MIME) {
			return record, os.ErrNotExist
		}
		return s.compatibleRecord(record)
	}
	return record, nil
}
func (s *mediaStore) browserPath(record mediaStoredObject) string {
	if record.browserPath != "" {
		return record.browserPath
	}
	return s.blobPath(record.ID)
}

func (s *mediaStore) browserPrepare(w http.ResponseWriter, r *http.Request) {
	record, err := s.getRecord(r.PathValue("mediaID"), time.Now().UTC())
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if !isVideoMIME(record.MIME) {
		http.Error(w, "Подготовка доступна только для видео", 415)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodHead {
		w.Header().Set("Content-Type", "application/json")
		return
	}
	if _, err := s.compatibleRecord(record); err == nil {
		writeJSON(w, 200, browserCompatStatus{State: "ready"})
		return
	}
	if r.Method == http.MethodGet {
		s.compatMu.Lock()
		job := s.compatJobs[record.SHA256]
		result := browserCompatStatus{State: "idle"}
		if job != nil {
			job.touched = time.Now()
			result = job.status
		}
		s.compatMu.Unlock()
		writeJSON(w, 200, result)
		return
	}
	var input struct {
		SupportedAudio []string `json:"supported_audio"`
	}
	contentType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if contentType != "application/json" {
		http.Error(w, "Ожидается JSON", http.StatusUnsupportedMediaType)
		return
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF || len(input.SupportedAudio) > 16 {
		http.Error(w, "Некорректные параметры браузера", 400)
		return
	}
	supported := make(map[string]bool)
	for _, codec := range input.SupportedAudio {
		supported[codec] = true
	}
	// WebM has browser-native Opus/Vorbis. Ordinary MP4 can be checked without
	// reading or decoding its samples. Other containers get a compatible view.
	direct := record.MIME == "video/webm"
	if browserAudioMIME(record.MIME) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		tracks, loadErr := s.browserAudioTracks(ctx, record)
		cancel()
		if loadErr == nil {
			direct = true
			for _, track := range tracks {
				if !supported[track.Codec] {
					direct = false
				}
			}
		} else if errors.Is(loadErr, errAudioContainer) {
			direct = true
		}
	}
	if direct {
		writeJSON(w, 200, browserCompatStatus{State: "direct"})
		return
	}
	s.compatMu.Lock()
	if s.compatJobs == nil {
		s.compatJobs = make(map[string]*browserCompatJob)
	}
	for key, job := range s.compatJobs {
		if job.status.State == "error" && time.Since(job.touched) > time.Minute {
			delete(s.compatJobs, key)
		}
	}
	if job := s.compatJobs[record.SHA256]; job != nil && job.status.State != "error" {
		job.touched = time.Now()
		status := job.status
		s.compatMu.Unlock()
		writeJSON(w, 202, status)
		return
	}
	if previous := s.compatJobs[record.SHA256]; previous != nil && previous.status.State == "error" {
		delete(s.compatJobs, record.SHA256)
	}
	if len(s.compatJobs) >= 4 {
		s.compatMu.Unlock()
		writeJSON(w, 202, browserCompatStatus{State: "busy", Message: "Подготовка других видео. Ожидайте…"})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	job := &browserCompatJob{status: browserCompatStatus{State: "queued"}, touched: time.Now(), cancel: cancel, done: make(chan struct{})}
	s.compatJobs[record.SHA256] = job
	s.compatMu.Unlock()
	go s.prepareCompatible(ctx, record, job)
	writeJSON(w, 202, browserCompatStatus{State: "queued"})
}
func (s *mediaStore) compatUpdate(job *browserCompatJob, status browserCompatStatus) {
	s.compatMu.Lock()
	job.status = status
	s.compatMu.Unlock()
}
func (s *mediaStore) prepareCompatible(ctx context.Context, record mediaStoredObject, job *browserCompatJob) {
	defer close(job.done)
	defer job.cancel()
	monitorDone := make(chan struct{})
	defer close(monitorDone)
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-monitorDone:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.compatMu.Lock()
				abandoned := time.Since(job.touched) > 45*time.Second
				s.compatMu.Unlock()
				if abandoned {
					job.cancel()
					return
				}
			}
		}
	}()
	fail := func(message string, err error) {
		s.compatUpdate(job, browserCompatStatus{State: "error", Message: message})
		slog.Warn("browser compatible media preparation failed", "media_id", record.ID, "error", err)
	}
	select {
	case browserCompatSlots <- struct{}{}:
		defer func() { <-browserCompatSlots }()
	case <-ctx.Done():
		fail("Подготовка прервана. Повторите запуск видео.", ctx.Err())
		return
	}
	s.compatUpdate(job, browserCompatStatus{State: "processing"})
	path, _ := s.compatPaths(record)
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0700); err != nil {
		fail("Не удалось создать кэш видео.", err)
		return
	}
	available, err := s.compatReserve(directory, record.Size)
	if err != nil {
		fail("В кэше недостаточно места. Повторите позже.", err)
		return
	}
	source, protocols, cleanup, err := s.compatSource(ctx, record)
	if err != nil {
		fail("Не удалось подключиться к хранилищу.", err)
		return
	}
	defer cleanup()
	probe, err := probeCompatible(ctx, source, protocols)
	if err != nil {
		fail("Не удалось прочитать видео. Проверьте хранилище и ffprobe на сервере.", err)
		return
	}
	duration, durationErr := strconv.ParseFloat(probe.Format.Duration, 64)
	if durationErr != nil || duration <= 0 || math.IsNaN(duration) || math.IsInf(duration, 0) {
		fail("Не удалось определить длительность видео.", durationErr)
		return
	}
	var video *compatProbeStream
	var audio, subtitles []compatProbeStream
	for i := range probe.Streams {
		stream := probe.Streams[i]
		switch stream.Type {
		case "video":
			if video == nil {
				copy := stream
				video = &copy
			}
		case "audio":
			audio = append(audio, stream)
		case "subtitle":
			switch stream.Codec {
			case "subrip", "ass", "ssa", "webvtt", "mov_text", "text":
				if len(subtitles) < maxVideoSubtitles {
					subtitles = append(subtitles, stream)
				}
			}
		}
	}
	if video == nil || video.Codec != "h264" || len(audio) == 0 || len(audio) > 16 {
		fail("Автоподготовка поддерживает видео H.264 со звуком. Этот файл требует другого способа обработки.", errAudioContainer)
		return
	}
	temporary, err := os.CreateTemp(directory, ".part-*.mp4")
	if err != nil {
		fail("Не удалось создать файл подготовки.", err)
		return
	}
	temporaryPath := temporary.Name()
	_ = temporary.Close()
	defer os.Remove(temporaryPath)
	args := []string{"-nostdin", "-v", "error", "-y", "-threads", "1", "-filter_threads", "1", "-protocol_whitelist", protocols, "-format_whitelist", "mov,matroska,webm,avi", "-i", source, "-map", fmt.Sprint("0:", video.Index)}
	for _, stream := range audio {
		args = append(args, "-map", fmt.Sprint("0:", stream.Index))
	}
	for _, stream := range subtitles {
		args = append(args, "-map", fmt.Sprint("0:", stream.Index))
	}
	args = append(args, "-c:v", "copy", "-c:a", "aac", "-b:a", "192k", "-ac", "2", "-c:s", "mov_text")
	for index, stream := range audio {
		if stream.Codec == "aac" {
			args = append(args, fmt.Sprintf("-c:a:%d", index), "copy")
		}
	}
	args = append(args, "-movflags", "+faststart", "-max_muxing_queue_size", "1024", "-fs", strconv.FormatInt(available, 10), "-progress", "pipe:1", "-stats_period", "0.5", temporaryPath)
	command := exec.CommandContext(ctx, "ffmpeg", args...)
	progress, err := command.StdoutPipe()
	if err != nil {
		fail("Не удалось запустить подготовку.", err)
		return
	}
	if err = command.Start(); err != nil {
		fail("Не удалось запустить ffmpeg на сервере.", err)
		return
	}
	scanner := bufio.NewScanner(progress)
	for scanner.Scan() {
		key, value, ok := strings.Cut(scanner.Text(), "=")
		if ok && key == "out_time_us" && duration > 0 && !math.IsInf(duration, 0) {
			micros, _ := strconv.ParseInt(value, 10, 64)
			percent := max(0, min(99, int(float64(micros)/1e6/duration*100)))
			s.compatUpdate(job, browserCompatStatus{State: "processing", Progress: percent})
		}
	}
	if err = command.Wait(); err != nil {
		fail("Не удалось подготовить звук. Проверьте хранилище и повторите запуск.", err)
		return
	}
	converted, err := probeCompatible(ctx, temporaryPath, "file,pipe")
	if err != nil {
		fail("Подготовленный файл не прошёл проверку.", err)
		return
	}
	convertedDuration, _ := strconv.ParseFloat(converted.Format.Duration, 64)
	if math.IsNaN(convertedDuration) || math.IsInf(convertedDuration, 0) || convertedDuration < duration-2 {
		fail("Подготовка не завершилась полностью: проверьте свободное место.", errors.New("truncated compatible video"))
		return
	}
	file, err := os.Open(temporaryPath)
	if err != nil {
		fail("Подготовленный файл недоступен.", err)
		return
	}
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	_ = file.Close()
	if err != nil || size > available || size <= 0 {
		fail("Не удалось проверить подготовленный файл.", err)
		return
	}
	manifest := browserCompatManifest{record.SHA256, browserCompatProfile, size, fmt.Sprintf("%x", hash.Sum(nil))}
	if ctx.Err() != nil {
		fail("Подготовка прервана. Повторите запуск видео.", ctx.Err())
		return
	}
	_, manifestPath := s.compatPaths(record)
	data, _ := json.Marshal(manifest)
	if err = os.Rename(temporaryPath, path); err == nil {
		err = os.WriteFile(manifestPath+".tmp", data, 0600)
	}
	if err == nil {
		err = os.Rename(manifestPath+".tmp", manifestPath)
	}
	if err != nil {
		_ = os.Remove(path)
		_ = os.Remove(manifestPath + ".tmp")
		fail("Не удалось сохранить подготовленный файл.", err)
		return
	}
	s.compatMu.Lock()
	delete(s.compatJobs, record.SHA256)
	s.compatMu.Unlock()
	slog.Info("browser compatible media ready", "media_id", record.ID, "size", size)
}

func (s *mediaStore) compatReserve(directory string, reserve int64) (int64, error) {
	reserve = min(browserCompatBudget, reserve+(128<<20))
	entries, err := os.ReadDir(directory)
	if err != nil {
		return 0, err
	}
	type cached struct {
		path string
		info os.FileInfo
	}
	var files []cached
	var total int64
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if strings.HasPrefix(entry.Name(), ".part-") && time.Since(info.ModTime()) > time.Hour {
			_ = os.Remove(filepath.Join(directory, entry.Name()))
			continue
		}
		if strings.HasSuffix(entry.Name(), ".mp4") {
			files = append(files, cached{filepath.Join(directory, entry.Name()), info})
			total += info.Size()
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].info.ModTime().Before(files[j].info.ModTime()) })
	for _, file := range files {
		if total+reserve <= browserCompatBudget {
			break
		}
		if time.Since(file.info.ModTime()) < 30*time.Minute {
			continue
		}
		if os.Remove(file.path) == nil {
			total -= file.info.Size()
			_ = os.Remove(strings.TrimSuffix(file.path, ".mp4") + ".json")
		}
	}
	if total+reserve > browserCompatBudget {
		return 0, errors.New("compatible cache full")
	}
	return browserCompatBudget - total, nil
}

type compatProbeStream struct {
	Index int    `json:"index"`
	Type  string `json:"codec_type"`
	Codec string `json:"codec_name"`
}
type compatProbe struct {
	Streams []compatProbeStream `json:"streams"`
	Format  struct {
		Duration string `json:"duration"`
	} `json:"format"`
}

func probeCompatible(ctx context.Context, source, protocols string) (compatProbe, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	data, err := subtitleCommand(ctx, "ffprobe", nil, 256<<10, "-v", "error", "-threads", "1", "-protocol_whitelist", protocols, "-format_whitelist", "mov,matroska,webm,avi", "-show_entries", "stream=index,codec_type,codec_name:format=duration", "-of", "json", source)
	var probe compatProbe
	if err != nil {
		return probe, err
	}
	err = json.Unmarshal(data, &probe)
	return probe, err
}
func (s *mediaStore) compatSource(ctx context.Context, record mediaStoredObject) (string, string, func(), error) {
	if record.RemoteOrigin == nil {
		return s.browserPath(record), "file,pipe", func() {}, nil
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", "", nil, err
	}
	token := make([]byte, 16)
	if _, err = rand.Read(token); err != nil {
		listener.Close()
		return "", "", nil, err
	}
	path := "/" + hex.EncodeToString(token)
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path || r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}
		r.SetPathValue("kind", "video")
		r.SetPathValue("mediaID", record.ID)
		s.browserContent(w, r)
	})}
	go func() { _ = server.Serve(listener) }()
	var once sync.Once
	cleanup := func() { once.Do(func() { _ = server.Close() }) }
	return "http://" + listener.Addr().String() + path, "http,tcp,pipe", cleanup, nil
}
