package alexa

import (
	"encoding/json"
	"fmt"

	"github.com/setucore/setu-cloud/internal/app"
	"github.com/setucore/setu-cloud/internal/capability"
)

// --- Alexa response types ---

type header struct {
	Namespace        string `json:"namespace"`
	Name             string `json:"name"`
	PayloadVersion   string `json:"payloadVersion"`
	MessageID        string `json:"messageId"`
	CorrelationToken string `json:"correlationToken,omitempty"`
}

type scope struct {
	Type  string `json:"type"`
	Token string `json:"token"`
}

type endpoint struct {
	Scope      *scope         `json:"scope,omitempty"`
	EndpointID string         `json:"endpointId"`
	Cookie     map[string]any `json:"cookie,omitempty"`
}

type property struct {
	Namespace                 string `json:"namespace"`
	Name                      string `json:"name"`
	Value                     any    `json:"value"`
	TimeOfSample              string `json:"timeOfSample"`
	UncertaintyInMilliseconds int    `json:"uncertaintyInMilliseconds"`
}

type capabilityDef struct {
	Type       string                 `json:"type"`
	Interface  string                 `json:"interface"`
	Version    string                 `json:"version"`
	Properties *capabilityProperties  `json:"properties,omitempty"`
	Instance   string                 `json:"instance,omitempty"`
	// For RangeController
	CapabilityResources *capabilityResources `json:"capabilityResources,omitempty"`
	Configuration       *rangeConfig         `json:"configuration,omitempty"`
}

type capabilityProperties struct {
	Supported             []map[string]string `json:"supported"`
	ProactivelyReported   bool                `json:"proactivelyReported"`
	Retrievable           bool                `json:"retrievable"`
}

type capabilityResources struct {
	FriendlyNames []map[string]any `json:"friendlyNames"`
}

type rangeConfig struct {
	SupportedRange map[string]int `json:"supportedRange"`
	Presets        []rangePreset  `json:"presets,omitempty"`
}

type rangePreset struct {
	RangeValue      int            `json:"rangeValue"`
	PresetResources capabilityResources `json:"presetResources"`
}

// EndpointDef is the Alexa endpoint representation sent in discovery.
type EndpointDef struct {
	EndpointID        string          `json:"endpointId"`
	ManufacturerName  string          `json:"manufacturerName"`
	Description       string          `json:"description"`
	FriendlyName      string          `json:"friendlyName"`
	DisplayCategories []string        `json:"displayCategories"`
	Cookie            map[string]any  `json:"cookie"`
	Capabilities      []capabilityDef `json:"capabilities"`
}

// --- Display category mapping ---

var consumerTypeToCategory = map[string]string{
	capability.TypeLighting:      "LIGHT",
	capability.TypePlug:          "SMARTPLUG",
	capability.TypeClimate:       "THERMOSTAT",
	capability.TypeSecurity:      "SECURITY_PANEL",
	capability.TypeSensors:       "SENSOR",
	capability.TypeEntertainment: "TV",
}

func displayCategory(consumerType string) []string {
	if cat, ok := consumerTypeToCategory[consumerType]; ok {
		return []string{cat}
	}
	return []string{"OTHER"}
}

// --- Capability → Alexa interface mapping ---

func prop(iface, name string) capabilityDef {
	return capabilityDef{
		Type:      "AlexaInterface",
		Interface: iface,
		Version:   "3",
		Properties: &capabilityProperties{
			Supported:           []map[string]string{{"name": name}},
			ProactivelyReported: true,
			Retrievable:         true,
		},
	}
}

// CapabilitiesToAlexa converts a slice of app.Capability to Alexa interface definitions.
func CapabilitiesToAlexa(caps []app.Capability) []capabilityDef {
	// Always include the base Alexa interface.
	out := []capabilityDef{
		{Type: "AlexaInterface", Interface: "Alexa", Version: "3"},
		{Type: "AlexaInterface", Interface: "Alexa.EndpointHealth", Version: "3",
			Properties: &capabilityProperties{
				Supported:           []map[string]string{{"name": "connectivity"}},
				ProactivelyReported: true,
				Retrievable:         true,
			}},
	}
	seenThermo := false
	for _, c := range caps {
		switch c.Kind {
		case capability.KindPower:
			out = append(out, prop("Alexa.PowerController", "powerState"))
		case capability.KindBrightness:
			out = append(out, prop("Alexa.BrightnessController", "brightness"))
		case capability.KindColor:
			out = append(out, prop("Alexa.ColorController", "color"))
		case capability.KindColorTemp:
			out = append(out, prop("Alexa.ColorTemperatureController", "colorTemperatureInKelvin"))
		case capability.KindTargetTemp:
			if !seenThermo {
				seenThermo = true
				out = append(out,
					prop("Alexa.ThermostatController", "targetSetpoint"),
					prop("Alexa.TemperatureSensor", "temperature"),
				)
			}
		case capability.KindThermoMode:
			// Handled by ThermostatController above; add thermostatMode if not already added.
		case capability.KindFanSpeed:
			out = append(out, fanSpeedCapability(c))
		case capability.KindLock:
			out = append(out, prop("Alexa.LockController", "lockState"))
		case capability.KindContactSensor:
			out = append(out, prop("Alexa.ContactSensor", "detectionState"))
		case capability.KindMotionSensor:
			out = append(out, prop("Alexa.MotionSensor", "detectionState"))
		case capability.KindHumidity:
			out = append(out, prop("Alexa.RangeController", "rangeValue"))
		}
	}
	return out
}

func fanSpeedCapability(c app.Capability) capabilityDef {
	min := 1
	max := 5
	if c.Min != nil {
		min = *c.Min
	}
	if c.Max != nil {
		max = *c.Max
	}
	return capabilityDef{
		Type:      "AlexaInterface",
		Interface: "Alexa.RangeController",
		Version:   "3",
		Instance:  "FanSpeed",
		Properties: &capabilityProperties{
			Supported:           []map[string]string{{"name": "rangeValue"}},
			ProactivelyReported: true,
			Retrievable:         true,
		},
		CapabilityResources: &capabilityResources{
			FriendlyNames: []map[string]any{
				{"@type": "text", "value": map[string]string{"text": "Fan Speed", "locale": "en-US"}},
			},
		},
		Configuration: &rangeConfig{
			SupportedRange: map[string]int{
				"minimumValue": min,
				"maximumValue": max,
				"precision":    1,
			},
		},
	}
}

// --- State property builders ---

// DPSToProperties converts reported DPS to Alexa context properties.
func DPSToProperties(caps []app.Capability, dps map[string]json.RawMessage, ts string) []property {
	var props []property
	for _, c := range caps {
		raw, ok := dps[c.DP]
		if !ok {
			continue
		}
		switch c.Kind {
		case capability.KindPower:
			var on bool
			if json.Unmarshal(raw, &on) == nil {
				val := "OFF"
				if on {
					val = "ON"
				}
				props = append(props, property{
					Namespace: "Alexa.PowerController", Name: "powerState",
					Value: val, TimeOfSample: ts, UncertaintyInMilliseconds: 500,
				})
			}
		case capability.KindBrightness:
			var v float64
			if json.Unmarshal(raw, &v) == nil {
				props = append(props, property{
					Namespace: "Alexa.BrightnessController", Name: "brightness",
					Value: int(v), TimeOfSample: ts, UncertaintyInMilliseconds: 500,
				})
			}
		case capability.KindColor:
			rgb, err := capability.ParseRGB(raw)
			if err == nil {
				props = append(props, property{
					Namespace: "Alexa.ColorController", Name: "color",
					Value: capability.RGBToHSB(rgb), TimeOfSample: ts, UncertaintyInMilliseconds: 500,
				})
			}
		case capability.KindColorTemp:
			var v float64
			if json.Unmarshal(raw, &v) == nil {
				props = append(props, property{
					Namespace: "Alexa.ColorTemperatureController", Name: "colorTemperatureInKelvin",
					Value: capability.ColorTempToKelvin(int(v)), TimeOfSample: ts, UncertaintyInMilliseconds: 500,
				})
			}
		case capability.KindTargetTemp:
			var v float64
			if json.Unmarshal(raw, &v) == nil {
				props = append(props, property{
					Namespace: "Alexa.ThermostatController", Name: "targetSetpoint",
					Value: map[string]any{"value": v, "scale": "CELSIUS"},
					TimeOfSample: ts, UncertaintyInMilliseconds: 500,
				})
				props = append(props, property{
					Namespace: "Alexa.TemperatureSensor", Name: "temperature",
					Value: map[string]any{"value": v, "scale": "CELSIUS"},
					TimeOfSample: ts, UncertaintyInMilliseconds: 500,
				})
			}
		case capability.KindFanSpeed:
			var v float64
			if json.Unmarshal(raw, &v) == nil {
				props = append(props, property{
					Namespace: "Alexa.RangeController", Name: "rangeValue",
					Value: int(v), TimeOfSample: ts, UncertaintyInMilliseconds: 500,
				})
			}
		case capability.KindLock:
			var locked bool
			if json.Unmarshal(raw, &locked) == nil {
				val := "UNLOCKED"
				if locked {
					val = "LOCKED"
				}
				props = append(props, property{
					Namespace: "Alexa.LockController", Name: "lockState",
					Value: val, TimeOfSample: ts, UncertaintyInMilliseconds: 500,
				})
			}
		}
	}
	// Always include connectivity.
	props = append(props, property{
		Namespace: "Alexa.EndpointHealth", Name: "connectivity",
		Value: map[string]string{"value": "OK"}, TimeOfSample: ts, UncertaintyInMilliseconds: 500,
	})
	return props
}

// --- Command payload → DPS translation ---

// DirectiveToDPS converts an Alexa directive into a dps map ready to send to the device.
func DirectiveToDPS(namespace, name string, payload json.RawMessage, caps []app.Capability) (map[string]json.RawMessage, error) {
	dpKinds := make(map[string]string, len(caps))
	kindDP := make(map[string]string, len(caps))
	for _, c := range caps {
		dpKinds[c.DP] = c.Kind
		kindDP[c.Kind] = c.DP
	}

	dps := map[string]json.RawMessage{}

	switch namespace {
	case "Alexa.PowerController":
		dp, ok := kindDP[capability.KindPower]
		if !ok {
			return nil, fmt.Errorf("device has no power capability")
		}
		on := name == "TurnOn"
		b, _ := json.Marshal(on)
		dps[dp] = b

	case "Alexa.BrightnessController":
		dp, ok := kindDP[capability.KindBrightness]
		if !ok {
			return nil, fmt.Errorf("device has no brightness capability")
		}
		var p struct {
			Brightness int `json:"brightness"`
		}
		json.Unmarshal(payload, &p)
		if name == "AdjustBrightness" {
			// Relative; we'd need current value — treat as absolute for simplicity.
			// Alexa will call SetBrightness for percentage-based control.
		}
		b, _ := json.Marshal(p.Brightness)
		dps[dp] = b

	case "Alexa.ColorController":
		dp, ok := kindDP[capability.KindColor]
		if !ok {
			return nil, fmt.Errorf("device has no color capability")
		}
		var p struct {
			Color capability.HSB `json:"color"`
		}
		json.Unmarshal(payload, &p)
		rgb := capability.HSBToRGB(p.Color)
		dps[dp] = capability.MarshalRGB(rgb)

	case "Alexa.ColorTemperatureController":
		dp, ok := kindDP[capability.KindColorTemp]
		if !ok {
			return nil, fmt.Errorf("device has no color temperature capability")
		}
		var p struct {
			ColorTemperatureInKelvin int `json:"colorTemperatureInKelvin"`
		}
		json.Unmarshal(payload, &p)
		v := capability.KelvinToColorTemp(p.ColorTemperatureInKelvin)
		b, _ := json.Marshal(v)
		dps[dp] = b

	case "Alexa.ThermostatController":
		dp, ok := kindDP[capability.KindTargetTemp]
		if !ok {
			return nil, fmt.Errorf("device has no thermostat capability")
		}
		var p struct {
			TargetSetpoint struct {
				Value float64 `json:"value"`
				Scale string  `json:"scale"`
			} `json:"targetSetpoint"`
		}
		json.Unmarshal(payload, &p)
		temp := p.TargetSetpoint.Value
		if p.TargetSetpoint.Scale == "FAHRENHEIT" {
			temp = (temp - 32) * 5 / 9
		}
		b, _ := json.Marshal(temp)
		dps[dp] = b

	case "Alexa.RangeController":
		// Fan speed or humidity.
		dp, ok := kindDP[capability.KindFanSpeed]
		if !ok {
			return nil, fmt.Errorf("device has no range capability")
		}
		var p struct {
			RangeValue int `json:"rangeValue"`
		}
		json.Unmarshal(payload, &p)
		b, _ := json.Marshal(p.RangeValue)
		dps[dp] = b

	case "Alexa.LockController":
		dp, ok := kindDP[capability.KindLock]
		if !ok {
			return nil, fmt.Errorf("device has no lock capability")
		}
		locked := name == "Lock"
		b, _ := json.Marshal(locked)
		dps[dp] = b

	default:
		return nil, fmt.Errorf("unsupported namespace: %s", namespace)
	}
	return dps, nil
}
