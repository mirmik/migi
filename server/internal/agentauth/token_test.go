package agentauth

import "testing"

func TestGeneratedTokenRoundTrips(t *testing.T) {
	tokenID, plain, wantHash, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	gotID, gotHash, ok := Parse(plain)
	if !ok || gotID != tokenID || gotHash != wantHash {
		t.Fatalf("parsed token id/hash = %q/%x, want %q/%x", gotID, gotHash, tokenID, wantHash)
	}
}

func TestMalformedTokensAreRejected(t *testing.T) {
	for _, token := range []string{
		"",
		"migi_at_missing-secret",
		"migi_at_bad_bad",
		"Bearer migi_at_bad_bad",
	} {
		if _, _, ok := Parse(token); ok {
			t.Fatalf("malformed token %q was accepted", token)
		}
	}
}
