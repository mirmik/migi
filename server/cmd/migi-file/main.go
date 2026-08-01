package main

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
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

type agentConfig struct {
	Endpoint       string `json:"endpoint"`
	Token          string `json:"token"`
	TLSFingerprint string `json:"tls_fingerprint"`
}

type fileClient struct {
	http  *http.Client
	token string
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "migi-file:", err)
		os.Exit(1)
	}
}

func run() error {
	endpoint := flag.String("endpoint", "http://127.0.0.1:8787", "trusted local Migi endpoint")
	configPath := flag.String("config", "", "agent JSON configuration for authenticated HTTPS access")
	source := flag.String("source", "", "agent name recorded for uploads")
	mimeType := flag.String("type", "", "upload MIME type (guessed from extension by default)")
	output := flag.String("output", "", "download destination (defaults to shared filename)")
	flag.Parse()
	base, client, err := configureClient(*endpoint, *configPath)
	if err != nil {
		return err
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
		return put(client, base, args[1], *source, *mimeType)
	case "list":
		if len(args) != 1 {
			return errors.New("list takes no arguments")
		}
		return list(client, base)
	case "get":
		if len(args) != 2 {
			return errors.New("get requires exactly one file ID")
		}
		return get(client, base, args[1], *output)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func configureClient(endpointText, configPath string) (*url.URL, *fileClient, error) {
	if configPath == "" {
		base, err := url.Parse(strings.TrimRight(endpointText, "/"))
		if err != nil || base.Scheme != "http" || base.Host == "" || base.User != nil ||
			base.Path != "" || base.RawQuery != "" || base.Fragment != "" {
			return nil, nil, errors.New("endpoint must be a trusted HTTP URL without a path")
		}
		return base, &fileClient{http: noRedirectClient()}, nil
	}
	config, pin, err := loadAgentConfig(configPath)
	if err != nil {
		return nil, nil, err
	}
	base, _ := url.Parse(config.Endpoint)
	base.Path = ""
	return base, &fileClient{http: pinnedClient(pin), token: config.Token}, nil
}

func loadAgentConfig(path string) (agentConfig, [sha256.Size]byte, error) {
	var config agentConfig
	var pin [sha256.Size]byte
	file, err := os.Open(path)
	if err != nil {
		return config, pin, fmt.Errorf("open agent config: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 16<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return config, pin, fmt.Errorf("decode agent config: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return config, pin, errors.New("agent config must contain one JSON object")
	}
	endpoint, err := url.Parse(config.Endpoint)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil ||
		endpoint.Path != "/v1/agent-events" || endpoint.RawQuery != "" || endpoint.Fragment != "" ||
		!strings.HasPrefix(config.Token, "migi_at_") || len(config.Token) > 256 {
		return config, pin, errors.New("agent config has invalid endpoint or token")
	}
	normalized := strings.ReplaceAll(strings.TrimSpace(config.TLSFingerprint), ":", "")
	raw, err := hex.DecodeString(normalized)
	if err != nil || len(raw) != sha256.Size {
		return config, pin, errors.New("agent config has invalid TLS SHA-256 fingerprint")
	}
	copy(pin[:], raw)
	return config, pin, nil
}

func noRedirectClient() *http.Client {
	return &http.Client{
		Timeout: 20 * time.Minute,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("file endpoint redirects are not allowed")
		},
	}
}

func pinnedClient(pin [sha256.Size]byte) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true, // Exact leaf-certificate pinning is performed below.
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return errors.New("server did not present a certificate")
			}
			actual := sha256.Sum256(state.PeerCertificates[0].Raw)
			if actual != pin {
				return errors.New("server TLS certificate fingerprint mismatch")
			}
			return nil
		},
	}
	client := noRedirectClient()
	client.Transport = transport
	return client
}

func (c *fileClient) request(method, target string, body io.Reader) (*http.Request, error) {
	request, err := http.NewRequest(method, target, body)
	if err == nil && c.token != "" {
		request.Header.Set("Authorization", "Bearer "+c.token)
	}
	return request, err
}

func put(client *fileClient, base *url.URL, path, source, contentType string) error {
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
	request, err := client.request(http.MethodPost, endpoint(base, "/v1/files"), file)
	if err != nil {
		return err
	}
	request.ContentLength = info.Size()
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("X-Migi-Filename", info.Name())
	if source != "" {
		request.Header.Set("X-Migi-Source", source)
	}
	response, err := client.http.Do(request)
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

func list(client *fileClient, base *url.URL) error {
	request, err := client.request(http.MethodGet, endpoint(base, "/v1/files"), nil)
	if err != nil {
		return err
	}
	response, err := client.http.Do(request)
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

func get(client *fileClient, base *url.URL, id, output string) error {
	if len(id) != 32 || strings.Trim(id, "0123456789abcdef") != "" {
		return errors.New("file ID must be 32 lowercase hexadecimal digits")
	}
	if output == "" {
		request, err := client.request(http.MethodGet, endpoint(base, "/v1/files/"+id), nil)
		if err != nil {
			return err
		}
		response, err := client.http.Do(request)
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
	request, err := client.request(http.MethodGet, endpoint(base, "/v1/files/"+id+"/content"), nil)
	if err != nil {
		return err
	}
	response, err := client.http.Do(request)
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
