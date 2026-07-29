package snapshot_test

import (
	"encoding/json"
	"testing"
)

func assertJSONField(t *testing.T, encoded []byte, field string, want any) {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatal(err)
	}
	if got := object[field]; got != want {
		t.Fatalf("%s = %#v, want %#v", field, got, want)
	}
}

func jsonPathExists(encoded []byte, path ...any) bool {
	var value any
	if json.Unmarshal(encoded, &value) != nil {
		return false
	}
	for _, segment := range path {
		switch segment := segment.(type) {
		case string:
			object, ok := value.(map[string]any)
			if !ok {
				return false
			}
			value, ok = object[segment]
			if !ok {
				return false
			}
		case int:
			array, ok := value.([]any)
			if !ok || segment < 0 || segment >= len(array) {
				return false
			}
			value = array[segment]
		default:
			return false
		}
	}
	return true
}
