package registry

import "testing"

func TestModelVisionCapabilityUsesSupportedInputModalities(t *testing.T) {
	reg := GetGlobalRegistry()
	reg.RegisterClient("vision-capability-image", "openai", []*ModelInfo{{
		ID:                       "registered-image-model",
		SupportedInputModalities: []string{"TEXT", "IMAGE"},
	}})
	defer reg.UnregisterClient("vision-capability-image")
	reg.RegisterClient("vision-capability-text", "openai", []*ModelInfo{{
		ID:                       "registered-text-model",
		SupportedInputModalities: []string{"TEXT"},
	}})
	defer reg.UnregisterClient("vision-capability-text")
	reg.RegisterClient("vision-capability-unknown", "openai", []*ModelInfo{{
		ID: "registered-no-modalities",
	}})
	defer reg.UnregisterClient("vision-capability-unknown")

	if got := ModelVisionCapability("registered-image-model"); !got.Known || !got.Supports {
		t.Fatalf("image model capability = %+v, want known support", got)
	}
	if got := ModelVisionCapability("registered-text-model"); !got.Known || got.Supports {
		t.Fatalf("text model capability = %+v, want known no support", got)
	}
	if got := ModelVisionCapability("registered-no-modalities"); got.Known || got.Supports {
		t.Fatalf("no modality capability = %+v, want unknown", got)
	}
	if got := ModelVisionCapability("missing-model"); got.Known || got.Supports {
		t.Fatalf("missing model capability = %+v, want unknown", got)
	}
}
