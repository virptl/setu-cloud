package capability

import (
	"encoding/json"
	"fmt"
	"math"
)

// RGB is the canonical color value used by the color capability.
type RGB struct {
	R int `json:"r"`
	G int `json:"g"`
	B int `json:"b"`
}

// HSB is the Alexa color representation (hue 0-360, saturation 0-1, brightness 0-1).
type HSB struct {
	Hue        float64 `json:"hue"`
	Saturation float64 `json:"saturation"`
	Brightness float64 `json:"brightness"`
}

// RGBToHSB converts an RGB color value to Alexa's HSB format.
func RGBToHSB(rgb RGB) HSB {
	r := float64(rgb.R) / 255
	g := float64(rgb.G) / 255
	b := float64(rgb.B) / 255

	max := math.Max(r, math.Max(g, b))
	min := math.Min(r, math.Min(g, b))
	delta := max - min

	brightness := max
	saturation := 0.0
	if max != 0 {
		saturation = delta / max
	}

	hue := 0.0
	if delta != 0 {
		switch max {
		case r:
			hue = 60 * math.Mod((g-b)/delta, 6)
		case g:
			hue = 60 * ((b-r)/delta + 2)
		case b:
			hue = 60 * ((r-g)/delta + 4)
		}
	}
	if hue < 0 {
		hue += 360
	}
	return HSB{Hue: hue, Saturation: saturation, Brightness: brightness}
}

// HSBToRGB converts Alexa's HSB back to the canonical RGB format.
func HSBToRGB(hsb HSB) RGB {
	h := hsb.Hue
	s := hsb.Saturation
	v := hsb.Brightness

	c := v * s
	x := c * (1 - math.Abs(math.Mod(h/60, 2)-1))
	m := v - c

	var r, g, b float64
	switch {
	case h < 60:
		r, g, b = c, x, 0
	case h < 120:
		r, g, b = x, c, 0
	case h < 180:
		r, g, b = 0, c, x
	case h < 240:
		r, g, b = 0, x, c
	case h < 300:
		r, g, b = x, 0, c
	default:
		r, g, b = c, 0, x
	}
	return RGB{
		R: int(math.Round((r + m) * 255)),
		G: int(math.Round((g + m) * 255)),
		B: int(math.Round((b + m) * 255)),
	}
}

// ColorTempToKelvin maps our internal 0-100 scale to Kelvin (2700 K warm → 6500 K cool).
func ColorTempToKelvin(v int) int {
	return 2700 + int(float64(v)/100*3800)
}

// KelvinToColorTemp maps Kelvin back to our 0-100 scale.
func KelvinToColorTemp(k int) int {
	v := float64(k-2700) / 3800 * 100
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return int(math.Round(v))
}

// ParseRGB decodes a raw JSON DP value into an RGB struct.
func ParseRGB(raw json.RawMessage) (RGB, error) {
	var rgb RGB
	if err := json.Unmarshal(raw, &rgb); err != nil {
		return RGB{}, fmt.Errorf("parse rgb: %w", err)
	}
	return rgb, nil
}

// MarshalRGB encodes an RGB value to JSON for use as a DP value.
func MarshalRGB(rgb RGB) json.RawMessage {
	b, _ := json.Marshal(rgb)
	return b
}

// ParseFloat decodes a raw JSON DP value as a float64.
func ParseFloat(raw json.RawMessage) (float64, error) {
	var v float64
	if err := json.Unmarshal(raw, &v); err != nil {
		return 0, fmt.Errorf("parse float: %w", err)
	}
	return v, nil
}

// ParseBool decodes a raw JSON DP value as a bool.
func ParseBool(raw json.RawMessage) (bool, error) {
	var v bool
	if err := json.Unmarshal(raw, &v); err != nil {
		return false, fmt.Errorf("parse bool: %w", err)
	}
	return v, nil
}

// ParseString decodes a raw JSON DP value as a string.
func ParseString(raw json.RawMessage) (string, error) {
	var v string
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", fmt.Errorf("parse string: %w", err)
	}
	return v, nil
}
