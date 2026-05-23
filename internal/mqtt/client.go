package mqtt

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"time"

	pahomqtt "github.com/eclipse/paho.mqtt.golang"
)

// NewClient creates and connects a Paho MQTT client.
// caCertFile may be empty — if so, TLS uses the system root pool.
func NewClient(brokerURL, clientID, username, password, caCertFile string) (pahomqtt.Client, error) {
	opts := pahomqtt.NewClientOptions()
	opts.AddBroker(brokerURL)
	opts.SetClientID(clientID)
	opts.SetCleanSession(false)
	opts.SetAutoReconnect(true)
	opts.SetConnectRetry(true)
	opts.SetConnectRetryInterval(5 * time.Second)
	opts.SetKeepAlive(30 * time.Second)

	if username != "" {
		opts.SetUsername(username)
		opts.SetPassword(password)
	}

	tlsCfg, err := buildTLS(caCertFile)
	if err != nil {
		return nil, err
	}
	opts.SetTLSConfig(tlsCfg)

	client := pahomqtt.NewClient(opts)
	token := client.Connect()
	token.Wait()
	if err := token.Error(); err != nil {
		return nil, fmt.Errorf("mqtt connect: %w", err)
	}
	return client, nil
}

func buildTLS(caCertFile string) (*tls.Config, error) {
	cfg := &tls.Config{}
	if caCertFile == "" {
		return cfg, nil
	}
	pem, err := os.ReadFile(caCertFile)
	if err != nil {
		return nil, fmt.Errorf("read CA cert %s: %w", caCertFile, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("invalid CA cert PEM in %s", caCertFile)
	}
	cfg.RootCAs = pool
	return cfg, nil
}
