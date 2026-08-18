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
	"strings"
	"time"
)

const maxResponseBytes = 1 << 20

type mediaObject struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Title     string    `json:"title"`
	Artist    string    `json:"artist,omitempty"`
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

type playClient struct {
	http  *http.Client
	token string
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "migi-play:", err)
		os.Exit(1)
	}
}

func run() error {
	endpointFlag := flag.String("endpoint", "http://127.0.0.1:8787", "trusted local Migi endpoint")
	configPath := flag.String("config", "", "agent JSON configuration (auto-detected by default)")
	source := flag.String("source", "", "agent name recorded for local requests")
	mimeType := flag.String("type", "", "upload MIME type (guessed from extension by default)")
	title := flag.String("title", "", "track title for put")
	artist := flag.String("artist", "", "track artist for put")
	deviceID := flag.String("device", "", "paired device ID (empty targets every paired phone)")
	playlistName := flag.String("name", "Migi playlist", "playlist name for queue or play")
	flag.Parse()
	var endpointExplicit, configExplicit bool
	flag.Visit(func(option *flag.Flag) {
		switch option.Name {
		case "endpoint":
			endpointExplicit = true
		case "config":
			configExplicit = true
		}
	})
	resolvedConfig, err := resolveConfigPath(*configPath, endpointExplicit, configExplicit)
	if err != nil {
		return err
	}
	base, client, err := configureClient(*endpointFlag, resolvedConfig)
	if err != nil {
		return err
	}
	args := flag.Args()
	if len(args) == 0 {
		return errors.New("usage: migi-play [flags] put PATH | list | queue MEDIA_ID... | play PATH...")
	}
	switch args[0] {
	case "put":
		if len(args) != 2 {
			return errors.New("put requires exactly one audio file path")
		}
		object, err := put(client, base, args[1], *source, *mimeType, *title, *artist)
		if err == nil {
			printObject(object)
		}
		return err
	case "list":
		if len(args) != 1 {
			return errors.New("list takes no arguments")
		}
		return list(client, base)
	case "queue":
		if len(args) < 2 {
			return errors.New("queue requires at least one media ID")
		}
		return queue(client, base, *playlistName, *deviceID, *source, args[1:])
	case "play":
		if len(args) < 2 {
			return errors.New("play requires at least one audio file path")
		}
		if *title != "" || *artist != "" {
			return errors.New("-title and -artist apply only to put; play derives metadata from filenames")
		}
		ids := make([]string, 0, len(args)-1)
		for _, path := range args[1:] {
			object, err := put(client, base, path, *source, *mimeType, "", "")
			if err != nil {
				return fmt.Errorf("upload %s: %w", path, err)
			}
			printObject(object)
			ids = append(ids, object.ID)
		}
		return queue(client, base, *playlistName, *deviceID, *source, ids)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func resolveConfigPath(configPath string, endpointExplicit, configExplicit bool) (string, error) {
	if endpointExplicit && configExplicit {
		return "", errors.New("-endpoint and -config cannot be used together")
	}
	if configExplicit {
		if configPath == "" {
			return "", errors.New("-config requires a non-empty path")
		}
		return configPath, nil
	}
	if endpointExplicit {
		return "", nil
	}
	if configured, exists := os.LookupEnv("MIGI_AGENT_CONFIG"); exists {
		if configured == "" {
			return "", errors.New("MIGI_AGENT_CONFIG is empty")
		}
		return configured, nil
	}
	configDirectory, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate user configuration: %w", err)
	}
	candidate := filepath.Join(configDirectory, "migi", "agent.json")
	info, err := os.Stat(candidate)
	if err == nil {
		if !info.Mode().IsRegular() {
			return "", errors.New("default agent config is not a regular file")
		}
		return candidate, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect default agent config: %w", err)
	}
	return "", nil
}

func configureClient(endpointText, configPath string) (*url.URL, *playClient, error) {
	if configPath == "" {
		base, err := url.Parse(strings.TrimRight(endpointText, "/"))
		if err != nil || base.Scheme != "http" || base.Host == "" || base.User != nil ||
			base.Path != "" || base.RawQuery != "" || base.Fragment != "" {
			return nil, nil, errors.New("endpoint must be a trusted HTTP URL without a path")
		}
		return base, &playClient{http: noRedirectClient()}, nil
	}
	config, pin, err := loadAgentConfig(configPath)
	if err != nil {
		return nil, nil, err
	}
	base, _ := url.Parse(config.Endpoint)
	base.Path = ""
	return base, &playClient{http: pinnedClient(pin), token: config.Token}, nil
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
			return errors.New("media endpoint redirects are not allowed")
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

func (c *playClient) request(method, target string, body io.Reader) (*http.Request, error) {
	request, err := http.NewRequest(method, target, body)
	if err == nil && c.token != "" {
		request.Header.Set("Authorization", "Bearer "+c.token)
	}
	return request, err
}

func put(
	client *playClient,
	base *url.URL,
	path, source, contentType, title, artist string,
) (mediaObject, error) {
	var object mediaObject
	file, err := os.Open(path)
	if err != nil {
		return object, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
		return object, errors.New("upload must be a non-empty regular file")
	}
	if contentType == "" {
		contentType = audioMIME(filepath.Ext(info.Name()))
	}
	if !strings.HasPrefix(strings.ToLower(contentType), "audio/") {
		return object, errors.New("audio MIME type is required; use -type when the extension is unknown")
	}
	request, err := client.request(http.MethodPost, endpoint(base, "/v1/media"), file)
	if err != nil {
		return object, err
	}
	request.ContentLength = info.Size()
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("X-Migi-Filename", info.Name())
	if source != "" {
		request.Header.Set("X-Migi-Source", source)
	}
	if title != "" {
		request.Header.Set("X-Migi-Title", title)
	}
	if artist != "" {
		request.Header.Set("X-Migi-Artist", artist)
	}
	response, err := client.http.Do(request)
	if err != nil {
		return object, err
	}
	defer response.Body.Close()
	body, err := readResponse(response)
	if err != nil {
		return object, err
	}
	if response.StatusCode != http.StatusCreated {
		return object, fmt.Errorf("server returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, &object); err != nil {
		return mediaObject{}, err
	}
	return object, nil
}

func audioMIME(extension string) string {
	extension = strings.ToLower(extension)
	switch extension {
	case ".mp3":
		return "audio/mpeg"
	case ".opus":
		return "audio/opus"
	case ".ogg", ".oga":
		return "audio/ogg"
	case ".flac":
		return "audio/flac"
	case ".m4a", ".mp4":
		return "audio/mp4"
	case ".aac":
		return "audio/aac"
	case ".wav":
		return "audio/wav"
	default:
		return mime.TypeByExtension(extension)
	}
}

func list(client *playClient, base *url.URL) error {
	request, err := client.request(http.MethodGet, endpoint(base, "/v1/media"), nil)
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
	var objects []mediaObject
	if err := json.Unmarshal(body, &objects); err != nil {
		return err
	}
	for _, object := range objects {
		printObject(object)
	}
	return nil
}

func queue(client *playClient, base *url.URL, name, deviceID, source string, mediaIDs []string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("playlist name must not be empty")
	}
	if len(mediaIDs) == 0 || len(mediaIDs) > 32 {
		return errors.New("queue requires 1-32 media IDs")
	}
	for _, id := range mediaIDs {
		if !validMediaID(id) {
			return fmt.Errorf("invalid media ID %q", id)
		}
	}
	body, err := json.Marshal(struct {
		Name     string   `json:"name"`
		DeviceID string   `json:"device_id,omitempty"`
		MediaIDs []string `json:"media_ids"`
	}{Name: name, DeviceID: deviceID, MediaIDs: mediaIDs})
	if err != nil {
		return err
	}
	request, err := client.request(http.MethodPost, endpoint(base, "/v1/playback/queue"), strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	if source != "" {
		request.Header.Set("X-Migi-Source", source)
	}
	response, err := client.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	responseBody, err := readResponse(response)
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusCreated {
		return fmt.Errorf("server returned %s: %s", response.Status, strings.TrimSpace(string(responseBody)))
	}
	var event struct {
		ID uint64 `json:"id"`
	}
	if err := json.Unmarshal(responseBody, &event); err != nil {
		return err
	}
	fmt.Printf("queued event %d\n", event.ID)
	return nil
}

func printObject(object mediaObject) {
	credit := object.Title
	if object.Artist != "" {
		credit += " — " + object.Artist
	}
	fmt.Printf("%s\t%d\t%s\t%s\t%s\n", object.ID, object.Size, object.ExpiresAt.Local().Format(time.RFC3339), credit, object.Name)
}

func validMediaID(id string) bool {
	return len(id) == 32 && strings.Trim(id, "0123456789abcdef") == ""
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
