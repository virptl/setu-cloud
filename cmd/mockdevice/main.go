package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	pahomqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/setucore/setu-cloud/internal/mqtt"
)

func main() {
	broker := env("MQTT_BROKER_URL", "tcp://localhost:1883")
	user := os.Getenv("MQTT_USERNAME")
	pass := os.Getenv("MQTT_PASSWORD")
	ca := os.Getenv("MQTT_CA_CERT_FILE")
	tid := env("TID", "setu")
	pid := env("PID", "sp1")
	did := env("DID", "")
	if did == "" {
		log.Fatal("set DID=<the claimed device id>")
	}

	upTopic := fmt.Sprintf("setu/%s/%s/%s/up", tid, pid, did)
	dnTopic := fmt.Sprintf("setu/%s/%s/%s/dn", tid, pid, did)
	shdTopic := fmt.Sprintf("setu/%s/%s/%s/shd", tid, pid, did)

	// LWT — EMQX delivers this to upTopic if this client drops without a clean disconnect.
	lwtPayload, _ := json.Marshal(map[string]any{
		"v": "1", "c": "offline", "id": "lwt",
		"t": time.Now().Unix(), "pid": pid,
	})

	// clientid = did: with shared per-tenant credentials the broker identifies
	// the device by its clientid, not the (now tenant-wide) username.
	client, err := mqtt.NewClient(broker, did, user, pass, ca,
		mqtt.WithLWT(upTopic, lwtPayload))
	if err != nil {
		log.Fatal(err)
	}

	state := map[string]any{"1": false}

	publishShadow := func() {
		b, _ := json.Marshal(map[string]any{
			"v": "1", "t": time.Now().Unix(),
			"online": true, "pid": pid, "did": did, "dps": state,
		})
		client.Publish(shdTopic, 1, true, b) // retained
	}

	client.Subscribe(dnTopic, 1, func(_ pahomqtt.Client, m pahomqtt.Message) {
		log.Printf("◀ /dn  %s", string(m.Payload()))
		var env struct {
			ID string                     `json:"id"`
			C  string                     `json:"c"`
			D  map[string]json.RawMessage `json:"d"`
		}
		json.Unmarshal(m.Payload(), &env)
		if env.C == "set" {
			for k, v := range env.D {
				var val any
				json.Unmarshal(v, &val)
				state[k] = val
			}
			ack, _ := json.Marshal(map[string]any{
				"v": "1", "c": "ack", "id": env.ID,
				"t": time.Now().Unix(), "pid": pid, "d": map[string]bool{"ok": true},
			})
			client.Publish(upTopic, 1, false, ack)
			rpt, _ := json.Marshal(map[string]any{
				"v": "1", "c": "rpt", "id": env.ID,
				"t": time.Now().Unix(), "pid": pid, "d": state,
			})
			client.Publish(upTopic, 0, false, rpt)
			publishShadow()
			log.Printf("▶ applied %v, reported back", state)
		}
	})

	// Announce online: boot event + retained shadow
	boo, _ := json.Marshal(map[string]any{
		"v": "1", "c": "boo", "id": "boot",
		"t": time.Now().Unix(), "pid": pid, "d": map[string]any{"rssi": -55},
	})
	client.Publish(upTopic, 1, false, boo)
	publishShadow()
	log.Printf("mock device online: tid=%s pid=%s did=%s — LWT registered on %s", tid, pid, did, upTopic)

	q := make(chan os.Signal, 1)
	signal.Notify(q, syscall.SIGINT, syscall.SIGTERM)
	<-q

	// Clean shutdown — publish explicit offline before disconnecting so the
	// server marks the device offline immediately rather than waiting for LWT.
	offline, _ := json.Marshal(map[string]any{
		"v": "1", "c": "offline", "id": "shutdown",
		"t": time.Now().Unix(), "pid": pid,
	})
	client.Publish(upTopic, 1, false, offline)
	time.Sleep(200 * time.Millisecond) // let the publish flush
	client.Disconnect(500)
	log.Println("mock device disconnected cleanly")
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
