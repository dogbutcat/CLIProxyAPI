package util

import (
	"unsafe"

	"github.com/tidwall/gjson"
)

// ParseGJSONBytesNoCopy parses a JSON byte slice into a GJSON result without copying
// the underlying bytes.
//
// The input bytes must remain valid and unmodified for the result's lifetime.
// Callers must treat returned results as read-only views into the backing slice.
func ParseGJSONBytesNoCopy(data []byte) gjson.Result {
	if len(data) == 0 {
		return gjson.Result{}
	}
	return gjson.Parse(unsafe.String(unsafe.SliceData(data), len(data)))
}

// GetGJSONBytesNoCopy returns a GJSON result that may reference data directly.
// The input bytes must remain valid and unmodified for the result's lifetime.
// Callers must use the returned result read-only and not mutate the backing bytes.
func GetGJSONBytesNoCopy(data []byte, path string) gjson.Result {
	if len(data) == 0 {
		return gjson.Result{}
	}
	return gjson.Get(unsafe.String(unsafe.SliceData(data), len(data)), path)
}
