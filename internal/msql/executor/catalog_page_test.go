package executor

import (
	"encoding/base64"
	"testing"
)

func TestCatalogCursorCodecRejectsTamperUnknownAndTrailingData(t *testing.T) {
	t.Parallel()

	core := catalogCursorCore{
		Version: catalogCursorVersion, Scope: "sha256:scope", Snapshot: "sha256:snapshot", Offset: 7,
	}
	encoded, err := encodeCatalogCursor(core)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeCatalogCursor(encoded)
	if err != nil || decoded.Offset != 7 || decoded.Scope != core.Scope || decoded.Snapshot != core.Snapshot {
		t.Fatalf("decodeCatalogCursor() = %#v, %v", decoded, err)
	}

	bytes, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	unknown := append(bytes[:len(bytes)-1], []byte(`,"unknown":true}`)...)
	if _, err := decodeCatalogCursor(base64.RawURLEncoding.EncodeToString(unknown)); err == nil {
		t.Fatal("cursor with unknown field succeeded")
	}
	trailing := append(append([]byte{}, bytes...), []byte(`{}`)...)
	if _, err := decodeCatalogCursor(base64.RawURLEncoding.EncodeToString(trailing)); err == nil {
		t.Fatal("cursor with trailing JSON succeeded")
	}
	nonCanonical := append([]byte(" "), bytes...)
	if _, err := decodeCatalogCursor(base64.RawURLEncoding.EncodeToString(nonCanonical)); err == nil {
		t.Fatal("non-canonical cursor succeeded")
	}
	bytes[len(bytes)/2] ^= 1
	if _, err := decodeCatalogCursor(base64.RawURLEncoding.EncodeToString(bytes)); err == nil {
		t.Fatal("tampered cursor succeeded")
	}
}
