package main

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigureClientUsesAgentConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	contents := `{"endpoint":"https://192.0.2.10:8790/v1/agent-events","token":"migi_at_id_secret","tls_fingerprint":"` + strings.Repeat("aa", sha256.Size) + `"}`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	base, client, err := configureClient("http://127.0.0.1:8787", path)
	if err != nil {
		t.Fatal(err)
	}
	if base.String() != "https://192.0.2.10:8790" {
		t.Fatalf("base endpoint = %q", base)
	}
	request, err := client.request(http.MethodGet, endpoint(base, "/v1/files"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if request.Header.Get("Authorization") != "Bearer migi_at_id_secret" {
		t.Fatal("agent authorization header is missing")
	}
}

func TestConfigureClientRejectsInsecureAgentConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	contents := `{"endpoint":"http://192.0.2.10:8790/v1/agent-events","token":"migi_at_id_secret","tls_fingerprint":"` + strings.Repeat("aa", sha256.Size) + `"}`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := configureClient("http://127.0.0.1:8787", path); err == nil {
		t.Fatal("insecure agent endpoint was accepted")
	}
}

func TestGetVerifiesAndCommitsDownload(t *testing.T) {
	const id = "0123456789abcdef0123456789abcdef"
	content := []byte("phone screenshot")
	digest := sha256.Sum256(content)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/files/"+id+"/content" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(content)))
		w.Header().Set("X-Content-SHA256", fmt.Sprintf("%x", digest))
		_, _ = w.Write(content)
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	client := &fileClient{http: server.Client()}
	output := filepath.Join(t.TempDir(), "screenshot.png")
	if err := get(client, base, id, output); err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if string(actual) != string(content) {
		t.Fatalf("download = %q", actual)
	}
}

func TestGetDeletesDigestMismatch(t *testing.T) {
	const id = "0123456789abcdef0123456789abcdef"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "3")
		w.Header().Set("X-Content-SHA256", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
		_, _ = w.Write([]byte("one"))
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	client := &fileClient{http: server.Client()}
	output := filepath.Join(t.TempDir(), "bad.bin")
	if err := get(client, base, id, output); err == nil {
		t.Fatal("digest mismatch was accepted")
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("failed download was not removed: %v", err)
	}
}
