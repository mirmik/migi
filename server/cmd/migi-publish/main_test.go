package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "publisher.json")
	body := `{
  "endpoint": "https://migi.example:10444/v1/releases",
  "token": "migi_at_id_secret",
  "package_name": "dev.migi.pilot",
  "signer_sha256": "` + strings.Repeat("ab", 32) + `",
  "tls_fingerprint": "` + strings.Repeat("AA:", 31) + `AA"
}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	config, pin, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.PackageName != "dev.migi.pilot" || pin[0] != 0xaa || pin[31] != 0xaa {
		t.Fatalf("unexpected config %#v pin %x", config, pin)
	}
}

func TestLoadConfigRejectsCredentialRedirectSurface(t *testing.T) {
	for _, endpoint := range []string{
		"http://migi.example/v1/releases",
		"https://user@migi.example/v1/releases",
		"https://migi.example/v1/releases?next=elsewhere",
		"https://migi.example/not-releases",
	} {
		t.Run(endpoint, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "publisher.json")
			body := `{"endpoint":"` + endpoint +
				`","token":"migi_at_id_secret","package_name":"dev.migi.pilot",` +
				`"signer_sha256":"` + strings.Repeat("ab", 32) +
				`","tls_fingerprint":"` + strings.Repeat("aa", 32) + `"}`
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := loadConfig(path); err == nil {
				t.Fatal("invalid endpoint was accepted")
			}
		})
	}
}
