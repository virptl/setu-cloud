package app

import (
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// Capability describes a single controllable datapoint on a device.
type Capability struct {
	DP    string `json:"dp"`
	Kind  string `json:"kind"` // power | brightness | color_temp | color | target_temp
	Label string `json:"label"`
	Min   *int   `json:"min,omitempty"`
	Max   *int   `json:"max,omitempty"`
	Unit  string `json:"unit,omitempty"`
}

// ProductProfile is the capability profile the app renders the control sheet from.
type ProductProfile struct {
	PID          string         `json:"pid"`
	ConsumerType string         `json:"consumer_type"`
	Display      ProfileDisplay `json:"display"`
	Capabilities []Capability   `json:"capabilities"`
	TileMetric   *TileMetric    `json:"tile_metric,omitempty"`
}

type ProfileDisplay struct {
	Icon        string `json:"icon"`
	DefaultName string `json:"default_name"`
}

type TileMetric struct {
	DP     string `json:"dp"`
	Format string `json:"format"`
}

func ip(n int) *int { return &n }

// productProfiles is the authoritative map of pid → profile.
// Static and versioned; safe to cache by clients.
var productProfiles = map[string]ProductProfile{
	"light-rgbcw": {
		PID:          "light-rgbcw",
		ConsumerType: "lighting",
		Display:      ProfileDisplay{Icon: "lightbulb", DefaultName: "Smart Light"},
		Capabilities: []Capability{
			{DP: "1", Kind: "power", Label: "Power"},
			{DP: "2", Kind: "brightness", Label: "Brightness", Min: ip(0), Max: ip(100), Unit: "%"},
			{DP: "3", Kind: "color_temp", Label: "Color Temperature", Min: ip(0), Max: ip(100)},
			{DP: "5", Kind: "color", Label: "Color"},
		},
		TileMetric: &TileMetric{DP: "2", Format: "{value}%"},
	},
	"light1": {
		PID:          "light1",
		ConsumerType: "lighting",
		Display:      ProfileDisplay{Icon: "lightbulb", DefaultName: "Smart Light"},
		Capabilities: []Capability{
			{DP: "1", Kind: "power", Label: "Power"},
			{DP: "2", Kind: "brightness", Label: "Brightness", Min: ip(10), Max: ip(100), Unit: "%"},
		},
		TileMetric: &TileMetric{DP: "2", Format: "{value}%"},
	},
	"th1": {
		PID:          "th1",
		ConsumerType: "climate",
		Display:      ProfileDisplay{Icon: "thermometer", DefaultName: "Smart Thermostat"},
		Capabilities: []Capability{
			{DP: "1", Kind: "power", Label: "Power"},
			{DP: "2", Kind: "target_temp", Label: "Target Temperature", Min: ip(16), Max: ip(30), Unit: "°C"},
		},
	},
	"sp1": {
		PID:          "sp1",
		ConsumerType: "plug",
		Display:      ProfileDisplay{Icon: "power_plug", DefaultName: "Smart Plug"},
		Capabilities: []Capability{
			{DP: "1", Kind: "power", Label: "Power"},
		},
	},
	"gen1": {
		PID:          "gen1",
		ConsumerType: "sensors",
		Display:      ProfileDisplay{Icon: "sensor", DefaultName: "Smart Device"},
		Capabilities: []Capability{
			{DP: "1", Kind: "power", Label: "Power"},
		},
	},
}

// typeDefaultPID maps a consumer type to the default pid used when claiming.
var typeDefaultPID = map[string]string{
	"lighting":      "light1",
	"climate":       "th1",
	"plug":          "sp1",
	"security":      "gen1",
	"entertainment": "gen1",
	"sensors":       "gen1",
}

// profile is the internal type returned by profileForType (used in claim/adopt flow).
type profile struct {
	PID  string
	Caps []Capability
}

// profileForType returns the default pid and capability set for a device type.
func profileForType(t string) profile {
	pid := typeDefaultPID[t]
	if pid == "" {
		pid = "gen1"
	}
	if pp, ok := productProfiles[pid]; ok {
		return profile{pid, pp.Capabilities}
	}
	return profile{"gen1", productProfiles["gen1"].Capabilities}
}

// capsForPID returns the capabilities for a pid, falling back to gen1.
func capsForPID(pid string) []Capability {
	if pp, ok := productProfiles[pid]; ok {
		return pp.Capabilities
	}
	return productProfiles["gen1"].Capabilities
}

// deviceTypeForPID maps a product ID to its consumer type string.
func deviceTypeForPID(pid string) string {
	if pp, ok := productProfiles[pid]; ok {
		return pp.ConsumerType
	}
	return "sensors"
}

// dpKindsForPID returns a dp→kind map for the given pid (used for command validation).
func dpKindsForPID(pid string) map[string]string {
	pp, ok := productProfiles[pid]
	if !ok {
		return nil
	}
	m := make(map[string]string, len(pp.Capabilities))
	for _, c := range pp.Capabilities {
		m[c.DP] = c.Kind
	}
	return m
}

// GetProductProfile handles GET /v1/products/{pid}/profile.
// Response is static per pid; clients should cache it for the session.
func GetProductProfile() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pid := chi.URLParam(r, "pid")
		prof, ok := productProfiles[pid]
		if !ok {
			writeErr(w, 404, "not_found", "unknown product")
			return
		}
		etag := `"` + pid + `"`
		w.Header().Set("Cache-Control", "max-age=3600")
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		writeJSON(w, 200, prof)
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
