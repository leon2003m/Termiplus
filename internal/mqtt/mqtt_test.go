package mqtt

import (
	"bufio"
	"strings"
	"testing"
	"time"

	"termiplus/internal/mqtt/mqtttest"
)

func TestConnectPublish(t *testing.T) {
	b := mqtttest.NewBroker(t, "ha", "secret")
	c, err := Dial(Options{URL: b.URL(), ClientID: "tp-test", Username: "ha", Password: "secret", WillTopic: "tp/status", WillPayload: "offline", KeepAlive: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Publish("tp/state", []byte(`{"x":1}`), true); err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("y", 300) // remaining length needs two bytes
	if err := c.Publish("tp/big", []byte(big), false); err != nil {
		t.Fatal(err)
	}
	// Survives longer than the keep-alive thanks to pings.
	time.Sleep(3 * time.Second)
	if err := c.Publish("tp/later", nil, false); err != nil {
		t.Fatalf("connection should still be alive: %v", err)
	}
	c.Close()
	<-c.Done()
	time.Sleep(100 * time.Millisecond)
	cn := b.Connects()[0]
	if cn.ClientID != "tp-test" || cn.WillTopic != "tp/status" || cn.WillPayload != "offline" || !cn.WillRetain || cn.KeepAlive != 2 {
		t.Fatalf("connect: %+v", cn)
	}
	p := b.Pubs()
	if len(p) != 3 || p[0].Topic != "tp/state" || p[0].Payload != `{"x":1}` || p[1].Payload != big || p[2].Topic != "tp/later" {
		t.Fatalf("pubs: %+v", p)
	}
}

func TestRefusedAndBadOptions(t *testing.T) {
	b := mqtttest.NewBroker(t, "ha", "secret")
	if _, err := Dial(Options{URL: b.URL(), ClientID: "x", Username: "ha", Password: "wrong"}); err == nil || !strings.Contains(err.Error(), "bad user name or password") {
		t.Fatalf("got %v", err)
	}
	for _, o := range []Options{
		{URL: "http://h", ClientID: "x"},
		{URL: "mqtt://", ClientID: "x"},
		{URL: b.URL(), ClientID: ""},
		{URL: b.URL(), ClientID: strings.Repeat("x", 24)},
	} {
		if _, err := Dial(o); err == nil {
			t.Errorf("%+v accepted", o)
		}
	}
}

func TestDoneOnBrokerLoss(t *testing.T) {
	b := mqtttest.NewBroker(t, "", "")
	c, err := Dial(Options{URL: b.URL(), ClientID: "x", KeepAlive: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	b.DropClients()
	select {
	case <-c.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("lost connection not noticed")
	}
	if err := c.Publish("t", nil, false); err == nil {
		t.Fatal("publish on a dead connection succeeded")
	}
}

func TestPacketLength(t *testing.T) {
	for _, n := range []int{0, 127, 128, 16383, 16384, 300000} {
		p := packet(0x30, make([]byte, n))
		_, body, err := readPacket(bufio.NewReader(strings.NewReader(string(p))))
		if err != nil || len(body) != n {
			t.Fatalf("%d: %v %d", n, err, len(body))
		}
	}
}
