package joycode

import "encoding/json"

// ModelInfo describes a JoyCode AI model.
type ModelInfo struct {
	Label              string   `json:"label"`
	ChatAPIModel       string   `json:"chatApiModel"`
	MaxTotalTokens     int      `json:"maxTotalTokens"`
	RespMaxTokens      int      `json:"respMaxTokens"`
	Temperature        float64  `json:"temperature"`
	Features           []string `json:"features"`
	SupportStream      bool     `json:"supportStream"`
	VerificationStatus string   `json:"verificationStatus"`
	ModelID            string   `json:"modelId"`
	CreateTime         int64    `json:"createTime"`
	ExtJSON            string   `json:"ext"` // raw JSON string with adapterType etc.
}

// extInfo holds the fields parsed from the ModelInfo.ExtJSON string.
type extInfo struct {
	AdapterType string `json:"adapterType"`
}

// AdapterType returns the adapter type from the model's ext JSON field.
// Returns "" if not set (meaning the default chat completions endpoint).
func (m ModelInfo) AdapterType() string {
	if m.ExtJSON == "" {
		return ""
	}
	var ext extInfo
	if err := json.Unmarshal([]byte(m.ExtJSON), &ext); err != nil {
		return ""
	}
	return ext.AdapterType
}
