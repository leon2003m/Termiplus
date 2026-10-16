// Package mqtt is a minimal MQTT 3.1.1 publisher: connect (optionally over
// TLS, with username/password and a last-will message), publish at QoS 0,
// keep the connection alive and disconnect. It cannot subscribe. That is all
// the gateway needs to report its metrics to Home Assistant or any broker.
package mqtt

import (
	"bufio"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"sync"
	"time"
)

// Options configure a connection.
type Options struct {
	// URL is mqtt://host[:1883] or mqtts://host[:8883].
	URL      string
	ClientID string
	Username string
	Password string
	// Will is published by the broker (retained) when the connection drops
	// without a DISCONNECT.
	WillTopic, WillPayload string
	KeepAlive              time.Duration // default 60s
	TLS                    *tls.Config   // for mqtts://; nil uses system roots
	DialTimeout            time.Duration // default 10s
}

// Client is one connection. Publish is safe for concurrent use.
type Client struct {
	conn net.Conn
	mu   sync.Mutex // serializes writes
	done chan struct{}
	err  error
}

const (
	typeConnect    = 1
	typeConnack    = 2
	typePublish    = 3
	typePingreq    = 12
	typePingresp   = 13
	typeDisconnect = 14
)

// Dial connects and waits for the broker's CONNACK.
func Dial(o Options) (*Client, error) {
	u, err := url.Parse(o.URL)
	if err != nil {
		return nil, fmt.Errorf("mqtt url: %w", err)
	}
	if o.ClientID == "" || len(o.ClientID) > 23 {
		return nil, errors.New("mqtt client id must be 1-23 characters")
	}
	if o.KeepAlive <= 0 {
		o.KeepAlive = 60 * time.Second
	}
	if o.DialTimeout <= 0 {
		o.DialTimeout = 10 * time.Second
	}
	host := u.Hostname()
	if host == "" {
		return nil, errors.New("mqtt url needs a host")
	}
	var conn net.Conn
	d := &net.Dialer{Timeout: o.DialTimeout}
	switch u.Scheme {
	case "mqtt", "tcp":
		conn, err = d.Dial("tcp", net.JoinHostPort(host, portOr(u, "1883")))
	case "mqtts", "ssl", "tls":
		cfg := o.TLS
		if cfg == nil {
			cfg = &tls.Config{MinVersion: tls.VersionTLS12}
		}
		cfg = cfg.Clone()
		if cfg.ServerName == "" {
			cfg.ServerName = host
		}
		conn, err = tls.DialWithDialer(d, "tcp", net.JoinHostPort(host, portOr(u, "8883")), cfg)
	default:
		return nil, fmt.Errorf("mqtt url scheme must be mqtt or mqtts, got %q", u.Scheme)
	}
	if err != nil {
		return nil, err
	}

	_ = conn.SetDeadline(time.Now().Add(o.DialTimeout))
	if _, err := conn.Write(connectPacket(o)); err != nil {
		conn.Close()
		return nil, err
	}
	br := bufio.NewReader(conn)
	typ, body, err := readPacket(br)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("mqtt connack: %w", err)
	}
	if typ != typeConnack || len(body) != 2 {
		conn.Close()
		return nil, errors.New("mqtt: expected CONNACK")
	}
	if body[1] != 0 {
		conn.Close()
		return nil, fmt.Errorf("mqtt: connection refused (%s)", connackReason(body[1]))
	}
	_ = conn.SetDeadline(time.Time{})

	c := &Client{conn: conn, done: make(chan struct{})}
	go c.readLoop(br, o.KeepAlive)
	go c.pingLoop(o.KeepAlive)
	return c, nil
}

func portOr(u *url.URL, def string) string {
	if p := u.Port(); p != "" {
		return p
	}
	return def
}

func connackReason(rc byte) string {
	switch rc {
	case 1:
		return "unacceptable protocol version"
	case 2:
		return "client id rejected"
	case 3:
		return "server unavailable"
	case 4:
		return "bad user name or password"
	case 5:
		return "not authorized"
	}
	return "code " + strconv.Itoa(int(rc))
}

func connectPacket(o Options) []byte {
	var vh []byte
	vh = appendString(vh, "MQTT")
	vh = append(vh, 4)  // protocol level 3.1.1
	flags := byte(0x02) // clean session
	if o.WillTopic != "" {
		flags |= 0x04 | 0x20 // will, retained, QoS 0
	}
	if o.Username != "" {
		flags |= 0x80
		if o.Password != "" {
			flags |= 0x40
		}
	}
	vh = append(vh, flags)
	vh = binary.BigEndian.AppendUint16(vh, uint16(o.KeepAlive/time.Second))
	vh = appendString(vh, o.ClientID)
	if o.WillTopic != "" {
		vh = appendString(vh, o.WillTopic)
		vh = appendString(vh, o.WillPayload)
	}
	if o.Username != "" {
		vh = appendString(vh, o.Username)
		if o.Password != "" {
			vh = appendString(vh, o.Password)
		}
	}
	return packet(typeConnect<<4, vh)
}

func appendString(b []byte, s string) []byte {
	b = binary.BigEndian.AppendUint16(b, uint16(len(s)))
	return append(b, s...)
}

func packet(header byte, body []byte) []byte {
	out := []byte{header}
	n := len(body)
	for {
		d := byte(n % 128)
		n /= 128
		if n > 0 {
			d |= 0x80
		}
		out = append(out, d)
		if n == 0 {
			break
		}
	}
	return append(out, body...)
}

// readPacket reads one control packet (at most 1 MiB).
func readPacket(r *bufio.Reader) (byte, []byte, error) {
	h, err := r.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	n, mult := 0, 1
	for i := 0; ; i++ {
		if i == 4 {
			return 0, nil, errors.New("mqtt: bad remaining length")
		}
		d, err := r.ReadByte()
		if err != nil {
			return 0, nil, err
		}
		n += int(d&0x7f) * mult
		mult *= 128
		if d&0x80 == 0 {
			break
		}
	}
	if n > 1<<20 {
		return 0, nil, errors.New("mqtt: packet too large")
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return 0, nil, err
	}
	return h >> 4, body, nil
}

// readLoop drains what the broker sends (PINGRESP; nothing else is expected)
// and notices a dead connection: nothing at all for 1.5 keep-alive periods.
func (c *Client) readLoop(r *bufio.Reader, keepAlive time.Duration) {
	for {
		_ = c.conn.SetReadDeadline(time.Now().Add(keepAlive * 3 / 2))
		if _, _, err := readPacket(r); err != nil {
			c.fail(err)
			return
		}
	}
}

func (c *Client) pingLoop(keepAlive time.Duration) {
	t := time.NewTicker(keepAlive / 2)
	defer t.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-t.C:
			if err := c.write([]byte{typePingreq << 4, 0}); err != nil {
				c.fail(err)
				return
			}
		}
	}
}

func (c *Client) fail(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	select {
	case <-c.done:
	default:
		c.err = err
		close(c.done)
		c.conn.Close()
	}
}

func (c *Client) write(b []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	select {
	case <-c.done:
		if c.err != nil {
			return c.err
		}
		return net.ErrClosed
	default:
	}
	_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, err := c.conn.Write(b)
	return err
}

// Publish sends payload to topic at QoS 0.
func (c *Client) Publish(topic string, payload []byte, retain bool) error {
	if topic == "" || len(topic) > 65535 {
		return errors.New("mqtt: invalid topic")
	}
	h := byte(typePublish << 4)
	if retain {
		h |= 0x01
	}
	body := appendString(nil, topic)
	body = append(body, payload...)
	if len(body) > 1<<20 {
		return errors.New("mqtt: payload too large")
	}
	if err := c.write(packet(h, body)); err != nil {
		c.fail(err)
		return err
	}
	return nil
}

// Done is closed when the connection is lost or closed.
func (c *Client) Done() <-chan struct{} { return c.done }

// Close disconnects cleanly (the broker does not send the will).
func (c *Client) Close() error {
	_ = c.write([]byte{typeDisconnect << 4, 0})
	c.fail(net.ErrClosed)
	return nil
}
