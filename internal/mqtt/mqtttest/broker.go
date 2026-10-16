// Package mqtttest is a tiny fake MQTT 3.1.1 broker for tests: it checks
// credentials, answers pings and records CONNECTs and PUBLISHes.
package mqtttest

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
)

// Broker is a tiny fake MQTT broker for tests: it checks credentials and
// records what clients publish.
type Broker struct {
	User, Pass string

	mu       sync.Mutex
	connects []Connect
	pubs     []Pub
	ln       net.Listener
	conns    []net.Conn
}

type Connect struct {
	ClientID, User, Pass, WillTopic, WillPayload string
	KeepAlive                                    int
	WillRetain                                   bool
}

type Pub struct {
	Topic, Payload string
	Retain         bool
}

func NewBroker(t testing.TB, user, pass string) *Broker {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	b := &Broker{User: user, Pass: pass, ln: ln}
	go b.serve()
	t.Cleanup(b.Close)
	return b
}

func (b *Broker) URL() string { return "mqtt://" + b.ln.Addr().String() }

func (b *Broker) Close() {
	b.ln.Close()
	b.mu.Lock()
	for _, c := range b.conns {
		c.Close()
	}
	b.mu.Unlock()
}

// DropClients closes every client connection (a broker restart).
func (b *Broker) DropClients() {
	b.mu.Lock()
	for _, c := range b.conns {
		c.Close()
	}
	b.conns = nil
	b.mu.Unlock()
}

func (b *Broker) Pubs() []Pub {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]Pub(nil), b.pubs...)
}

func (b *Broker) Connects() []Connect {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]Connect(nil), b.connects...)
}

func (b *Broker) serve() {
	for {
		c, err := b.ln.Accept()
		if err != nil {
			return
		}
		b.mu.Lock()
		b.conns = append(b.conns, c)
		b.mu.Unlock()
		go b.handle(c)
	}
}

func str(b []byte) (string, []byte) {
	n := int(binary.BigEndian.Uint16(b))
	return string(b[2 : 2+n]), b[2+n:]
}

func (b *Broker) handle(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	typ, body, err := readPacket(r)
	if err != nil || typ != 1 {
		return
	}
	var cn Connect
	proto, rest := str(body)
	if proto != "MQTT" || rest[0] != 4 {
		_, _ = c.Write([]byte{0x20, 2, 0, 1})
		return
	}
	flags := rest[1]
	cn.KeepAlive = int(binary.BigEndian.Uint16(rest[2:4]))
	cn.ClientID, rest = str(rest[4:])
	if flags&0x04 != 0 {
		cn.WillTopic, rest = str(rest)
		cn.WillPayload, rest = str(rest)
		cn.WillRetain = flags&0x20 != 0
	}
	if flags&0x80 != 0 {
		cn.User, rest = str(rest)
	}
	if flags&0x40 != 0 {
		cn.Pass, _ = str(rest)
	}
	b.mu.Lock()
	b.connects = append(b.connects, cn)
	b.mu.Unlock()
	if cn.User != b.User || cn.Pass != b.Pass {
		_, _ = c.Write([]byte{0x20, 2, 0, 4})
		return
	}
	_, _ = c.Write([]byte{0x20, 2, 0, 0})
	for {
		typ, body, err := readPacket(r)
		if err != nil {
			return
		}
		switch typ {
		case 3:
			topic, payload := str(body)
			b.mu.Lock()
			b.pubs = append(b.pubs, Pub{Topic: topic, Payload: string(payload)})
			b.mu.Unlock()
		case 12:
			_, _ = c.Write([]byte{0xd0, 0})
		case 14:
			return
		}
	}
}

func readPacket(r *bufio.Reader) (byte, []byte, error) {
	h, err := r.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	n, mult := 0, 1
	for i := 0; ; i++ {
		if i == 4 {
			return 0, nil, errors.New("bad length")
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
	body := make([]byte, n)
	_, err = io.ReadFull(r, body)
	return h >> 4, body, err
}
