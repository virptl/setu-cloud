package mqtt

import (
	"log"

	pahomqtt "github.com/eclipse/paho.mqtt.golang"
)

// Subscribe attaches the router handlers to the MQTT broker.
// Called once after the client connects.
func Subscribe(client pahomqtt.Client, router *Router) {
	SubscribeWithGroup(client, router, "")
}

// SubscribeWithGroup attaches router handlers, optionally using MQTT 5.0 / EMQX
// shared subscriptions ($share/{group}/topic) to load balance telemetry
// processing across multiple setu-cloud server instances.
func SubscribeWithGroup(client pahomqtt.Client, router *Router, sharedGroup string) {
	telemetryPrefix := ""
	if sharedGroup != "" {
		telemetryPrefix = "$share/" + sharedGroup + "/"
	}

	subs := map[string]pahomqtt.MessageHandler{
		// Device telemetry — use shared subscription if group is set
		telemetryPrefix + "setu/+/+/+/up":  router.HandleUp,
		telemetryPrefix + "setu/+/+/+/shd": router.HandleShd,

		// EMQX system events — fired on every client connect/disconnect
		// regardless of whether the device publishes any data.
		// This is the reliable source of online truth for idle devices.
		"$SYS/brokers/+/clients/+/connected":    router.HandleSysConnected,
		"$SYS/brokers/+/clients/+/disconnected": router.HandleSysDisconnected,
	}

	for topic, handler := range subs {
		tok := client.Subscribe(topic, 1, handler)
		tok.Wait()
		if err := tok.Error(); err != nil {
			// $SYS topics may not be enabled on all brokers — warn but don't fatal.
			if len(topic) > 0 && topic[0] == '$' && sharedGroup == "" {
				log.Printf("mqtt: WARNING — could not subscribe to %s: %v (EMQX $SYS events disabled?)", topic, err)
			} else {
				log.Fatalf("mqtt subscribe %s: %v", topic, err)
			}
		} else {
			log.Printf("mqtt subscribed: %s", topic)
		}
	}
}
