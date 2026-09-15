package oagmsg

import (
	"bytes"
	"encoding/json"

	"github.com/tidwall/sjson"
)

// SetStringWithoutHTMLEscape sets a JSON string field without escaping HTML
// characters. Tool argument carriers must preserve provider-visible bytes such
// as "<", ">", and "&" because providers interpret them inside nested JSON
// strings instead of ordinary prose.
func SetStringWithoutHTMLEscape(data []byte, path, value string) ([]byte, error) {
	buf := &bytes.Buffer{}
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	if errEncode := enc.Encode(value); errEncode != nil {
		return sjson.SetBytes(data, path, value)
	}
	raw := bytes.TrimRight(buf.Bytes(), "\n")
	return sjson.SetRawBytes(data, path, raw)
}

func marshalJSONWithoutHTMLEscape(value any) ([]byte, error) {
	buf := &bytes.Buffer{}
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
