package mqtt

import (
	"log"

	pahomqtt "github.com/eclipse/paho.mqtt.golang"
)

// Subscribe attaches the router handlers to the MQTT broker.
// Called once after the client connects.
func Subscribe(client pahomqtt.Client, router *Router) {
	subs := map[string]pahomqtt.MessageHandler{
		"setu/+/+/+/up":  router.HandleUp,
		"setu/+/+/+/shd": router.HandleShd,
	}

	for topic, handler := range subs {
		tok := client.Subscribe(topic, 1, handler)
		tok.Wait()
		if err := tok.Error(); err != nil {
			log.Fatalf("mqtt subscribe %s: %v", topic, err)
		}
		log.Printf("mqtt subscribed: %s", topic)
	}
}
