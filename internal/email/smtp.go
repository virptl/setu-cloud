package email

import (
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"net/textproto"
	"strings"
)

// SendOTP delivers a 6-digit OTP via direct TLS SMTP (port 465, IPv4).
// Uses raw textproto to control the EHLO hostname — Go's smtp.Client sends
// "EHLO localhost" which causes Google's relay to do a slow DNS lookup and drop.
func SendOTP(host, port, user, password, from, to, code string) error {
	addr := net.JoinHostPort(host, port)

	msg := fmt.Sprintf(
		"From: Setu <%s>\r\nTo: %s\r\nSubject: Your verification code\r\n\r\nYour OTP is: %s\r\n\r\nThis code expires in 10 minutes. Do not share it with anyone.",
		from, to, code,
	)

	// Force IPv4 so the relay matches the whitelisted IP (187.127.166.16).
	tlsConn, err := tls.DialWithDialer(
		&net.Dialer{},
		"tcp4",
		addr,
		&tls.Config{ServerName: host},
	)
	if err != nil {
		return fmt.Errorf("tls dial: %w", err)
	}
	defer tlsConn.Close()

	tp := textproto.NewConn(tlsConn)
	defer tp.Close()

	if _, _, err = tp.ReadResponse(220); err != nil {
		return fmt.Errorf("greeting: %w", err)
	}

	// Use the sending domain for EHLO to avoid relay's reverse-DNS timeout on "localhost".
	ehloHost := host
	if i := strings.LastIndex(from, "@"); i >= 0 {
		ehloHost = from[i+1:]
	}

	if err = cmd(tp, 250, "EHLO %s", ehloHost); err != nil {
		return fmt.Errorf("EHLO: %w", err)
	}

	if user != "" {
		plain := base64.StdEncoding.EncodeToString([]byte("\x00" + user + "\x00" + password))
		if err = cmd(tp, 235, "AUTH PLAIN %s", plain); err != nil {
			return fmt.Errorf("AUTH: %w", err)
		}
	}

	if err = cmd(tp, 250, "MAIL FROM:<%s>", from); err != nil {
		return fmt.Errorf("MAIL FROM: %w", err)
	}
	if err = cmd(tp, 250, "RCPT TO:<%s>", to); err != nil {
		return fmt.Errorf("RCPT TO: %w", err)
	}
	if err = cmd(tp, 354, "DATA"); err != nil {
		return fmt.Errorf("DATA: %w", err)
	}

	w := tp.DotWriter()
	if _, err = fmt.Fprint(w, msg); err != nil {
		return fmt.Errorf("write body: %w", err)
	}
	if err = w.Close(); err != nil {
		return fmt.Errorf("end data: %w", err)
	}
	if _, _, err = tp.ReadResponse(250); err != nil {
		return fmt.Errorf("message accepted: %w", err)
	}

	tp.Cmd("QUIT")
	return nil
}

func cmd(tp *textproto.Conn, expectCode int, format string, args ...any) error {
	id, _ := tp.Cmd(format, args...)
	tp.StartResponse(id)
	defer tp.EndResponse(id)
	_, _, err := tp.ReadResponse(expectCode)
	return err
}
