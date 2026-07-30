package main

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

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
	output := filepath.Join(t.TempDir(), "screenshot.png")
	if err := get(base, id, output); err != nil {
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
	output := filepath.Join(t.TempDir(), "bad.bin")
	if err := get(base, id, output); err == nil {
		t.Fatal("digest mismatch was accepted")
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("failed download was not removed: %v", err)
	}
}
