package oagmsg

import (
	"encoding/json"
	"testing"
)

func TestCustomToolUseBlockFreeformInputPreservesRawValues(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "empty", input: ""},
		{name: "text", input: "run the local formatter"},
		{name: "non-json", input: `cmd: { go test ./...`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			block := CustomToolUseBlock{
				ID:    "call_custom",
				Name:  "shell",
				Input: tt.input,
			}

			if block.blockType() != "custom_tool_use" {
				t.Fatalf("blockType() = %q, want %q", block.blockType(), "custom_tool_use")
			}
			if got := block.RawInput(); got != tt.input {
				t.Fatalf("RawInput() = %q, want %q", got, tt.input)
			}

			raw, err := json.Marshal(block)
			if err != nil {
				t.Fatalf("json.Marshal error: %v", err)
			}

			var decoded struct {
				Input string `json:"input"`
			}
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatalf("json.Unmarshal error: %v", err)
			}
			if decoded.Input != tt.input {
				t.Fatalf("decoded input = %q, want %q", decoded.Input, tt.input)
			}
		})
	}
}

func TestCustomToolResultBlockOutputAccessor(t *testing.T) {
	block := CustomToolResultBlock{
		ToolUseID: "call_custom",
		Output:    "done",
	}

	if block.blockType() != "custom_tool_result" {
		t.Fatalf("blockType() = %q, want %q", block.blockType(), "custom_tool_result")
	}
	if got := block.OutputString(); got != "done" {
		t.Fatalf("OutputString() = %q, want %q", got, "done")
	}
}
