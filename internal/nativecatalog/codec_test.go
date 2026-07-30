package nativecatalog

import (
	"path/filepath"
	"reflect"
	"testing"

	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

func TestTypedPayloadRoundTripThroughNativeFile(t *testing.T) {
	t.Parallel()

	name, err := text(1, "语义目录")
	if err != nil {
		t.Fatalf("text() error = %v", err)
	}
	aliases, err := textList(5, []string{"catalog", "目录"})
	if err != nil {
		t.Fatalf("textList() error = %v", err)
	}
	payload, err := encodeFields(
		name,
		uintValue(2, 42),
		intValue(3, -7),
		boolValue(4, true),
		aliases,
	)
	if err != nil {
		t.Fatalf("encodeFields() error = %v", err)
	}

	path := filepath.Join(t.TempDir(), "typed.memora")
	file, err := nativestore.Create(path, nativestore.FileKindDatabase)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := file.Put(nativestore.ObjectKindOpaque, 1, "typed_fixture", payload); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := nativestore.Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	gotPayload, err := reopened.Get(nativestore.ObjectKindOpaque, "typed_fixture")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	got, err := decodeFields(gotPayload)
	if err != nil {
		t.Fatalf("decodeFields() error = %v", err)
	}

	gotText, err := got.text(1)
	assertFieldValue(t, "text", gotText, err, "语义目录")
	gotUint, err := got.uint64(2)
	assertFieldValue(t, "uint64", gotUint, err, uint64(42))
	gotInt, err := got.int64(3)
	assertFieldValue(t, "int64", gotInt, err, int64(-7))
	gotBool, err := got.bool(4)
	assertFieldValue(t, "bool", gotBool, err, true)
	gotAliases, err := got.textList(5)
	assertFieldValue(t, "text list", gotAliases, err, []string{"catalog", "目录"})
}

func TestDecodeFieldsRejectsTruncation(t *testing.T) {
	t.Parallel()

	value, err := text(1, "catalog")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := encodeFields(value)
	if err != nil {
		t.Fatal(err)
	}
	for length := 0; length < len(payload); length++ {
		if _, err := decodeFields(payload[:length]); err == nil {
			t.Fatalf("decodeFields(payload[:%d]) unexpectedly succeeded", length)
		}
	}
}

func assertFieldValue[T any](t *testing.T, name string, got T, err error, want T) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s error = %v", name, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s = %#v, want %#v", name, got, want)
	}
}
