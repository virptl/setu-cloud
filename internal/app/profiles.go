package app

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/setucore/setu-cloud/internal/schema"
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
	PID           string         `json:"pid"`
	ConsumerType  string         `json:"consumer_type"`
	Display       ProfileDisplay `json:"display"`
	Capabilities  []Capability   `json:"capabilities"`
	TileMetric    *TileMetric    `json:"tile_metric,omitempty"`
	SchemaVersion *int           `json:"schema_version,omitempty"`
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

// CapsForPID is the exported version of capsForPID for use by voice assistant adapters.
func CapsForPID(pid string) []Capability { return capsForPID(pid) }

// ResolveCapabilities returns the capabilities for a pid, first checking released_products database, then falling back to static map.
func ResolveCapabilities(ctx context.Context, db *pgxpool.Pool, pid string, version int) []Capability {
	var raw []byte
	var err error
	if version > 0 {
		err = db.QueryRow(ctx,
			`SELECT schema_json FROM released_products
  WHERE pid=$1 AND version=$2`, pid, version).Scan(&raw)
	} else {
		err = db.QueryRow(ctx,
			`SELECT schema_json FROM released_products
  WHERE pid=$1 ORDER BY version DESC LIMIT 1`, pid).Scan(&raw)
	}

	if err == nil {
		art, perr := schema.Parse(raw)
		if perr == nil {
			p := art.AppProfile()
			caps := make([]Capability, 0, len(p.Capabilities))
			for _, c := range p.Capabilities {
				caps = append(caps, Capability{DP: c.DP, Kind: c.Kind, Label: c.Label, Min: c.Min, Max: c.Max, Unit: c.Unit})
			}
			return caps
		}
	}

	return capsForPID(pid)
}

// deviceTypeForPID maps a product ID to its consumer type string.
func deviceTypeForPID(pid string) string {
	if pp, ok := productProfiles[pid]; ok {
		return pp.ConsumerType
	}
	return "sensors"
}

// DeviceTypeForPID is the exported version of deviceTypeForPID for use by voice assistant adapters.
func DeviceTypeForPID(pid string) string { return deviceTypeForPID(pid) }

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
//
// The profile is derived from the released schema_version (Shared contract B app
// projection) for any released PID; ?version= selects a specific version
// (default: latest). PIDs from the legacy hardcoded catalog still resolve via a
// fallback so existing consumer devices keep working. The ETag is the schema
// content hash (or the pid for static profiles) so clients can cache per version.
func GetProductProfile(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pid := chi.URLParam(r, "pid")

		prof, etag, ok := profileFromReleasedSchema(r, db, pid)
		if !ok {
			static, found := productProfiles[pid]
			if !found {
				writeErr(w, 404, "not_found", "unknown product")
				return
			}
			prof, etag = static, `"`+pid+`"`
		}

		w.Header().Set("Cache-Control", "max-age=3600")
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		writeJSON(w, 200, prof)
	}
}

// profileFromReleasedSchema looks up the released schema for a pid (optionally a
// specific ?version=) and projects it to a ProductProfile. Returns ok=false when
// no released schema exists for the pid.
func profileFromReleasedSchema(r *http.Request, db *pgxpool.Pool, pid string) (ProductProfile, string, bool) {
	var (
		raw         []byte
		version     int
		contentHash string
		err         error
	)
	if v := r.URL.Query().Get("version"); v != "" {
		ver, convErr := strconv.Atoi(v)
		if convErr != nil {
			return ProductProfile{}, "", false
		}
		err = db.QueryRow(r.Context(),
			`SELECT schema_json, version, content_hash FROM released_products
			  WHERE pid=$1 AND version=$2`, pid, ver).Scan(&raw, &version, &contentHash)
	} else {
		err = db.QueryRow(r.Context(),
			`SELECT schema_json, version, content_hash FROM released_products
			  WHERE pid=$1 ORDER BY version DESC LIMIT 1`, pid).Scan(&raw, &version, &contentHash)
	}
	if err == pgx.ErrNoRows || err != nil {
		return ProductProfile{}, "", false
	}

	art, perr := schema.Parse(raw)
	if perr != nil {
		return ProductProfile{}, "", false
	}
	p := art.AppProfile()

	caps := make([]Capability, 0, len(p.Capabilities))
	for _, c := range p.Capabilities {
		caps = append(caps, Capability{DP: c.DP, Kind: c.Kind, Label: c.Label, Min: c.Min, Max: c.Max, Unit: c.Unit})
	}
	prof := ProductProfile{
		PID:           pid,
		ConsumerType:  p.ConsumerType,
		Display:       ProfileDisplay{Icon: p.Icon, DefaultName: p.DefaultName},
		Capabilities:  caps,
		SchemaVersion: &version,
	}
	if p.TileMetricDP != "" {
		prof.TileMetric = &TileMetric{DP: p.TileMetricDP, Format: "{value}%"}
	}
	etag := `"` + contentHash + `"`
	return prof, etag, true
}

// metricFor returns a human-readable status string for a device tile.
func metricFor(typ string, on bool, dps map[string]any) string {
	if !on {
		return "Off"
	}
	switch typ {
	case "lighting":
		brightness := "100%"
		if b, ok := dps["2"]; ok {
			brightness = formatNum(b, "%")
		}

		// 1. Check for Color (DP 5)
		if c, ok := dps["5"]; ok {
			if rgb, ok := c.(map[string]any); ok {
				var r, g, b int
				if rv, ok := rgb["r"].(float64); ok {
					r = int(rv)
				}
				if gv, ok := rgb["g"].(float64); ok {
					g = int(gv)
				}
				if bv, ok := rgb["b"].(float64); ok {
					b = int(bv)
				}
				hex := fmt.Sprintf("%02X%02X%02X", r, g, b)
				return fmt.Sprintf("%s · #%s", brightness, hex)
			}
		}

		// 2. Check for Color Temp (DP 3)
		if ct, ok := dps["3"]; ok {
			var ctVal float64
			if v, ok := ct.(float64); ok {
				ctVal = v
			}
			mode := "Ambient"
			if ctVal == 0 {
				mode = "Warm"
			} else if ctVal == 100 {
				mode = "Cool"
			}
			return fmt.Sprintf("%s · %s", brightness, mode)
		}

		return brightness
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
