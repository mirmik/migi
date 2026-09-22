package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const maxOriginRegistryBytes = 4 << 20

var errOriginSourceChanged = errors.New("media origin source changed")

type originFileSnapshot struct {
	Path               string
	Name               string
	Size               int64
	SHA256             string
	Device             uint64
	Inode              uint64
	ModifiedAtUnixNano int64
}

type originRegistry struct {
	Version int                            `json:"version"`
	Entries map[string]originRegistryEntry `json:"entries"`
}

type originRegistryEntry struct {
	Path               string `json:"path"`
	Name               string `json:"name"`
	MIME               string `json:"mime"`
	Size               int64  `json:"size"`
	SHA256             string `json:"sha256"`
	Device             uint64 `json:"device"`
	Inode              uint64 `json:"inode"`
	ModifiedAtUnixNano int64  `json:"modified_at_unix_nano"`
}

type originFetchRange struct {
	Version int   `json:"version"`
	Offset  int64 `json:"offset"`
	Length  int64 `json:"length"`
}

type originFetchRequest struct {
	Range   *originFetchRange `json:"range,omitempty"`
	ID      string            `json:"id"`
	MediaID string            `json:"media_id"`
	Name    string            `json:"name"`
	MIME    string            `json:"mime"`
	Size    int64             `json:"size"`
	SHA256  string            `json:"sha256"`
}

func resolveOriginRegistryPath(explicit string) (string, error) {
	if explicit == "" {
		if configured := os.Getenv("MIGI_ORIGIN_REGISTRY"); configured != "" {
			explicit = configured
		} else if stateRoot := os.Getenv("XDG_STATE_HOME"); stateRoot != "" {
			explicit = filepath.Join(stateRoot, "migi", "media-origin.json")
		} else {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", fmt.Errorf("locate media origin registry: %w", err)
			}
			explicit = filepath.Join(home, ".local", "state", "migi", "media-origin.json")
		}
	}
	absolute, err := filepath.Abs(explicit)
	if err != nil {
		return "", fmt.Errorf("resolve media origin registry: %w", err)
	}
	return filepath.Clean(absolute), nil
}

func inspectOriginFile(path string, maximumBytes int64) (originFileSnapshot, error) {
	var snapshot originFileSnapshot
	absolute, err := filepath.Abs(path)
	if err != nil {
		return snapshot, err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return snapshot, err
	}
	file, err := os.Open(canonical)
	if err != nil {
		return snapshot, err
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Size() <= 0 {
		return snapshot, errors.New("media origin must be a non-empty regular file")
	}
	if maximumBytes > 0 && before.Size() > maximumBytes {
		return snapshot, fmt.Errorf("file exceeds the %d byte limit", maximumBytes)
	}
	identity, ok := before.Sys().(*syscall.Stat_t)
	if !ok {
		return snapshot, errors.New("media origin filesystem identity is unavailable")
	}
	hash := sha256.New()
	written, err := io.Copy(hash, file)
	if err != nil {
		return snapshot, err
	}
	after, err := file.Stat()
	if err != nil || written != before.Size() || !os.SameFile(before, after) ||
		after.Size() != before.Size() || after.ModTime() != before.ModTime() {
		return snapshot, errOriginSourceChanged
	}
	return originFileSnapshot{
		Path: filepath.Clean(canonical), Name: before.Name(), Size: before.Size(),
		SHA256: hex.EncodeToString(hash.Sum(nil)), Device: uint64(identity.Dev),
		Inode: uint64(identity.Ino), ModifiedAtUnixNano: before.ModTime().UnixNano(),
	}, nil
}

func saveOriginRegistry(path string, items []originMediaRegistration, objects []mediaObject) error {
	if len(items) != len(objects) {
		return errors.New("media origin registry input does not match server response")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create media origin state directory: %w", err)
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open media origin registry lock: %w", err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock media origin registry: %w", err)
	}
	registry, err := loadOriginRegistryOptional(path)
	if err != nil {
		return err
	}
	for index, item := range items {
		object := objects[index]
		registry.Entries[object.ID] = originRegistryEntry{
			Path: item.Path, Name: item.Name, MIME: item.MIME, Size: item.Size,
			SHA256: item.SHA256, Device: item.Device, Inode: item.Inode,
			ModifiedAtUnixNano: item.ModifiedAtUnixNano,
		}
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".media-origin-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	encoder := json.NewEncoder(temporary)
	if err := encoder.Encode(registry); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return nil
}

func loadOriginRegistryOptional(path string) (originRegistry, error) {
	registry := originRegistry{Version: 1, Entries: make(map[string]originRegistryEntry)}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return registry, nil
	}
	if err != nil {
		return registry, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 ||
		info.Size() <= 0 || info.Size() > maxOriginRegistryBytes {
		return registry, errors.New("media origin registry must be a private regular file")
	}
	identity, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(identity.Uid) != os.Getuid() {
		return registry, errors.New("media origin registry must be owned by the current user")
	}
	decoder := json.NewDecoder(io.LimitReader(file, maxOriginRegistryBytes+1))
	if err := decoder.Decode(&registry); err != nil {
		return registry, fmt.Errorf("decode media origin registry: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return registry, errors.New("media origin registry must contain one JSON object")
	}
	if registry.Version != 1 || registry.Entries == nil {
		return registry, errors.New("media origin registry has an unsupported format")
	}
	return registry, nil
}

func serveMediaOrigin(client *playClient, base *url.URL, once bool) error {
	if once {
		return serveMediaOriginWorker(client, base, true)
	}
	results := make(chan error, 3)
	for range 3 {
		go func() { results <- serveMediaOriginWorker(client, base, false) }()
	}
	return <-results
}

func serveMediaOriginWorker(client *playClient, base *url.URL, once bool) error {
	if client.token == "" {
		return errors.New("origin requires an authenticated agent configuration")
	}
	for {
		job, err := pollMediaOrigin(client, base)
		if err != nil {
			if once {
				return err
			}
			fmt.Fprintln(os.Stderr, "migi-play origin:", err)
			time.Sleep(time.Second)
			continue
		}
		if job == nil {
			if once {
				return nil
			}
			continue
		}
		registry, err := loadOriginRegistryOptional(client.originRegistry)
		entry, exists := registry.Entries[job.MediaID]
		if err == nil && !exists {
			err = errors.New("requested media ID is absent from the origin registry")
		}
		if err == nil {
			err = uploadOriginMedia(client, base, *job, entry)
		}
		if err != nil {
			if errors.Is(err, errOriginSourceChanged) || !exists {
				_ = failOriginRequest(client, base, job.ID)
			}
			if once {
				return err
			}
			fmt.Fprintln(os.Stderr, "migi-play origin:", err)
			continue
		}
		fmt.Printf("streamed %s\n", job.MediaID)
		if once {
			return nil
		}
	}
}

func pollMediaOrigin(client *playClient, base *url.URL) (*originFetchRequest, error) {
	request, err := client.request(http.MethodGet, endpoint(base, "/v1/media/origin/requests"), nil)
	if err != nil {
		return nil, err
	}
	request.URL.RawQuery = "ranges=1"
	response, err := client.http.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, err := readResponse(response)
	if err != nil {
		return nil, err
	}
	if response.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	var job originFetchRequest
	if err := json.Unmarshal(body, &job); err != nil {
		return nil, err
	}
	if !validMediaID(job.ID) || !validMediaID(job.MediaID) || job.Size <= 0 ||
		len(job.SHA256) != 64 || !strings.HasPrefix(strings.ToLower(job.MIME), "audio/") && !strings.HasPrefix(strings.ToLower(job.MIME), "video/") && artworkMIME(filepath.Ext(job.Name)) == "" {
		return nil, errors.New("server returned an invalid media origin request")
	}
	if span := job.Range; span != nil && (span.Version != 1 || span.Offset < 0 || span.Offset >= job.Size || span.Length <= 0 || span.Length > job.Size-span.Offset) {
		return nil, errors.New("invalid origin range")
	}
	return &job, nil
}

func uploadOriginMedia(client *playClient, base *url.URL, job originFetchRequest, entry originRegistryEntry) error {
	if entry.Size != job.Size || entry.SHA256 != job.SHA256 || entry.MIME != job.MIME {
		return errOriginSourceChanged
	}
	canonical, err := filepath.EvalSymlinks(entry.Path)
	if err != nil || filepath.Clean(canonical) != entry.Path {
		return errOriginSourceChanged
	}
	file, err := os.Open(canonical)
	if err != nil {
		return fmt.Errorf("%w: %v", errOriginSourceChanged, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("%w: %v", errOriginSourceChanged, err)
	}
	identity, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Size() != entry.Size ||
		uint64(identity.Dev) != entry.Device || uint64(identity.Ino) != entry.Inode ||
		info.ModTime().UnixNano() != entry.ModifiedAtUnixNano {
		return errOriginSourceChanged
	}
	var source io.Reader = file
	length := job.Size
	rangeDigest := ""
	if span := job.Range; span != nil {
		if span.Version != 1 || span.Offset < 0 || span.Offset >= job.Size || span.Length <= 0 || span.Length > job.Size-span.Offset {
			return errors.New("invalid origin range")
		}
		length = span.Length
		source = io.NewSectionReader(file, span.Offset, length)
		if length <= 2<<20 {
			data, err := io.ReadAll(source)
			after, statErr := file.Stat()
			if err != nil || statErr != nil || int64(len(data)) != length || !info.ModTime().Equal(after.ModTime()) || info.Size() != after.Size() {
				return errOriginSourceChanged
			}
			digest := sha256.Sum256(data)
			rangeDigest = hex.EncodeToString(digest[:])
			source = bytes.NewReader(data)
		}
	}
	request, err := client.request(
		http.MethodPut,
		endpoint(base, "/v1/media/origin/requests/"+job.ID),
		source,
	)
	if err != nil {
		return err
	}
	request.ContentLength = length
	if span := job.Range; span != nil {
		request.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", span.Offset, span.Offset+span.Length-1, job.Size))
		if rangeDigest != "" {
			request.Header.Set("X-Range-SHA256", rangeDigest)
		}
	}
	request.Header.Set("Content-Type", job.MIME)
	response, err := client.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := readResponse(response)
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("server returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

func failOriginRequest(client *playClient, base *url.URL, requestID string) error {
	request, err := client.request(
		http.MethodPost,
		endpoint(base, "/v1/media/origin/requests/"+requestID+"/fail"),
		strings.NewReader(""),
	)
	if err != nil {
		return err
	}
	request.ContentLength = 0
	response, err := client.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent && response.StatusCode != http.StatusNotFound {
		body, _ := readResponse(response)
		return fmt.Errorf("server returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	return nil
}
