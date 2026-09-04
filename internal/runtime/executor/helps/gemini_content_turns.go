package helps

import (
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var emptyGeminiUserTurnJSON = []byte(`{"role":"user","parts":[{"text":""}]}`)

// EnsureGeminiLeadingUserContent ensures that the contents array at the given path
// starts with a user turn when sending to Gemini/Antigravity upstreams.
func EnsureGeminiLeadingUserContent(payload []byte, path string) []byte {
	firstRole := gjson.GetBytes(payload, path+".0.role")
	if firstRole.String() != "model" {
		return payload
	}
	contents := util.GetGJSONBytesNoCopy(payload, path)
	if !contents.IsArray() {
		return payload
	}
	contentArray := contents.Array()
	if len(contentArray) == 0 {
		return payload
	}

	contentItems := make([][]byte, 0, len(contentArray)+1)
	contentItems = append(contentItems, emptyGeminiUserTurnJSON)
	for _, content := range contentArray {
		contentItems = append(contentItems, []byte(content.Raw))
	}

	out, errSet := sjson.SetRawBytes(payload, path, joinRawJSONArray(contentItems))
	if errSet != nil {
		return payload
	}
	return out
}

func joinRawJSONArray(items [][]byte) []byte {
	out := make([]byte, 0, rawJSONArraySize(items))
	out = append(out, '[')
	for idx, item := range items {
		if idx > 0 {
			out = append(out, ',')
		}
		out = append(out, item...)
	}
	out = append(out, ']')
	return out
}

func rawJSONArraySize(items [][]byte) int {
	size := 2
	if len(items) > 1 {
		size += len(items) - 1
	}
	for _, item := range items {
		size += len(item)
	}
	return size
}
