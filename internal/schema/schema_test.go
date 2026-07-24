package schema

import (
	"encoding/json"
	"testing"
)

func TestAppProfileAuthoredConfigFallback(t *testing.T) {
	// 1. Test case: No panel config (heuristic fallback)
	art1 := Artifact{
		TID: "tenant-1",
		PID: "prod-1",
		DPs: []DP{
			{DPID: 1, Code: "switch_1", Name: "Switch 1", Semantic: Semantic{Type: "bool", Mode: "rw"}},
			{DPID: 2, Code: "bright_value", Name: "Brightness", Semantic: Semantic{Type: "value", Mode: "rw", Min: floatPtr(0), Max: floatPtr(100)}},
		},
	}

	p1 := art1.AppProfile()
	if p1.ConsumerType != "lighting" {
		t.Errorf("Expected fallback ConsumerType to be lighting, got %s", p1.ConsumerType)
	}
	if p1.Icon != "lightbulb" {
		t.Errorf("Expected fallback Icon to be lightbulb, got %s", p1.Icon)
	}
	if p1.DefaultName != "Smart Light" {
		t.Errorf("Expected fallback DefaultName to be Smart Light, got %s", p1.DefaultName)
	}
	if len(p1.Capabilities) != 2 {
		t.Errorf("Expected 2 capabilities, got %d", len(p1.Capabilities))
	}

	// 2. Test case: Authored panel config overrides display name, icon and hides one DP
	art2 := Artifact{
		TID: "tenant-1",
		PID: "prod-1",
		DPs: []DP{
			{DPID: 1, Code: "switch_1", Name: "Switch 1", Semantic: Semantic{Type: "bool", Mode: "rw"}},
			{DPID: 2, Code: "bright_value", Name: "Brightness", Semantic: Semantic{Type: "value", Mode: "rw", Min: floatPtr(0), Max: floatPtr(100)}},
		},
		Panel: json.RawMessage(`{
			"display": {
				"icon": "power_plug",
				"default_name": "Custom Light Controller"
			},
			"controls": [
				{
					"dp": 1,
					"kind": "power",
					"label": "Main Switch",
					"widget": "toggle"
				},
				{
					"dp": 2,
					"hidden": true
				}
			],
			"theme": "dark",
			"tile_metric": {
				"dp": 1,
				"format": "State: {value}"
			}
		}`),
	}

	p2 := art2.AppProfile()
	if p2.Icon != "power_plug" {
		t.Errorf("Expected overridden Icon to be power_plug, got %s", p2.Icon)
	}
	if p2.DefaultName != "Custom Light Controller" {
		t.Errorf("Expected overridden DefaultName to be Custom Light Controller, got %s", p2.DefaultName)
	}
	if p2.Theme != "dark" {
		t.Errorf("Expected overridden Theme to be dark, got %s", p2.Theme)
	}
	if p2.TileMetricDP != "1" {
		t.Errorf("Expected overridden TileMetricDP to be 1, got %s", p2.TileMetricDP)
	}
	if p2.TileMetricFormat != "State: {value}" {
		t.Errorf("Expected overridden TileMetricFormat to be 'State: {value}', got %s", p2.TileMetricFormat)
	}

	// DP 2 is hidden, so only DP 1 should be present in capabilities
	if len(p2.Capabilities) != 1 {
		t.Errorf("Expected 1 capability due to hidden DP, got %d", len(p2.Capabilities))
	}
	if p2.Capabilities[0].DP != "1" {
		t.Errorf("Expected remaining capability to be DP 1, got %s", p2.Capabilities[0].DP)
	}
	if p2.Capabilities[0].Label != "Main Switch" {
		t.Errorf("Expected label override to be 'Main Switch', got %s", p2.Capabilities[0].Label)
	}
}

func TestAssistantConfig(t *testing.T) {
	// 1. Artifact with top-level assistant config
	art1 := Artifact{
		TID: "tenant-1",
		PID: "prod-1",
		Assistant: &AssistantConfig{
			Enabled: true,
			Alexa:   true,
			Google:  false,
		},
	}
	p1 := art1.AppProfile()
	if p1.Assistant == nil || !p1.Assistant.Enabled || !p1.Assistant.Alexa || p1.Assistant.Google {
		t.Errorf("Expected Alexa=true, Google=false in p1.Assistant, got %+v", p1.Assistant)
	}

	// 2. Artifact with panel assistant config (overrides)
	art2 := Artifact{
		TID: "tenant-1",
		PID: "prod-2",
		Assistant: &AssistantConfig{
			Enabled: true,
			Alexa:   true,
			Google:  true,
		},
		Panel: json.RawMessage(`{
			"assistant": {
				"enabled": true,
				"alexa": false,
				"google": true
			}
		}`),
	}
	p2 := art2.AppProfile()
	if p2.Assistant == nil || !p2.Assistant.Enabled || p2.Assistant.Alexa || !p2.Assistant.Google {
		t.Errorf("Expected Alexa=false, Google=true in p2.Assistant, got %+v", p2.Assistant)
	}

	// 3. Artifact without assistant config
	art3 := Artifact{TID: "tenant-1", PID: "prod-3"}
	p3 := art3.AppProfile()
	if p3.Assistant != nil {
		t.Errorf("Expected nil Assistant for art3, got %+v", p3.Assistant)
	}
}

func floatPtr(v float64) *float64 {
	return &v
}
