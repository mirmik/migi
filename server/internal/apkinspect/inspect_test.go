package apkinspect

import (
	"errors"
	"strings"
	"testing"
)

const validSignerOutput = `Verifies
Verified using v1 scheme (JAR signing): false
Verified using v2 scheme (APK Signature Scheme v2): true
Verified using v3 scheme (APK Signature Scheme v3): false
Verified using v3.1 scheme (APK Signature Scheme v3.1): false
Number of signers: 1
Signer #1 certificate SHA-256 digest: 2BBC1BDE285CDDB596D86AB87BE844C6B8A0C504F4488B7C07B00BFD5F4F3097
`

func TestParseSignerAcceptsSingleV2Signer(t *testing.T) {
	got, err := parseSigner(validSignerOutput)
	if err != nil {
		t.Fatal(err)
	}
	const want = "2bbc1bde285cddb596d86ab87be844c6b8a0c504f4488b7c07b00bfd5f4f3097"
	if got != want {
		t.Fatalf("digest = %q, want %q", got, want)
	}
}

func TestParseSignerRejectsMultipleSignersAndV3(t *testing.T) {
	for name, output := range map[string]string{
		"multiple": strings.Replace(validSignerOutput, "Number of signers: 1", "Number of signers: 2", 1),
		"v3":       strings.Replace(validSignerOutput, "Verified using v3 scheme (APK Signature Scheme v3): false", "Verified using v3 scheme (APK Signature Scheme v3): true", 1),
		"no-v2":    strings.Replace(validSignerOutput, "Verified using v2 scheme (APK Signature Scheme v2): true", "Verified using v2 scheme (APK Signature Scheme v2): false", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseSigner(output); !errors.Is(err, ErrUnsupportedSigner) {
				t.Fatalf("error = %v, want unsupported signer", err)
			}
		})
	}
}

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
