package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const maxResponseBytes = 1 << 20

type sharedFile struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	MIME      string    `json:"mime"`
	Size      int64     `json:"size"`
	SHA256    string    `json:"sha256"`
	Source    string    `json:"source"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "migi-file:", err)
		os.Exit(1)
	}
}

func run() error {
	endpoint := flag.String("endpoint", "http://127.0.0.1:8787", "trusted local Migi endpoint")
	source := flag.String("source", "", "agent name recorded for uploads")
	mimeType := flag.String("type", "", "upload MIME type (guessed from extension by default)")
	output := flag.String("output", "", "download destination (defaults to shared filename)")
	flag.Parse()
	base, err := url.Parse(strings.TrimRight(*endpoint, "/"))
	if err != nil || base.Scheme != "http" || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return errors.New("endpoint must be a trusted local HTTP URL")
	}
	args := flag.Args()
	if len(args) == 0 {
		return errors.New("usage: migi-file [flags] put PATH | list | get ID")
	}
	switch args[0] {
	case "put":
		if len(args) != 2 {
			return errors.New("put requires exactly one file path")
		}
		return put(base, args[1], *source, *mimeType)
	case "list":
		if len(args) != 1 {
			return errors.New("list takes no arguments")
		}
		return list(base)
	case "get":
		if len(args) != 2 {
			return errors.New("get requires exactly one file ID")
		}
		return get(base, args[1], *output)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func put(base *url.URL, path, source, contentType string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
		return errors.New("upload must be a non-empty regular file")
	}
	if contentType == "" {
		contentType = mime.TypeByExtension(strings.ToLower(filepath.Ext(info.Name())))
		if contentType == "" {
			contentType = "application/octet-stream"
		}
	}
	request, err := http.NewRequest(http.MethodPost, endpoint(base, "/v1/files"), file)
	if err != nil {
		return err
	}
	request.ContentLength = info.Size()
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("X-Migi-Filename", info.Name())
	if source != "" {
		request.Header.Set("X-Migi-Source", source)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := readResponse(response)
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusCreated {
		return fmt.Errorf("server returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	var shared sharedFile
	if err := json.Unmarshal(body, &shared); err != nil {
		return err
	}
	fmt.Printf("%s\t%s\t%d bytes\t%s\n", shared.ID, shared.Name, shared.Size, shared.ExpiresAt.Local().Format(time.RFC3339))
	return nil
}

func list(base *url.URL) error {
	response, err := http.Get(endpoint(base, "/v1/files"))
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := readResponse(response)
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	var files []sharedFile
	if err := json.Unmarshal(body, &files); err != nil {
		return err
	}
	for _, file := range files {
		fmt.Printf("%s\t%d\t%s\t%s\t%s\n", file.ID, file.Size, file.Source, file.ExpiresAt.Local().Format(time.RFC3339), file.Name)
	}
	return nil
}

func get(base *url.URL, id, output string) error {
	if len(id) != 32 || strings.Trim(id, "0123456789abcdef") != "" {
		return errors.New("file ID must be 32 lowercase hexadecimal digits")
	}
	if output == "" {
		response, err := http.Get(endpoint(base, "/v1/files/"+id))
		if err != nil {
			return err
		}
		body, readErr := readResponse(response)
		response.Body.Close()
		if readErr != nil {
			return readErr
		}
		if response.StatusCode != http.StatusOK {
			return fmt.Errorf("server returned %s: %s", response.Status, strings.TrimSpace(string(body)))
		}
		var metadata sharedFile
		if err := json.Unmarshal(body, &metadata); err != nil {
			return err
		}
		output = filepath.Base(metadata.Name)
	}
	destination, err := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create destination: %w", err)
	}
	ok := false
	defer func() {
		destination.Close()
		if !ok {
			os.Remove(output)
		}
	}()
	response, err := http.Get(endpoint(base, "/v1/files/"+id+"/content"))
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
		return fmt.Errorf("server returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	expectedSize, err := strconv.ParseInt(response.Header.Get("Content-Length"), 10, 64)
	if err != nil || expectedSize <= 0 {
		return errors.New("server returned an invalid content length")
	}
	expectedDigest := response.Header.Get("X-Content-SHA256")
	if len(expectedDigest) != 64 {
		return errors.New("server returned an invalid content digest")
	}
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(destination, hash), io.LimitReader(response.Body, expectedSize+1))
	if err != nil {
		return err
	}
	if written != expectedSize {
		return fmt.Errorf("downloaded %d bytes, expected %d", written, expectedSize)
	}
	actualDigest := fmt.Sprintf("%x", hash.Sum(nil))
	if !strings.EqualFold(actualDigest, expectedDigest) {
		return errors.New("downloaded file digest does not match server metadata")
	}
	if err := destination.Sync(); err != nil {
		return err
	}
	ok = true
	fmt.Println(output)
	return nil
}

func endpoint(base *url.URL, path string) string {
	copy := *base
	copy.Path = strings.TrimRight(copy.Path, "/") + path
	return copy.String()
}

func readResponse(response *http.Response) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxResponseBytes {
		return nil, errors.New("server response exceeds limit")
	}
	return body, nil
}
