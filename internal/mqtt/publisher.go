package mqtt

import (
	"encoding/json"
	"fmt"
	"time"

	pahomqtt "github.com/eclipse/paho.mqtt.golang"
)

// Publisher publishes downstream commands to devices.
type Publisher struct {
	client pahomqtt.Client
}

func NewPublisher(client pahomqtt.Client) *Publisher {
	return &Publisher{client: client}
}

// dnPayload is the standard downstream command envelope.
type dnPayload struct {
	V  string          `json:"v"`
	C  string          `json:"c"`
	ID string          `json:"id"`
	T  int64           `json:"t"`
	D  json.RawMessage `json:"d,omitempty"`
}

// Publish sends a command to a specific device.
// cmdID must be the UUID from the commands table.
// d is the command-specific payload (may be nil for "get").
func (p *Publisher) Publish(tid, pid, did, cmdType, cmdID string, d json.RawMessage) error {
	topic := fmt.Sprintf("setu/%s/%s/%s/dn", tid, pid, did)
	payload := dnPayload{
		V:  "1",
		C:  cmdType,
		ID: cmdID,
		T:  time.Now().Unix(),
		D:  d,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	tok := p.client.Publish(topic, 1, false, b)
	tok.Wait()
	return tok.Error()
}
