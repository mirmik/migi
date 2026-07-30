package apkinspect

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAndroidBuildToolsIntegration(t *testing.T) {
	apk := os.Getenv("MIGI_TEST_APK")
	apksigner := os.Getenv("MIGI_TEST_APKSIGNER")
	aapt2 := os.Getenv("MIGI_TEST_AAPT2")
	if apk == "" || apksigner == "" || aapt2 == "" {
		t.Skip("set MIGI_TEST_APK, MIGI_TEST_APKSIGNER, and MIGI_TEST_AAPT2")
	}
	inspector, err := New(Config{APKSIGNER: apksigner, AAPT2: aapt2})
	if err != nil {
		t.Fatal(err)
	}
	if versions, err := inspector.Versions(context.Background()); err != nil || versions == "" {
		t.Fatalf("versions = %q, %v", versions, err)
	}
	info, err := inspector.Inspect(context.Background(), apk)
	if err != nil {
		t.Fatal(err)
	}
	if info.PackageName != "dev.migi.pilot" || info.VersionCode <= 0 ||
		len(info.SHA256) != 64 || len(info.SignerSHA256) != 64 {
		t.Fatalf("unexpected info: %#v", info)
	}
	t.Logf("verified %#v", info)

	for _, rejected := range strings.Split(os.Getenv("MIGI_TEST_REJECT_APKS"), string(os.PathListSeparator)) {
		if rejected == "" {
			continue
		}
		_, err := inspector.Inspect(context.Background(), rejected)
		if !errors.Is(err, ErrInvalidAPK) {
			t.Errorf("%s error = %v, want invalid APK", filepath.Base(rejected), err)
		}
	}
}
