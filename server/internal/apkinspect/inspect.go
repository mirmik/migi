package apkinspect

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	defaultTimeout   = 20 * time.Second
	defaultMaxOutput = 256 << 10
)

var (
	ErrInvalidAPK        = errors.New("APK is invalid or its signature does not verify")
	ErrUnsupportedSigner = errors.New("APK signer profile is not supported")

	packageLinePattern  = regexp.MustCompile(`(?m)^package: name='([^']+)' versionCode='([0-9]+)' versionName='([^']*)'`)
	signerCountPattern  = regexp.MustCompile(`(?m)^Number of signers: ([0-9]+)$`)
	signerDigestPattern = regexp.MustCompile(`(?m)^Signer #1 certificate SHA-256 digest: ([0-9a-fA-F]{64})$`)
)

type Info struct {
	PackageName  string
	VersionCode  int64
	VersionName  string
	Size         int64
	SHA256       string
	SignerSHA256 string
}

type Config struct {
	APKSIGNER string
	AAPT2     string
	Timeout   time.Duration
	MaxOutput int
}

type Inspector struct {
	apksigner string
	aapt2     string
	timeout   time.Duration
	maxOutput int
}

func New(config Config) (*Inspector, error) {
	if config.APKSIGNER == "" || config.AAPT2 == "" {
		return nil, errors.New("apksigner and aapt2 paths are required")
	}
	for name, path := range map[string]string{
		"apksigner": config.APKSIGNER,
		"aapt2":     config.AAPT2,
	} {
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w", name, err)
		}
		if !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
			return nil, fmt.Errorf("%s is not an executable regular file", name)
		}
	}
	if config.Timeout <= 0 {
		config.Timeout = defaultTimeout
	}
	if config.MaxOutput <= 0 {
		config.MaxOutput = defaultMaxOutput
	}
	return &Inspector{
		apksigner: config.APKSIGNER,
		aapt2:     config.AAPT2,
		timeout:   config.Timeout,
		maxOutput: config.MaxOutput,
	}, nil
}

func (i *Inspector) Inspect(ctx context.Context, path string) (Info, error) {
	file, err := os.Open(path)
	if err != nil {
		return Info{}, fmt.Errorf("open APK: %w", err)
	}
	stat, err := file.Stat()
	if err != nil {
		file.Close()
		return Info{}, fmt.Errorf("stat APK: %w", err)
	}
	if !stat.Mode().IsRegular() || stat.Size() <= 0 {
		file.Close()
		return Info{}, ErrInvalidAPK
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		file.Close()
		return Info{}, fmt.Errorf("hash APK: %w", err)
	}
	if err := file.Close(); err != nil {
		return Info{}, fmt.Errorf("close APK: %w", err)
	}

	signatureOutput, err := i.run(ctx, i.apksigner,
		"verify", "--verbose", "--print-certs", "--Werr", path)
	if err != nil {
		return Info{}, fmt.Errorf("%w: %v", ErrInvalidAPK, err)
	}
	signer, err := parseSigner(signatureOutput)
	if err != nil {
		return Info{}, err
	}
	manifestOutput, err := i.run(ctx, i.aapt2, "dump", "badging", path)
	if err != nil {
		return Info{}, fmt.Errorf("%w: read manifest: %v", ErrInvalidAPK, err)
	}
	packageName, versionCode, versionName, err := parsePackage(manifestOutput)
	if err != nil {
		return Info{}, err
	}
	return Info{
		PackageName:  packageName,
		VersionCode:  versionCode,
		VersionName:  versionName,
		Size:         stat.Size(),
		SHA256:       hex.EncodeToString(digest.Sum(nil)),
		SignerSHA256: signer,
	}, nil
}

func (i *Inspector) Versions(ctx context.Context) (string, error) {
	apksigner, err := i.run(ctx, i.apksigner, "version")
	if err != nil {
		return "", err
	}
	aapt2, err := i.run(ctx, i.aapt2, "version")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("apksigner %s; %s", strings.TrimSpace(apksigner), strings.TrimSpace(aapt2)), nil
}

func (i *Inspector) run(parent context.Context, executable string, arguments ...string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, i.timeout)
	defer cancel()
	output := &limitedBuffer{remaining: i.maxOutput}
	command := exec.CommandContext(ctx, executable, arguments...)
	command.Dir = filepath.Dir(arguments[len(arguments)-1])
	command.Env = []string{
		"HOME=/nonexistent",
		"LANG=C",
		"LC_ALL=C",
		"PATH=/usr/bin:/bin",
	}
	command.Stdout = output
	command.Stderr = output
	err := command.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "", fmt.Errorf("tool timed out after %s", i.timeout)
	}
	if output.exceeded {
		return "", fmt.Errorf("tool output exceeded %d bytes", i.maxOutput)
	}
	if err != nil {
		return "", fmt.Errorf("%s failed: %w: %s",
			filepath.Base(executable), err, strings.TrimSpace(output.String()))
	}
	return output.String(), nil
}

func parseSigner(output string) (string, error) {
	countMatch := signerCountPattern.FindStringSubmatch(output)
	if countMatch == nil || countMatch[1] != "1" {
		return "", ErrUnsupportedSigner
	}
	// apksigner does not expose signing-certificate lineage through its CLI.
	// Requiring v2 and rejecting v3/v3.1 is the conservative version-1 policy:
	// v3 is the only accepted scheme here that can carry proof-of-rotation.
	if !strings.Contains(output, "Verified using v2 scheme (APK Signature Scheme v2): true") ||
		strings.Contains(output, "Verified using v3 scheme (APK Signature Scheme v3): true") ||
		strings.Contains(output, "Verified using v3.1 scheme (APK Signature Scheme v3.1): true") {
		return "", ErrUnsupportedSigner
	}
	digestMatch := signerDigestPattern.FindStringSubmatch(output)
	if digestMatch == nil {
		return "", ErrInvalidAPK
	}
	return strings.ToLower(digestMatch[1]), nil
}

func parsePackage(output string) (string, int64, string, error) {
	match := packageLinePattern.FindStringSubmatch(output)
	if match == nil {
		return "", 0, "", ErrInvalidAPK
	}
	versionCode, err := strconv.ParseInt(match[2], 10, 64)
	if err != nil || versionCode <= 0 {
		return "", 0, "", ErrInvalidAPK
	}
	if !validPackageName(match[1]) {
		return "", 0, "", ErrInvalidAPK
	}
	return match[1], versionCode, match[3], nil
}

func validPackageName(value string) bool {
	if len(value) == 0 || len(value) > 255 || !strings.Contains(value, ".") {
		return false
	}
	for _, part := range strings.Split(value, ".") {
		if part == "" || !(part[0] == '_' || part[0] >= 'A' && part[0] <= 'Z' || part[0] >= 'a' && part[0] <= 'z') {
			return false
		}
		for _, character := range part[1:] {
			if !(character == '_' ||
				character >= 'A' && character <= 'Z' ||
				character >= 'a' && character <= 'z' ||
				character >= '0' && character <= '9') {
				return false
			}
		}
	}
	return true
}

type limitedBuffer struct {
	builder   strings.Builder
	remaining int
	exceeded  bool
}

func (b *limitedBuffer) Write(value []byte) (int, error) {
	if len(value) > b.remaining {
		_, _ = b.builder.Write(value[:b.remaining])
		b.remaining = 0
		b.exceeded = true
		return len(value), nil
	}
	b.remaining -= len(value)
	return b.builder.Write(value)
}

func (b *limitedBuffer) String() string {
	return b.builder.String()
}
