package google

import (
	"encoding/json"
	"fmt"

	"github.com/setucore/setu-cloud/internal/app"
	"github.com/setucore/setu-cloud/internal/capability"
)

// --- Google device types ---

var consumerTypeToGoogleType = map[string]string{
	capability.TypeLighting:      "action.devices.types.LIGHT",
	capability.TypePlug:          "action.devices.types.OUTLET",
	capability.TypeClimate:       "action.devices.types.THERMOSTAT",
	capability.TypeSecurity:      "action.devices.types.SECURITYSYSTEM",
	capability.TypeSensors:       "action.devices.types.SENSOR",
	capability.TypeEntertainment: "action.devices.types.TV",
}

func googleType(consumerType string) string {
	if t, ok := consumerTypeToGoogleType[consumerType]; ok {
		return t
	}
	return "action.devices.types.SWITCH"
}

// --- Trait mapping ---

// TraitsForCaps returns the Google Home trait strings for a slice of capabilities.
func TraitsForCaps(caps []app.Capability) []string {
	seen := map[string]bool{}
	var traits []string
	add := func(t string) {
		if !seen[t] {
			seen[t] = true
			traits = append(traits, t)
		}
	}
	for _, c := range caps {
		switch c.Kind {
		case capability.KindPower:
			add("action.devices.traits.OnOff")
		case capability.KindBrightness:
			add("action.devices.traits.Brightness")
		case capability.KindColor:
			add("action.devices.traits.ColorSetting")
		case capability.KindColorTemp:
			add("action.devices.traits.ColorSetting")
		case capability.KindTargetTemp, capability.KindThermoMode:
			add("action.devices.traits.TemperatureSetting")
		case capability.KindFanSpeed:
			add("action.devices.traits.FanSpeed")
		case capability.KindLock:
			add("action.devices.traits.LockUnlock")
		case capability.KindContactSensor:
			add("action.devices.traits.OpenClose")
		case capability.KindMotionSensor:
			add("action.devices.traits.SensorState")
		case capability.KindHumidity:
			add("action.devices.traits.HumiditySetting")
		case capability.KindTemperature:
			add("action.devices.traits.TemperatureControl")
		}
	}
	return traits
}

// AttributesForCaps builds the trait-specific attributes object.
func AttributesForCaps(caps []app.Capability) map[string]any {
	attrs := map[string]any{}
	hasColor := false
	hasColorTemp := false
	for _, c := range caps {
		switch c.Kind {
		case capability.KindColor:
			hasColor = true
		case capability.KindColorTemp:
			hasColorTemp = true
		case capability.KindTargetTemp:
			min := 16.0
			max := 30.0
			if c.Min != nil {
				min = float64(*c.Min)
			}
			if c.Max != nil {
				max = float64(*c.Max)
			}
			attrs["thermostatTemperatureUnit"] = "C"
			attrs["thermostatTemperatureRange"] = map[string]float64{
				"minThresholdCelsius": min,
				"maxThresholdCelsius": max,
			}
			attrs["availableThermostatModes"] = []string{"off", "heat", "cool", "auto"}
		case capability.KindFanSpeed:
			min := 1
			max := 5
			if c.Min != nil {
				min = *c.Min
			}
			if c.Max != nil {
				max = *c.Max
			}
			speeds := make([]map[string]any, 0, max-min+1)
			for i := min; i <= max; i++ {
				speeds = append(speeds, map[string]any{
					"speed_name":  fmt.Sprintf("speed_%d", i),
					"speed_values": []map[string]any{{
						"speed_synonym": []string{fmt.Sprintf("speed %d", i)},
						"lang":          "en",
					}},
				})
			}
			attrs["availableFanSpeeds"] = map[string]any{"speeds": speeds, "ordered": true}
			attrs["reversible"] = false
		}
	}
	if hasColor || hasColorTemp {
		cs := map[string]any{}
		if hasColor {
			cs["colorModel"] = "rgb"
		}
		if hasColorTemp {
			cs["colorTemperatureRange"] = map[string]int{
				"temperatureMinK": 2700,
				"temperatureMaxK": 6500,
			}
		}
		for k, v := range cs {
			attrs[k] = v
		}
	}
	return attrs
}

// DeviceDef is a Google Home SYNC device object.
type DeviceDef struct {
	ID              string         `json:"id"`
	Type            string         `json:"type"`
	Traits          []string       `json:"traits"`
	Name            googleName     `json:"name"`
	WillReportState bool           `json:"willReportState"`
	Attributes      map[string]any `json:"attributes,omitempty"`
	DeviceInfo      googleDevInfo  `json:"deviceInfo"`
	CustomData      map[string]any `json:"customData,omitempty"`
}

type googleName struct {
	Name         string   `json:"name"`
	Nicknames    []string `json:"nicknames,omitempty"`
	DefaultNames []string `json:"defaultNames,omitempty"`
}

type googleDevInfo struct {
	Manufacturer string `json:"manufacturer"`
}

// BuildDeviceDef creates a Google SYNC DeviceDef from a device's metadata.
func BuildDeviceDef(did, pid, name, consumerType string, caps []app.Capability) DeviceDef {
	return DeviceDef{
		ID:              did,
		Type:            googleType(consumerType),
		Traits:          TraitsForCaps(caps),
		Name:            googleName{Name: name, DefaultNames: []string{name}},
		WillReportState: true,
		Attributes:      AttributesForCaps(caps),
		DeviceInfo:      googleDevInfo{Manufacturer: "SetuIoT"},
		CustomData:      map[string]any{"pid": pid},
	}
}

// --- State builders (QUERY / EXECUTE responses) ---

// DPSToState converts reported DPS to a Google device state map.
func DPSToState(caps []app.Capability, dps map[string]json.RawMessage, online bool) map[string]any {
	state := map[string]any{"online": online, "status": "SUCCESS"}
	for _, c := range caps {
		raw, ok := dps[c.DP]
		if !ok {
			continue
		}
		switch c.Kind {
		case capability.KindPower:
			var on bool
			if json.Unmarshal(raw, &on) == nil {
				state["on"] = on
			}
		case capability.KindBrightness:
			var v float64
			if json.Unmarshal(raw, &v) == nil {
				state["brightness"] = int(v)
			}
		case capability.KindColor:
			rgb, err := capability.ParseRGB(raw)
			if err == nil {
				// Google expects spectrumRgb as a 24-bit integer.
				state["color"] = map[string]any{
					"spectrumRgb": (rgb.R << 16) | (rgb.G << 8) | rgb.B,
				}
			}
		case capability.KindColorTemp:
			var v float64
			if json.Unmarshal(raw, &v) == nil {
				state["color"] = map[string]any{
					"temperatureK": capability.ColorTempToKelvin(int(v)),
				}
			}
		case capability.KindTargetTemp:
			var v float64
			if json.Unmarshal(raw, &v) == nil {
				state["thermostatTemperatureSetpoint"] = v
			}
		case capability.KindThermoMode:
			var m string
			if json.Unmarshal(raw, &m) == nil {
				state["thermostatMode"] = m
			}
		case capability.KindFanSpeed:
			var v float64
			if json.Unmarshal(raw, &v) == nil {
				state["currentFanSpeedSetting"] = fmt.Sprintf("speed_%d", int(v))
			}
		case capability.KindLock:
			var locked bool
			if json.Unmarshal(raw, &locked) == nil {
				state["isLocked"] = locked
				state["isJammed"] = false
			}
		case capability.KindHumidity:
			var v float64
			if json.Unmarshal(raw, &v) == nil {
				state["humidityAmbientPercent"] = int(v)
			}
		case capability.KindTemperature:
			var v float64
			if json.Unmarshal(raw, &v) == nil {
				state["temperatureAmbientCelsius"] = v
			}
		}
	}
	return state
}

// --- EXECUTE command → DPS translation ---

// ExecuteCommandToDPS converts a Google EXECUTE command to a dps map.
func ExecuteCommandToDPS(googleCommand string, params map[string]any, caps []app.Capability) map[string]json.RawMessage {
	kindDP := make(map[string]string, len(caps))
	for _, c := range caps {
		kindDP[c.Kind] = c.DP
	}

	dps := map[string]json.RawMessage{}

	switch googleCommand {
	case "action.devices.commands.OnOff":
		if dp, ok := kindDP[capability.KindPower]; ok {
			on, _ := params["on"].(bool)
			b, _ := json.Marshal(on)
			dps[dp] = b
		}
	case "action.devices.commands.BrightnessAbsolute":
		if dp, ok := kindDP[capability.KindBrightness]; ok {
			b, _ := json.Marshal(int(toFloat(params["brightness"])))
			dps[dp] = b
		}
	case "action.devices.commands.ColorAbsolute":
		colorMap, _ := params["color"].(map[string]any)
		if spectrumRGB, ok := colorMap["spectrumRGB"]; ok {
			rgb := int(toFloat(spectrumRGB))
			r := (rgb >> 16) & 0xFF
			g := (rgb >> 8) & 0xFF
			bCh := rgb & 0xFF
			if dp, ok2 := kindDP[capability.KindColor]; ok2 {
				dps[dp] = capability.MarshalRGB(capability.RGB{R: r, G: g, B: bCh})
			}
		}
		if tempK, ok := colorMap["temperature"]; ok {
			if dp, ok2 := kindDP[capability.KindColorTemp]; ok2 {
				v := capability.KelvinToColorTemp(int(toFloat(tempK)))
				b, _ := json.Marshal(v)
				dps[dp] = b
			}
		}
	case "action.devices.commands.ThermostatTemperatureSetpoint":
		if dp, ok := kindDP[capability.KindTargetTemp]; ok {
			b, _ := json.Marshal(toFloat(params["thermostatTemperatureSetpoint"]))
			dps[dp] = b
		}
	case "action.devices.commands.ThermostatSetMode":
		if dp, ok := kindDP[capability.KindThermoMode]; ok {
			mode, _ := params["thermostatMode"].(string)
			b, _ := json.Marshal(mode)
			dps[dp] = b
		}
	case "action.devices.commands.SetFanSpeed":
		if dp, ok := kindDP[capability.KindFanSpeed]; ok {
			// "speed_N" format
			speedName, _ := params["fanSpeed"].(string)
			var n int
			fmt.Sscanf(speedName, "speed_%d", &n)
			b, _ := json.Marshal(n)
			dps[dp] = b
		}
	case "action.devices.commands.LockUnlock":
		if dp, ok := kindDP[capability.KindLock]; ok {
			lock, _ := params["lock"].(bool)
			b, _ := json.Marshal(lock)
			dps[dp] = b
		}
	}
	return dps
}

func toFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	}
	return 0
}

