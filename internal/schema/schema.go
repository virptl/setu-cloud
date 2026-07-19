// Package schema parses the released-product artifact (dev_portal "Shared
// contract A") and derives the two projections ("Shared contract B"):
//   - the firmware hw_config the device parses at ZTP, and
//   - the app capability profile the consumer app renders controls from.
//
// These projections are the single place the cloud interprets a released schema.
package schema

import (
	"encoding/json"
	"strconv"
	"strings"
)

// Artifact is the released schema_version document stored in released_products.
type Artifact struct {
	TID         string          `json:"tid"`
	PID         string          `json:"pid"`
	Version     int             `json:"version"`
	ContentHash string          `json:"content_hash"`
	PublishedAt string          `json:"published_at"`
	DPs         []DP            `json:"dps"`
	Panel       json.RawMessage `json:"panel,omitempty"`
}

// DP is one unified data point: semantic facet + hardware facet.
type DP struct {
	DPID     int      `json:"dp_id"`
	Code     string   `json:"code"`
	Name     string   `json:"name"`
	Semantic Semantic `json:"semantic"`
	Hardware Hardware `json:"hardware"`
}

type Semantic struct {
	Type       string   `json:"type"`
	Mode       string   `json:"mode"`
	Min        *float64 `json:"min,omitempty"`
	Max        *float64 `json:"max,omitempty"`
	Step       *float64 `json:"step,omitempty"`
	Scale      *int     `json:"scale,omitempty"`
	Unit       string   `json:"unit,omitempty"`
	EnumValues []string `json:"enum_values,omitempty"`
}

type Hardware struct {
	Actuation  string `json:"actuation"`
	GPIO       *int   `json:"gpio,omitempty"`
	ActiveHigh *bool  `json:"active_high,omitempty"`
	FreqHz     *int   `json:"freq_hz,omitempty"`
	Resolution *int   `json:"resolution,omitempty"`
	// RGB channel GPIOs (actuation == "rgb"): a colour DP (e.g. colour_data)
	// carries an HSV value the app converts to R/G/B, and the firmware drives
	// three PWM channels from it. Keys match the firmware contract.
	RGPIO *int `json:"gpio_r,omitempty"`
	GGPIO *int `json:"gpio_g,omitempty"`
	BGPIO *int `json:"gpio_b,omitempty"`
	// Companion physical button of a relay, for local (offline) button→relay
	// control on the device.
	ButtonGPIO       *int  `json:"button_gpio,omitempty"`
	ButtonActiveHigh *bool `json:"button_active_high,omitempty"`
}

// Parse decodes a stored schema_json blob.
func Parse(raw []byte) (*Artifact, error) {
	var a Artifact
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	return &a, nil
}

// FirmwareConfig projects the artifact to the firmware hw_config the device
// parses (Shared contract B):
//
//	{"config":{"dps":{"1":{"type":"relay","gpio":4,"active_high":true},
//	                  "2":{"type":"pwm","gpio":13,"freq_hz":5000,"resolution":8},
//	                  "3":{"type":"rgb","gpio_r":6,"gpio_g":7,"gpio_b":8,"freq_hz":5000,"resolution":8}}}}
//
// Only actuated DPs (actuation != none) appear.
func (a *Artifact) FirmwareConfig() map[string]any {
	dps := map[string]any{}
	for _, dp := range a.DPs {
		act := dp.Hardware.Actuation
		if act == "" || act == "none" {
			continue
		}
		entry := map[string]any{"type": act}
		if act == "rgb" {
			if dp.Hardware.RGPIO != nil {
				entry["gpio_r"] = *dp.Hardware.RGPIO
			}
			if dp.Hardware.GGPIO != nil {
				entry["gpio_g"] = *dp.Hardware.GGPIO
			}
			if dp.Hardware.BGPIO != nil {
				entry["gpio_b"] = *dp.Hardware.BGPIO
			}
		} else if dp.Hardware.GPIO != nil {
			entry["gpio"] = *dp.Hardware.GPIO
		}
		if dp.Hardware.ActiveHigh != nil {
			entry["active_high"] = *dp.Hardware.ActiveHigh
		}
		if dp.Hardware.FreqHz != nil {
			entry["freq_hz"] = *dp.Hardware.FreqHz
		}
		if dp.Hardware.Resolution != nil {
			entry["resolution"] = *dp.Hardware.Resolution
		}
		// Companion button for local relay control.
		if dp.Hardware.ButtonGPIO != nil {
			entry["button_gpio"] = *dp.Hardware.ButtonGPIO
			if dp.Hardware.ButtonActiveHigh != nil {
				entry["button_active_high"] = *dp.Hardware.ButtonActiveHigh
			}
		}
		dps[strconv.Itoa(dp.DPID)] = entry
	}
	return map[string]any{"config": map[string]any{"dps": dps}}
}

// Capability kinds (must match app.Capability.Kind / capability.Kind*).
const (
	KindPower       = "power"
	KindBrightness  = "brightness"
	KindColorTemp   = "color_temp"
	KindColor       = "color"
	KindTargetTemp  = "target_temp"
	KindTemperature = "temperature"
	KindHumidity    = "humidity"
	KindContact     = "contact"
	KindMotion      = "motion"
	KindFanSpeed    = "fan_speed"
	KindLock        = "lock"
)

// Capability is one controllable/observable datapoint in the app profile.
type Capability struct {
	DP     string `json:"dp"`
	Kind   string `json:"kind"`
	Label  string `json:"label"`
	Min    *int   `json:"min,omitempty"`
	Max    *int   `json:"max,omitempty"`
	Unit   string `json:"unit,omitempty"`
	Widget string `json:"widget,omitempty"`
}

type Group struct {
	Title string `json:"title"`
	DPs   []int  `json:"dps"`
}

// Profile is the projected app capability profile.
type Profile struct {
	ConsumerType     string
	Icon             string
	DefaultName      string
	Capabilities     []Capability
	TileMetricDP     string // empty if none
	TileMetricFormat string // empty if none
	Theme            string
	Groups           []Group
}

// AppProfile projects the artifact to the app capability profile (Shared
// contract B): each DP's semantic facet maps to a capability kind.
func (a *Artifact) AppProfile() Profile {
	var panel struct {
		Display *struct {
			Icon        string `json:"icon"`
			DefaultName string `json:"default_name"`
		} `json:"display"`
		Controls []struct {
			DP      int    `json:"dp"`
			Kind    string `json:"kind"`
			Label   string `json:"label"`
			Widget  string `json:"widget"`
			Primary bool   `json:"primary"`
			Order   int    `json:"order"`
			Unit    string `json:"unit"`
			Min     *int   `json:"min"`
			Max     *int   `json:"max"`
			Hidden  bool   `json:"hidden"`
		} `json:"controls"`
		TileMetric *struct {
			DP     int    `json:"dp"`
			Format string `json:"format"`
		} `json:"tile_metric"`
		Groups []struct {
			Title string `json:"title"`
			DPs   []int  `json:"dps"`
		} `json:"groups"`
		Theme string `json:"theme"`
	}

	hasPanel := false
	if len(a.Panel) > 0 && string(a.Panel) != "{}" && string(a.Panel) != "null" {
		if err := json.Unmarshal(a.Panel, &panel); err == nil {
			hasPanel = true
		}
	}

	if hasPanel {
		dpMap := make(map[int]DP)
		for _, dp := range a.DPs {
			dpMap[dp.DPID] = dp
		}

		caps := []Capability{}
		controlDPs := make(map[int]bool)
		for _, ctrl := range panel.Controls {
			if ctrl.Hidden {
				controlDPs[ctrl.DP] = true
				continue
			}
			dp, exists := dpMap[ctrl.DP]
			if !exists {
				continue
			}
			controlDPs[ctrl.DP] = true

			kind := ctrl.Kind
			label := ctrl.Label
			unit := ctrl.Unit
			min := ctrl.Min
			max := ctrl.Max

			hCap, hOk := capabilityFor(dp)
			if kind == "" && hOk {
				kind = hCap.Kind
			}
			if label == "" {
				if hOk && hCap.Label != "" {
					label = hCap.Label
				} else if dp.Name != "" {
					label = dp.Name
				} else {
					label = dp.Code
				}
			}
			if unit == "" && hOk {
				unit = hCap.Unit
			}
			if min == nil && hOk {
				min = hCap.Min
			}
			if max == nil && hOk {
				max = hCap.Max
			}

			caps = append(caps, Capability{
				DP:     strconv.Itoa(ctrl.DP),
				Kind:   kind,
				Label:  label,
				Min:    min,
				Max:    max,
				Unit:   unit,
				Widget: ctrl.Widget,
			})
		}

		for _, dp := range a.DPs {
			if controlDPs[dp.DPID] {
				continue
			}
			if c, ok := capabilityFor(dp); ok {
				caps = append(caps, c)
			}
		}

		var fallbackType, fallbackIcon, fallbackName string
		has := map[string]bool{}
		for _, c := range caps {
			has[c.Kind] = true
		}
		switch {
		case has[KindBrightness] || has[KindColor] || has[KindColorTemp]:
			fallbackType, fallbackIcon, fallbackName = "lighting", "lightbulb", "Smart Light"
		case has[KindTargetTemp]:
			fallbackType, fallbackIcon, fallbackName = "climate", "thermometer", "Smart Thermostat"
		case has[KindContact] || has[KindMotion] || has[KindTemperature] || has[KindHumidity]:
			fallbackType, fallbackIcon, fallbackName = "sensors", "sensor", "Smart Sensor"
		case has[KindPower]:
			fallbackType, fallbackIcon, fallbackName = "plug", "power_plug", "Smart Plug"
		default:
			fallbackType, fallbackIcon, fallbackName = "sensors", "sensor", "Smart Device"
		}

		icon := fallbackIcon
		defaultName := fallbackName
		if panel.Display != nil {
			if panel.Display.Icon != "" {
				icon = panel.Display.Icon
			}
			if panel.Display.DefaultName != "" {
				defaultName = panel.Display.DefaultName
			}
		}

		tileMetricDP := ""
		tileMetricFormat := ""
		if panel.TileMetric != nil && panel.TileMetric.DP != 0 {
			tileMetricDP = strconv.Itoa(panel.TileMetric.DP)
			tileMetricFormat = panel.TileMetric.Format
		} else {
			if has[KindBrightness] {
				for _, c := range caps {
					if c.Kind == KindBrightness {
						tileMetricDP = c.DP
						break
					}
				}
			}
		}

		var groups []Group
		if len(panel.Groups) > 0 {
			groups = make([]Group, len(panel.Groups))
			for i, g := range panel.Groups {
				groups[i] = Group{Title: g.Title, DPs: g.DPs}
			}
		}

		return Profile{
			ConsumerType:     fallbackType,
			Icon:             icon,
			DefaultName:      defaultName,
			Capabilities:     caps,
			TileMetricDP:     tileMetricDP,
			TileMetricFormat: tileMetricFormat,
			Theme:            panel.Theme,
			Groups:           groups,
		}
	}

	caps := []Capability{}
	for _, dp := range a.DPs {
		if c, ok := capabilityFor(dp); ok {
			caps = append(caps, c)
		}
	}

	p := Profile{Capabilities: caps}
	has := map[string]bool{}
	for _, c := range caps {
		has[c.Kind] = true
	}
	switch {
	case has[KindBrightness] || has[KindColor] || has[KindColorTemp]:
		p.ConsumerType, p.Icon, p.DefaultName = "lighting", "lightbulb", "Smart Light"
	case has[KindTargetTemp]:
		p.ConsumerType, p.Icon, p.DefaultName = "climate", "thermometer", "Smart Thermostat"
	case has[KindContact] || has[KindMotion] || has[KindTemperature] || has[KindHumidity]:
		p.ConsumerType, p.Icon, p.DefaultName = "sensors", "sensor", "Smart Sensor"
	case has[KindPower]:
		p.ConsumerType, p.Icon, p.DefaultName = "plug", "power_plug", "Smart Plug"
	default:
		p.ConsumerType, p.Icon, p.DefaultName = "sensors", "sensor", "Smart Device"
	}
	if has[KindBrightness] {
		for _, c := range caps {
			if c.Kind == KindBrightness {
				p.TileMetricDP = c.DP
				break
			}
		}
	}
	return p
}

// capabilityFor maps a single DP's semantic facet to an app capability kind.
// Returns false for DPs that are not user-facing controls/sensors (raw configs,
// metering counters, fault bitmaps, mode enums).
func capabilityFor(dp DP) (Capability, bool) {
	code := strings.ToLower(dp.Code)
	dpStr := strconv.Itoa(dp.DPID)
	label := dp.Name
	if label == "" {
		label = dp.Code
	}
	pct := func() (Capability, bool) {
		c := Capability{DP: dpStr, Kind: KindBrightness, Label: label, Unit: "%"}
		c.Min, c.Max = scaledRange(dp)
		return c, true
	}

	switch dp.Semantic.Type {
	case "bool":
		switch {
		case strings.Contains(code, "door") || strings.Contains(code, "contact"):
			return Capability{DP: dpStr, Kind: KindContact, Label: label}, true
		case strings.Contains(code, "motion") || strings.Contains(code, "pir") || strings.Contains(code, "presence"):
			return Capability{DP: dpStr, Kind: KindMotion, Label: label}, true
		case strings.Contains(code, "lock"):
			return Capability{DP: dpStr, Kind: KindLock, Label: label}, true
		case strings.HasPrefix(code, "switch") || code == "fan_switch" || strings.Contains(code, "power") || strings.Contains(code, "control"):
			return Capability{DP: dpStr, Kind: KindPower, Label: label}, true
		default:
			// A writable bool with no better match is treated as power.
			if dp.Semantic.Mode == "rw" || dp.Semantic.Mode == "wo" {
				return Capability{DP: dpStr, Kind: KindPower, Label: label}, true
			}
			return Capability{}, false
		}
	case "value":
		switch {
		case strings.Contains(code, "bright"):
			return pct()
		case strings.Contains(code, "temp_value") || strings.Contains(code, "colour_temp") || strings.Contains(code, "color_temp") || strings.Contains(code, "cct"):
			c := Capability{DP: dpStr, Kind: KindColorTemp, Label: label}
			c.Min, c.Max = scaledRange(dp)
			return c, true
		case strings.Contains(code, "temp_set") || strings.Contains(code, "target_temp") || (strings.Contains(code, "temp") && dp.Semantic.Mode == "rw"):
			c := Capability{DP: dpStr, Kind: KindTargetTemp, Label: label, Unit: dp.Semantic.Unit}
			c.Min, c.Max = scaledRange(dp)
			return c, true
		case strings.Contains(code, "humidity"):
			return Capability{DP: dpStr, Kind: KindHumidity, Label: label, Unit: "%"}, true
		case strings.Contains(code, "temperature") || code == "temp_current":
			return Capability{DP: dpStr, Kind: KindTemperature, Label: label, Unit: dp.Semantic.Unit}, true
		default:
			// Metering / counters / generic values are not control capabilities.
			return Capability{}, false
		}
	case "str":
		if strings.Contains(code, "colour") || strings.Contains(code, "color") {
			return Capability{DP: dpStr, Kind: KindColor, Label: label}, true
		}
		return Capability{}, false
	default:
		return Capability{}, false
	}
}

// scaledRange returns the DP's min/max as integers, applying the decimal scale
// so the app sees human units (e.g. bright_value 10..1000 scale 0 stays 10..1000;
// a value with scale 1 is divided by 10).
func scaledRange(dp DP) (*int, *int) {
	conv := func(p *float64) *int {
		if p == nil {
			return nil
		}
		v := *p
		if dp.Semantic.Scale != nil && *dp.Semantic.Scale > 0 {
			for i := 0; i < *dp.Semantic.Scale; i++ {
				v /= 10
			}
		}
		n := int(v)
		return &n
	}
	return conv(dp.Semantic.Min), conv(dp.Semantic.Max)
}
