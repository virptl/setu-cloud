package app

import "fmt"

// Capability describes a single controllable datapoint on a device.
type Capability struct {
	DP    string `json:"dp"`
	Kind  string `json:"kind"` // power | brightness | color_temp | target_temp
	Label string `json:"label"`
	Min   *int   `json:"min,omitempty"`
	Max   *int   `json:"max,omitempty"`
	Unit  string `json:"unit,omitempty"`
}

type profile struct {
	PID  string
	Caps []Capability
}

func ip(n int) *int { return &n }

// profileForType maps a device type to a pid and capability set.
// dp "1" is always power.
func profileForType(t string) profile {
	power := Capability{DP: "1", Kind: "power", Label: "Power"}
	switch t {
	case "lighting":
		return profile{"light1", []Capability{power,
			{DP: "2", Kind: "brightness", Label: "Brightness", Min: ip(10), Max: ip(100), Unit: "%"}}}
	case "climate":
		return profile{"th1", []Capability{power,
			{DP: "2", Kind: "target_temp", Label: "Target Temperature", Min: ip(16), Max: ip(30), Unit: "°C"}}}
	case "plug":
		return profile{"sp1", []Capability{power}}
	default: // security | entertainment | sensors
		return profile{"gen1", []Capability{power}}
	}
}

// metricFor returns a human-readable status string for a device tile.
func metricFor(typ string, on bool, dps map[string]any) string {
	if !on {
		return "Off"
	}
	switch typ {
	case "lighting":
		if b, ok := dps["2"]; ok {
			return formatNum(b, "%")
		}
	case "climate":
		if t, ok := dps["2"]; ok {
			return formatNum(t, "°C")
		}
	}
	return "On"
}

func formatNum(v any, unit string) string {
	switch n := v.(type) {
	case float64:
		return fmt.Sprintf("%.0f%s", n, unit)
	}
	return "On"
}
