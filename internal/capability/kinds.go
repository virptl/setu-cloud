// Package capability defines the canonical IoT capability kinds and helpers
// that drive the Alexa, Google Home, and future Matter adapters.
// All adapters read from this layer; device/MQTT code never references it.
package capability

// Canonical capability kind constants.
// These must match the Kind field in app.Capability / app.productProfiles.
const (
	KindPower         = "power"        // bool — on/off
	KindBrightness    = "brightness"   // int 0-100 %
	KindColor         = "color"        // map {r,g,b} 0-255
	KindColorTemp     = "color_temp"   // int 0-100 (mapped to Kelvin by adapters)
	KindTargetTemp    = "target_temp"  // float °C
	KindThermoMode    = "thermo_mode"  // string: auto|cool|heat|off
	KindFanSpeed      = "fan_speed"    // int 1-N steps
	KindLock          = "lock"         // bool true=locked
	KindContactSensor = "contact"      // bool true=open
	KindMotionSensor  = "motion"       // bool true=detected
	KindHumidity      = "humidity"     // int 0-100 %
	KindTemperature   = "temperature"  // float °C (read-only sensor)
)

// ConsumerType constants mirror app.productProfiles[*].ConsumerType.
const (
	TypeLighting      = "lighting"
	TypePlug          = "plug"
	TypeClimate       = "climate"
	TypeSecurity      = "security"
	TypeSensors       = "sensors"
	TypeEntertainment = "entertainment"
)
