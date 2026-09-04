package registry

import "strings"

// VisionCapability describes whether model metadata made a definitive image
// input decision. Known is false when the model is absent or has no modality data.
type VisionCapability struct {
	Known    bool
	Supports bool
}

// ModelVisionCapability returns image input support from SupportedInputModalities.
// Dynamic registry entries take precedence over static definitions via LookupModelInfo.
func ModelVisionCapability(modelID string, provider ...string) VisionCapability {
	info := LookupModelInfo(strings.TrimSpace(modelID), provider...)
	if info == nil || len(info.SupportedInputModalities) == 0 {
		return VisionCapability{}
	}
	for _, modality := range info.SupportedInputModalities {
		if strings.EqualFold(strings.TrimSpace(modality), "IMAGE") {
			return VisionCapability{Known: true, Supports: true}
		}
	}
	return VisionCapability{Known: true, Supports: false}
}
