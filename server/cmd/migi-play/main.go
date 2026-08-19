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

const (
	maxResponseBytes = 1 << 20
	maxArtworkBytes  = int64(8 << 20)
	maxTrackBytes    = int64(256 << 20)
)

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

type savedPlaylist struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	ArtworkMediaID string    `json:"artwork_media_id,omitempty"`
	MediaIDs       []string  `json:"media_ids"`
	Source         string    `json:"source"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type originMediaRegistration struct {
	Path               string `json:"-"`
	Name               string `json:"name"`
	Title              string `json:"title,omitempty"`
	Artist             string `json:"artist,omitempty"`
	MIME               string `json:"mime"`
	Size               int64  `json:"size"`
	SHA256             string `json:"sha256"`
	Device             uint64 `json:"-"`
	Inode              uint64 `json:"-"`
	ModifiedAtUnixNano int64  `json:"-"`
}

type agentConfig struct {
	Endpoint       string `json:"endpoint"`
	Token          string `json:"token"`
	TLSFingerprint string `json:"tls_fingerprint"`
}

type playClient struct {
	http           *http.Client
	token          string
	originRegistry string
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
	lazy := flag.Bool("lazy", false, "register a remote origin and transfer tracks only on demand")
	originRegistry := flag.String("origin-registry", "", "private media origin registry path")
	originOnce := flag.Bool("origin-once", false, "poll and serve at most one origin request")
	cover := flag.String("cover", "", "playlist cover image path for queue or play")
	artworkID := flag.String("artwork-id", "", "existing artwork media ID for queue or saved playlist")
	deviceID := flag.String("device", "", "paired device ID (empty targets every paired phone)")
	playlistName := flag.String("name", "Migi playlist", "playlist name for queue, play, save, or index")
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
		return errors.New("usage: migi-play [flags] put PATH | index PATH... | list | search QUERY | queue MEDIA_ID... | play PATH... | save MEDIA_ID... | playlists | start PLAYLIST_ID | forget PLAYLIST_ID | origin")
	}
	if *lazy || args[0] == "index" || args[0] == "origin" {
		client.originRegistry, err = resolveOriginRegistryPath(*originRegistry)
		if err != nil {
			return err
		}
	}
	if *cover != "" && *artworkID != "" {
		return errors.New("-cover and -artwork-id cannot be used together")
	}
	switch args[0] {
	case "origin":
		if len(args) != 1 {
			return errors.New("origin takes no operands")
		}
		return serveMediaOrigin(client, base, *originOnce)
	case "put":
		if *cover != "" || *artworkID != "" {
			return errors.New("-cover and -artwork-id do not apply to put")
		}
		if len(args) != 2 {
			return errors.New("put requires exactly one audio file path")
		}
		var object mediaObject
		var err error
		if *lazy {
			object, err = putOrigin(client, base, args[1], *source, *mimeType, *title, *artist)
		} else {
			object, err = put(client, base, args[1], *source, *mimeType, *title, *artist)
		}
		if err == nil {
			printObject(object)
		}
		return err
	case "index":
		if len(args) < 2 {
			return errors.New("index requires at least one audio file path")
		}
		if *artworkID != "" {
			return errors.New("-artwork-id does not apply to index")
		}
		if *title != "" || *artist != "" {
			return errors.New("-title and -artist apply only to put; index derives metadata from filenames")
		}
		return indexOrigin(client, base, *cover, *source, *mimeType, args[1:])
	case "list":
		if *cover != "" {
			return errors.New("-cover applies only to queue or play")
		}
		if len(args) != 1 {
			return errors.New("list takes no arguments")
		}
		if *lazy {
			return errors.New("-lazy does not apply to list")
		}
		return list(client, base)
	case "search":
		if *cover != "" || *artworkID != "" {
			return errors.New("-cover and -artwork-id do not apply to search")
		}
		if len(args) < 2 {
			return errors.New("search requires a query")
		}
		return search(client, base, strings.Join(args[1:], " "))
	case "queue":
		if len(args) < 2 {
			return errors.New("queue requires at least one media ID")
		}
		resolvedArtworkID, err := resolveArtwork(client, base, *cover, *artworkID, *source, *lazy)
		if err != nil {
			return err
		}
		return queue(client, base, *playlistName, *deviceID, *source, resolvedArtworkID, args[1:])
	case "play":
		if len(args) < 2 {
			return errors.New("play requires at least one audio file path")
		}
		if *title != "" || *artist != "" {
			return errors.New("-title and -artist apply only to put; play derives metadata from filenames")
		}
		if *lazy {
			return playOrigin(
				client, base, *playlistName, *deviceID, *source, *cover, *artworkID, *mimeType, args[1:],
			)
		}
		resolvedArtworkID, err := resolveArtwork(client, base, *cover, *artworkID, *source, false)
		if err != nil {
			return err
		}
		ids := make([]string, 0, len(args)-1)
		for _, path := range args[1:] {
			object, err := put(client, base, path, *source, *mimeType, titleFromPath(path), "")
			if err != nil {
				return fmt.Errorf("upload %s: %w", path, err)
			}
			printObject(object)
			ids = append(ids, object.ID)
		}
		return queue(client, base, *playlistName, *deviceID, *source, resolvedArtworkID, ids)
	case "save":
		if len(args) < 2 {
			return errors.New("save requires at least one media ID")
		}
		resolvedArtworkID, err := resolveArtwork(client, base, *cover, *artworkID, *source, *lazy)
		if err != nil {
			return err
		}
		return savePlaylist(client, base, *playlistName, *source, resolvedArtworkID, args[1:])
	case "playlists":
		if len(args) != 1 {
			return errors.New("playlists takes no arguments")
		}
		return listPlaylists(client, base)
	case "start":
		if len(args) != 2 {
			return errors.New("start requires exactly one saved playlist ID")
		}
		return startPlaylist(client, base, args[1], *deviceID, *source)
	case "forget":
		if len(args) != 2 {
			return errors.New("forget requires exactly one saved playlist ID")
		}
		return forgetPlaylist(client, base, args[1])
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func titleFromPath(path string) string {
	name := strings.TrimSpace(filepath.Base(path))
	title := strings.TrimSpace(strings.TrimSuffix(name, filepath.Ext(name)))
	if title == "" {
		return name
	}
	return title
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
	if contentType == "" {
		contentType = audioMIME(filepath.Ext(path))
	}
	if !strings.HasPrefix(strings.ToLower(contentType), "audio/") {
		return mediaObject{}, errors.New("audio MIME type is required; use -type when the extension is unknown")
	}
	return putMedia(client, base, path, source, contentType, title, artist, 0)
}

func putOrigin(
	client *playClient,
	base *url.URL,
	path, source, contentType, title, artist string,
) (mediaObject, error) {
	if contentType == "" {
		contentType = audioMIME(filepath.Ext(path))
	}
	if !strings.HasPrefix(strings.ToLower(contentType), "audio/") {
		return mediaObject{}, errors.New("audio MIME type is required; use -type when the extension is unknown")
	}
	input, err := makeOriginMediaRegistration(path, contentType, title, artist, 0)
	if err != nil {
		return mediaObject{}, err
	}
	objects, err := registerOriginMedia(client, base, source, []originMediaRegistration{input})
	if err != nil {
		return mediaObject{}, err
	}
	return objects[0], nil
}

func playOrigin(
	client *playClient,
	base *url.URL,
	name, deviceID, source, cover, existingArtworkID, contentType string,
	paths []string,
) error {
	inputs, err := makeOriginInputs(cover, contentType, paths)
	if err != nil {
		return err
	}
	objects, err := registerOriginMedia(client, base, source, inputs)
	if err != nil {
		return err
	}
	position := 0
	artworkID := existingArtworkID
	if cover != "" {
		artworkID = objects[0].ID
		position = 1
	}
	ids := make([]string, 0, len(paths))
	for _, object := range objects {
		printObject(object)
	}
	for _, object := range objects[position:] {
		ids = append(ids, object.ID)
	}
	return queue(client, base, name, deviceID, source, artworkID, ids)
}

func indexOrigin(
	client *playClient,
	base *url.URL,
	cover, source, contentType string,
	paths []string,
) error {
	inputs, err := makeOriginInputs(cover, contentType, paths)
	if err != nil {
		return err
	}
	objects, err := registerOriginMedia(client, base, source, inputs)
	if err != nil {
		return err
	}
	for _, object := range objects {
		printObject(object)
	}
	return nil
}

func makeOriginInputs(cover, contentType string, paths []string) ([]originMediaRegistration, error) {
	inputs := make([]originMediaRegistration, 0, len(paths)+1)
	if cover != "" {
		coverType := artworkMIME(filepath.Ext(cover))
		if coverType == "" {
			return nil, errors.New("cover must be JPEG, PNG, or WebP")
		}
		input, err := makeOriginMediaRegistration(cover, coverType, titleFromPath(cover), "", maxArtworkBytes)
		if err != nil {
			return nil, fmt.Errorf("register cover %s: %w", cover, err)
		}
		inputs = append(inputs, input)
	}
	for _, path := range paths {
		trackType := contentType
		if trackType == "" {
			trackType = audioMIME(filepath.Ext(path))
		}
		if !strings.HasPrefix(strings.ToLower(trackType), "audio/") {
			return nil, fmt.Errorf("register %s: audio MIME type is required; use -type when the extension is unknown", path)
		}
		input, err := makeOriginMediaRegistration(path, trackType, titleFromPath(path), "", 0)
		if err != nil {
			return nil, fmt.Errorf("register %s: %w", path, err)
		}
		inputs = append(inputs, input)
	}
	return inputs, nil
}

func prepareArtwork(
	client *playClient,
	base *url.URL,
	path, source string,
	lazy bool,
) (string, error) {
	if !lazy {
		return uploadArtwork(client, base, path, source)
	}
	if path == "" {
		return "", nil
	}
	contentType := artworkMIME(filepath.Ext(path))
	if contentType == "" {
		return "", errors.New("cover must be JPEG, PNG, or WebP")
	}
	input, err := makeOriginMediaRegistration(path, contentType, titleFromPath(path), "", maxArtworkBytes)
	if err != nil {
		return "", fmt.Errorf("register cover %s: %w", path, err)
	}
	objects, err := registerOriginMedia(client, base, source, []originMediaRegistration{input})
	if err != nil {
		return "", err
	}
	printObject(objects[0])
	return objects[0].ID, nil
}

func resolveArtwork(
	client *playClient,
	base *url.URL,
	path, existingID, source string,
	lazy bool,
) (string, error) {
	if existingID != "" {
		if !validMediaID(existingID) {
			return "", errors.New("-artwork-id must be a valid media ID")
		}
		return existingID, nil
	}
	return prepareArtwork(client, base, path, source, lazy)
}

func makeOriginMediaRegistration(
	path, contentType, title, artist string,
	maximumBytes int64,
) (originMediaRegistration, error) {
	if maximumBytes == 0 {
		maximumBytes = maxTrackBytes
	}
	snapshot, err := inspectOriginFile(path, maximumBytes)
	if err != nil {
		return originMediaRegistration{}, err
	}
	return originMediaRegistration{
		Path: snapshot.Path, Name: snapshot.Name, Title: title, Artist: artist,
		MIME: contentType, Size: snapshot.Size, SHA256: snapshot.SHA256,
		Device: snapshot.Device, Inode: snapshot.Inode,
		ModifiedAtUnixNano: snapshot.ModifiedAtUnixNano,
	}, nil
}

func registerOriginMedia(
	client *playClient,
	base *url.URL,
	source string,
	items []originMediaRegistration,
) ([]mediaObject, error) {
	if client.token == "" {
		return nil, errors.New("media origin registration requires an authenticated agent configuration")
	}
	body, err := json.Marshal(struct {
		Items []originMediaRegistration `json:"items"`
	}{Items: items})
	if err != nil {
		return nil, err
	}
	request, err := client.request(http.MethodPost, endpoint(base, "/v1/media/origin"), strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.http.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	responseBody, err := readResponse(response)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("server returned %s: %s", response.Status, strings.TrimSpace(string(responseBody)))
	}
	var objects []mediaObject
	if err := json.Unmarshal(responseBody, &objects); err != nil {
		return nil, err
	}
	if len(objects) != len(items) {
		return nil, errors.New("server returned a mismatched media origin manifest")
	}
	for _, object := range objects {
		if !validMediaID(object.ID) || object.Size <= 0 || len(object.SHA256) != 64 {
			return nil, errors.New("server returned invalid media origin metadata")
		}
	}
	if err := saveOriginRegistry(client.originRegistry, items, objects); err != nil {
		return nil, err
	}
	return objects, nil
}

func uploadArtwork(client *playClient, base *url.URL, path, source string) (string, error) {
	if path == "" {
		return "", nil
	}
	contentType := artworkMIME(filepath.Ext(path))
	if contentType == "" {
		return "", errors.New("cover must be JPEG, PNG, or WebP")
	}
	object, err := putMedia(
		client,
		base,
		path,
		source,
		contentType,
		titleFromPath(path),
		"",
		maxArtworkBytes,
	)
	if err != nil {
		return "", fmt.Errorf("upload cover %s: %w", path, err)
	}
	printObject(object)
	return object.ID, nil
}

func putMedia(
	client *playClient,
	base *url.URL,
	path, source, contentType, title, artist string,
	maximumBytes int64,
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
	if maximumBytes > 0 && info.Size() > maximumBytes {
		return object, fmt.Errorf("file exceeds the %d byte limit", maximumBytes)
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

func artworkMIME(extension string) string {
	switch strings.ToLower(extension) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	default:
		return ""
	}
}

func list(client *playClient, base *url.URL) error {
	return listMedia(client, base, "")
}

func search(client *playClient, base *url.URL, query string) error {
	query = strings.TrimSpace(query)
	if query == "" {
		return errors.New("search query must not be empty")
	}
	return listMedia(client, base, query)
}

func listMedia(client *playClient, base *url.URL, query string) error {
	target := endpoint(base, "/v1/media")
	if query != "" {
		parsed, _ := url.Parse(target)
		values := parsed.Query()
		values.Set("q", query)
		parsed.RawQuery = values.Encode()
		target = parsed.String()
	}
	request, err := client.request(http.MethodGet, target, nil)
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

func savePlaylist(
	client *playClient,
	base *url.URL,
	name, source, artworkMediaID string,
	mediaIDs []string,
) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("playlist name must not be empty")
	}
	if len(mediaIDs) == 0 || len(mediaIDs) > 32 {
		return errors.New("save requires 1-32 media IDs")
	}
	for _, id := range mediaIDs {
		if !validMediaID(id) {
			return fmt.Errorf("invalid media ID %q", id)
		}
	}
	body, err := json.Marshal(struct {
		Name           string   `json:"name"`
		ArtworkMediaID string   `json:"artwork_media_id,omitempty"`
		MediaIDs       []string `json:"media_ids"`
	}{Name: name, ArtworkMediaID: artworkMediaID, MediaIDs: mediaIDs})
	if err != nil {
		return err
	}
	request, err := client.request(http.MethodPost, endpoint(base, "/v1/playlists"), strings.NewReader(string(body)))
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
	var playlist savedPlaylist
	if err := json.Unmarshal(responseBody, &playlist); err != nil {
		return err
	}
	if !validMediaID(playlist.ID) {
		return errors.New("server returned an invalid saved playlist ID")
	}
	printPlaylist(playlist)
	return nil
}

func listPlaylists(client *playClient, base *url.URL) error {
	request, err := client.request(http.MethodGet, endpoint(base, "/v1/playlists"), nil)
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
	var playlists []savedPlaylist
	if err := json.Unmarshal(body, &playlists); err != nil {
		return err
	}
	for _, playlist := range playlists {
		printPlaylist(playlist)
	}
	return nil
}

func startPlaylist(client *playClient, base *url.URL, id, deviceID, source string) error {
	if !validMediaID(id) {
		return errors.New("invalid saved playlist ID")
	}
	body, err := json.Marshal(struct {
		DeviceID string `json:"device_id,omitempty"`
	}{DeviceID: deviceID})
	if err != nil {
		return err
	}
	request, err := client.request(http.MethodPost, endpoint(base, "/v1/playlists/"+id+"/queue"), strings.NewReader(string(body)))
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
	fmt.Printf("queued saved playlist event %d\n", event.ID)
	return nil
}

func forgetPlaylist(client *playClient, base *url.URL, id string) error {
	if !validMediaID(id) {
		return errors.New("invalid saved playlist ID")
	}
	request, err := client.request(http.MethodDelete, endpoint(base, "/v1/playlists/"+id), nil)
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
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("server returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	fmt.Printf("forgot saved playlist %s\n", id)
	return nil
}

func printPlaylist(playlist savedPlaylist) {
	fmt.Printf("%s\t%d tracks\t%s\t%s\n", playlist.ID, len(playlist.MediaIDs), playlist.Name, playlist.Source)
}

func queue(
	client *playClient,
	base *url.URL,
	name, deviceID, source, artworkMediaID string,
	mediaIDs []string,
) error {
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
		Name           string   `json:"name"`
		DeviceID       string   `json:"device_id,omitempty"`
		ArtworkMediaID string   `json:"artwork_media_id,omitempty"`
		MediaIDs       []string `json:"media_ids"`
	}{Name: name, DeviceID: deviceID, ArtworkMediaID: artworkMediaID, MediaIDs: mediaIDs})
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
	expiry := "persistent"
	if !object.ExpiresAt.IsZero() {
		expiry = object.ExpiresAt.Local().Format(time.RFC3339)
	}
	fmt.Printf("%s\t%d\t%s\t%s\t%s\n", object.ID, object.Size, expiry, credit, object.Name)
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
