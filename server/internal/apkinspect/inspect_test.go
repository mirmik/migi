package apkinspect

import (
	"errors"
	"testing"
)

func TestParsePackage(t *testing.T) {
	gotPackage, gotCode, gotName, err := parsePackage(
		"package: name='dev.migi.pilot' versionCode='42' versionName='0.8.0' platformBuildVersionName='16'\n",
	)
	if err != nil {
		t.Fatal(err)
	}
	if gotPackage != "dev.migi.pilot" || gotCode != 42 || gotName != "0.8.0" {
		t.Fatalf("got %q %d %q", gotPackage, gotCode, gotName)
	}
}

func TestParsePackageRejectsMalformedValues(t *testing.T) {
	for _, output := range []string{
		"",
		"package: name='pilot' versionCode='1' versionName='x'",
		"package: name='dev.migi.pilot' versionCode='0' versionName='x'",
		"package: name='dev.migi-pilot' versionCode='1' versionName='x'",
	} {
		if _, _, _, err := parsePackage(output); !errors.Is(err, ErrInvalidAPK) {
			t.Fatalf("parsePackage(%q) error = %v", output, err)
		}
	}
}

func TestLimitedBufferRecordsOverflowWithoutShortWrite(t *testing.T) {
	buffer := &limitedBuffer{remaining: 3}
	if written, err := buffer.Write([]byte("abcdef")); written != 6 || err != nil {
		t.Fatalf("Write = %d, %v", written, err)
	}
	if !buffer.exceeded || buffer.String() != "abc" {
		t.Fatalf("buffer = %q, exceeded = %v", buffer.String(), buffer.exceeded)
	}
}
