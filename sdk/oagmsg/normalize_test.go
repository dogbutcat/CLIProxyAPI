package oagmsg

import "testing"

func TestCacheFingerprintGoldenHashes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		format  Format
		payload []byte
		want    PrefixFingerprint
	}{
		{
			name:    "fork",
			format:  FormatOpenAI,
			payload: []byte(`{"messages":[{"role":"system","content":"approved instructions v1"},{"role":"user","content":"Start task"},{"role":"assistant","content":"ignored branch A"},{"role":"user","content":"Continue branch A"}]}`),
			want: PrefixFingerprint{
				FingerprintID:       "fp:86969d71dddcaa58129074bd473a0dac31881f8d21b1feda3bb8725e3e917085",
				CacheID:             "d2b0981a316c930dfff05fbd9dfccbef715627b5e7b8adcecbb059b52965231b",
				TailID:              "tail2:4d26cea7aa156db91c26ac39318b72016798e4c35a0bdf4311ab72b21b09b6cc",
				CacheIDAfterSuccess: "dfe3d541657425f06796807d2e1fe4b57b6d215c5cc1e613ba106b5c0a32f628",
			},
		},
		{
			name:    "revert",
			format:  FormatOpenAI,
			payload: []byte(`{"messages":[{"role":"system","content":"approved instructions v2"},{"role":"user","content":"Start task"},{"role":"assistant","content":"different reverted assistant text"},{"role":"user","content":"Continue branch B"}]}`),
			want: PrefixFingerprint{
				FingerprintID:       "fp:86969d71dddcaa58129074bd473a0dac31881f8d21b1feda3bb8725e3e917085",
				CacheID:             "d33558bd93d8af120985e522830d1e181f5c7df7d93ee235bc1de1aa3bc44b92",
				TailID:              "tail2:4d26cea7aa156db91c26ac39318b72016798e4c35a0bdf4311ab72b21b09b6cc",
				CacheIDAfterSuccess: "bfef18dd9832aa1ffcbc0a56faaf47bc2275bb5184454be011c2d43ce4217c44",
			},
		},
		{
			name:    "tools",
			format:  FormatOpenAI,
			payload: []byte(`{"messages":[{"role":"user","content":"Use a tool"},{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"/tmp/a\",\"limit\":2}"}}]},{"role":"tool","tool_call_id":"call_1","content":"file contents"},{"role":"user","content":"Now summarize"}]}`),
			want: PrefixFingerprint{
				FingerprintID:       "fp:dc7c5761e6c8e81061aa85d2e4cb1cd4bd726d43156faf374fa3ae28a495bd9c",
				CacheID:             "677c25f596b2d5588bf7f01fc519b48969578a0246b52bbf6c6cb6b1d8076a57",
				TailID:              "tail2:d9983d6455005c9f6caf59176f0a308bdf0f31817e6bc69c119e85de69f1e710",
				CacheIDAfterSuccess: "1f81c02416b97f926c164ce43966e44cd61eb38f0b55ce5c9f420551da6e89d2",
			},
		},
	}

	for _, tt := range cases {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ComputePrefixFingerprint(tt.format, tt.payload, 2)
			if got != tt.want {
				t.Fatalf("ComputePrefixFingerprint() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestCacheFingerprintExcludesSystemAndKeepsFirstUser(t *testing.T) {
	t.Parallel()

	first := []byte(`{"messages":[{"role":"system","content":"approved instructions A"},{"role":"user","content":"Stable root"},{"role":"assistant","content":"one"},{"role":"user","content":"next"}]}`)
	changedSystem := []byte(`{"messages":[{"role":"system","content":"approved instructions B"},{"role":"user","content":"Stable root"},{"role":"assistant","content":"two"},{"role":"user","content":"next"}]}`)
	changedUser := []byte(`{"messages":[{"role":"system","content":"approved instructions B"},{"role":"user","content":"Different root"},{"role":"assistant","content":"two"},{"role":"user","content":"next"}]}`)

	want := "fp:139f51d54438b79df595aa8eee9f2f0403fdfd13d77abf3d019f4c24ffcc5876"
	if got := ComputeFingerprint(FormatOpenAI, first); got != want {
		t.Fatalf("ComputeFingerprint(first) = %q, want %q", got, want)
	}
	if got := ComputeFingerprint(FormatOpenAI, changedSystem); got != want {
		t.Fatalf("ComputeFingerprint(changedSystem) = %q, want %q", got, want)
	}
	if got := ComputeFingerprint(FormatOpenAI, changedUser); got == want {
		t.Fatalf("ComputeFingerprint(changedUser) = %q, want different from %q", got, want)
	}
}

func TestCacheFingerprintMissingUserGolden(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"messages":[{"role":"system","content":"approved only"},{"role":"assistant","content":"assistant only"}]}`)
	got := ComputePrefixFingerprint(FormatOpenAI, payload, 2)
	want := PrefixFingerprint{
		CacheID:             "e3411381d9ce5c4751649258ba96f32e8142f8b908cd25bafbbb045cfc0dd171",
		CacheIDAfterSuccess: "950fa746ed03f88631282275380816c2c72f1c74b9a99034d389c999eeb33413",
	}
	if got != want {
		t.Fatalf("ComputePrefixFingerprint() = %#v, want %#v", got, want)
	}
}

func TestCacheFingerprintProtocolParityGolden(t *testing.T) {
	t.Parallel()

	openAI := []byte(`{"messages":[{"role":"system","content":"Be concise"},{"role":"user","content":[{"type":"text","text":"Hello"}]},{"role":"assistant","content":"ignored"},{"role":"user","content":"Next"}]}`)
	anthropic := []byte(`{"system":"Be concise","messages":[{"role":"user","content":"Hello"},{"role":"assistant","content":"ignored differently"},{"role":"user","content":"Next"}]}`)
	gemini := []byte(`{"systemInstruction":{"parts":[{"text":"Be concise"}]},"contents":[{"role":"user","parts":[{"text":"Hello"}]},{"role":"model","parts":[{"text":"ignored again"}]},{"role":"user","parts":[{"text":"Next"}]}]}`)
	interactions := []byte(`{"system_instruction":"Be concise","input":[{"type":"user_input","content":[{"type":"text","text":"Hello"}]},{"type":"model_output","content":[{"type":"text","text":"ignored"}]},{"type":"user_input","content":[{"type":"text","text":"Next"}]}]}`)

	want := PrefixFingerprint{
		FingerprintID:       "fp:216fbed91a27569527b8f5b545295fce94a95289fdb486ccf4b80947baadae94",
		CacheID:             "1d0e15a03e8e3722c70ca19318f7c55ba765802a4a80d23428216ad3d8bcfa61",
		TailID:              "tail2:47426ccefab073ed7e47ff1f78cd26215e7f13dcc467ba7fe50fec7cd0578505",
		CacheIDAfterSuccess: "d475539ec87936644ff0544cbbe6aa8973e0b248d4ff824d6986c707c9849ef8",
	}
	cases := map[Format][]byte{
		FormatOpenAI:       openAI,
		FormatAnthropic:    anthropic,
		FormatGemini:       gemini,
		FormatInteractions: interactions,
	}
	for format, payload := range cases {
		got := ComputePrefixFingerprint(format, payload, 2)
		if got != want {
			t.Fatalf("%s ComputePrefixFingerprint() = %#v, want %#v", format, got, want)
		}
	}
}
