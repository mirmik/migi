package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"strings"
	"time"
)

const maxResponseBytes = 64 << 10

type publisherConfig struct {
	Endpoint           string `json:"endpoint"`
	Token              string `json:"token"`
	LegacyPackageName  string `json:"package_name,omitempty"`
	LegacySignerSHA256 string `json:"signer_sha256,omitempty"`
	TLSFingerprint     string `json:"tls_fingerprint"`
}

type releaseMetadata struct {
	ReleaseNotes   string `json:"release_notes,omitempty"`
	SourceRevision string `json:"source_revision,omitempty"`
	BuildID        string `json:"build_id,omitempty"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "migi-publish:", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "", "publisher JSON configuration from the Migi admin panel")
	apkPath := flag.String("apk", "", "signed APK to publish")
	notes := flag.String("notes", "", "release notes")
	sourceRevision := flag.String("source-revision", "", "source revision")
	buildID := flag.String("build-id", "", "build identifier")
	idempotencyKey := flag.String("idempotency-key", "", "stable retry key; defaults to the APK SHA-256")
	flag.Parse()
	if *configPath == "" || *apkPath == "" {
		return errors.New("-config and -apk are required")
	}
	config, pin, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	apk, err := os.Open(*apkPath)
	if err != nil {
		return fmt.Errorf("open APK: %w", err)
	}
	defer apk.Close()
	stat, err := apk.Stat()
	if err != nil || !stat.Mode().IsRegular() || stat.Size() == 0 {
		return errors.New("APK must be a non-empty regular file")
	}
	digest, err := fileSHA256(apk)
	if err != nil {
		return err
	}
	digestText := hex.EncodeToString(digest[:])
	if *idempotencyKey == "" {
		*idempotencyKey = digestText
	}
	if len(*idempotencyKey) > 128 || strings.TrimSpace(*idempotencyKey) != *idempotencyKey {
		return errors.New("idempotency key must be 1-128 non-surrounding-whitespace characters")
	}
	metadata := releaseMetadata{
		ReleaseNotes:   *notes,
		SourceRevision: *sourceRevision,
		BuildID:        *buildID,
	}
	body, contentType := multipartBody(apk, metadata)
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, config.Endpoint, body)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+config.Token)
	request.Header.Set("Idempotency-Key", *idempotencyKey)
	request.Header.Set("Content-Type", contentType)
	client := pinnedClient(pin)
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("upload release: %w", err)
	}
	defer response.Body.Close()
	result, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if len(result) > maxResponseBytes {
		return errors.New("server response exceeds limit")
	}
	if response.StatusCode != http.StatusCreated {
		return fmt.Errorf("server returned %s: %s", response.Status, strings.TrimSpace(string(result)))
	}
	var pretty bytes.Buffer
	if json.Indent(&pretty, result, "", "  ") == nil {
		result = append(pretty.Bytes(), '\n')
	}
	_, err = os.Stdout.Write(result)
	return err
}

func loadConfig(path string) (publisherConfig, [sha256.Size]byte, error) {
	var config publisherConfig
	var pin [sha256.Size]byte
	file, err := os.Open(path)
	if err != nil {
		return config, pin, fmt.Errorf("open publisher config: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 16<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return config, pin, fmt.Errorf("decode publisher config: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return config, pin, errors.New("publisher config must contain one JSON object")
	}
	endpoint, err := url.Parse(config.Endpoint)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" ||
		endpoint.User != nil || endpoint.Path != "/v1/releases" ||
		endpoint.RawQuery != "" || endpoint.Fragment != "" ||
		!strings.HasPrefix(config.Token, "migi_at_") || len(config.Token) > 256 {
		return config, pin, errors.New("publisher config has invalid endpoint or token")
	}
	normalized := strings.ReplaceAll(strings.TrimSpace(config.TLSFingerprint), ":", "")
	raw, err := hex.DecodeString(normalized)
	if err != nil || len(raw) != sha256.Size {
		return config, pin, errors.New("publisher config has invalid TLS SHA-256 fingerprint")
	}
	copy(pin[:], raw)
	return config, pin, nil
}

func fileSHA256(file *os.File) ([sha256.Size]byte, error) {
	var result [sha256.Size]byte
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return result, fmt.Errorf("hash APK: %w", err)
	}
	copy(result[:], hash.Sum(nil))
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return result, fmt.Errorf("rewind APK: %w", err)
	}
	return result, nil
}

func multipartBody(apk *os.File, metadata releaseMetadata) (io.Reader, string) {
	reader, writer := io.Pipe()
	multipartWriter := multipart.NewWriter(writer)
	go func() {
		var writeErr error
		defer func() { _ = writer.CloseWithError(writeErr) }()
		metadataHeader := make(map[string][]string)
		metadataHeader["Content-Disposition"] = []string{`form-data; name="metadata"`}
		metadataHeader["Content-Type"] = []string{"application/json"}
		part, err := multipartWriter.CreatePart(textproto.MIMEHeader(metadataHeader))
		if err == nil {
			err = json.NewEncoder(part).Encode(metadata)
		}
		if err == nil {
			apkHeader := make(map[string][]string)
			apkHeader["Content-Disposition"] = []string{
				`form-data; name="apk"; filename="release.apk"`,
			}
			apkHeader["Content-Type"] = []string{"application/vnd.android.package-archive"}
			part, err = multipartWriter.CreatePart(textproto.MIMEHeader(apkHeader))
			if err == nil {
				_, err = io.Copy(part, apk)
			}
		}
		if err == nil {
			err = multipartWriter.Close()
		}
		writeErr = err
	}()
	return reader, multipartWriter.FormDataContentType()
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
	return &http.Client{
		Transport: transport,
		Timeout:   20 * time.Minute,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("release endpoint redirects are not allowed")
		},
	}
}
