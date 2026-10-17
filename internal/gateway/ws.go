package gateway

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Open WebUI's terminal WebSocket uses first-message auth: the first client
// frame is a JSON text frame {"type":"auth","token":"..."}. The gateway reads
// that frame, checks the token against its own key, then opens the upstream
// connection itself and sends the same message with the container's key.
// After that, frames are copied unchanged in both directions.

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

func acceptKey(key string) string {
	h := sha1.Sum([]byte(key + wsGUID))
	return base64.StdEncoding.EncodeToString(h[:])
}

func (g *Gateway) serveWebSocket(w http.ResponseWriter, r *http.Request, userID, sessionID, policyName string, pol Policy, rest string) {
	if r.Method != http.MethodGet || r.Header.Get("Sec-WebSocket-Version") != "13" {
		httpError(w, http.StatusBadRequest, "unsupported websocket request")
		return
	}
	clientKey := r.Header.Get("Sec-WebSocket-Key")
	if k, err := base64.StdEncoding.DecodeString(clientKey); err != nil || len(k) != 16 {
		httpError(w, http.StatusBadRequest, "bad Sec-WebSocket-Key")
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		httpError(w, http.StatusInternalServerError, "hijack unsupported")
		return
	}
	conn, brw, err := hj.Hijack()
	if err != nil {
		return
	}
	defer conn.Close()

	// No extensions and no subprotocol are negotiated on either side, so
	// frames can be passed through without re-encoding.
	fmt.Fprintf(brw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", acceptKey(clientKey))
	if err := brw.Flush(); err != nil {
		return
	}

	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	payload, err := readClientTextFrame(brw.Reader, 4096)
	if err != nil {
		writeClose(conn, 4001, "Auth timeout or invalid payload")
		return
	}
	_ = conn.SetReadDeadline(time.Time{})

	var auth map[string]any
	if json.Unmarshal(payload, &auth) != nil || auth["type"] != "auth" {
		writeClose(conn, 4001, "Invalid auth message")
		return
	}
	tok, _ := auth["token"].(string)
	if !g.tokenOK(tok) {
		writeClose(conn, 4001, "Invalid API key")
		return
	}

	// Only now, after authentication, may a container be started.
	inst, err := g.mgr.ensure(r.Context(), userID, sessionID, policyName, pol)
	if err != nil {
		log.Printf("ws ensure %s/%s: %v", userID, policyName, err)
		writeClose(conn, 1011, "Terminal unavailable")
		return
	}

	up, upReader, err := dialUpstream(inst.addr, rest, r.URL.RawQuery, r.Header)
	if err != nil {
		log.Printf("ws dial %s: %v", inst.name, err)
		g.mgr.invalidate(inst.name)
		writeClose(conn, 1011, "Terminal unavailable")
		return
	}
	defer up.Close()

	auth["token"] = inst.key
	msg, _ := json.Marshal(auth)
	if err := writeMaskedText(up, msg); err != nil {
		writeClose(conn, 1011, "Terminal unavailable")
		return
	}

	g.mgr.touch(inst, +1)
	defer g.mgr.touch(inst, -1)

	var once sync.Once
	done := make(chan struct{})
	closeBoth := func() { once.Do(func() { conn.Close(); up.Close(); close(done) }) }
	go func() { _, _ = io.Copy(up, brw.Reader); closeBoth() }() // client -> terminal
	go func() { _, _ = io.Copy(conn, upReader); closeBoth() }() // terminal -> client

	// Keep the instance marked as used while the session is open.
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			g.mgr.touch(inst, 0)
		}
	}
}

var forwardHeaders = []string{"X-User-Id", "X-Session-Id", "X-Terminal-Context-Id"}

func dialUpstream(addr, path, rawQuery string, in http.Header) (net.Conn, *bufio.Reader, error) {
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return nil, nil, err
	}
	keyBytes := make([]byte, 16)
	_, _ = rand.Read(keyBytes)
	key := base64.StdEncoding.EncodeToString(keyBytes)

	u := (&url.URL{Path: path, RawQuery: rawQuery}).RequestURI()
	var b strings.Builder
	fmt.Fprintf(&b, "GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: %s\r\n", u, addr, key)
	for _, h := range forwardHeaders {
		if v := in.Get(h); v != "" && !strings.ContainsAny(v, "\r\n") {
			fmt.Fprintf(&b, "%s: %s\r\n", h, v)
		}
	}
	b.WriteString("\r\n")

	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.WriteString(conn, b.String()); err != nil {
		conn.Close()
		return nil, nil, err
	}
	// The container controls this response: read the handshake through a
	// small byte budget so an endless status line or header cannot fill the
	// shared gateway's memory. The limit is lifted for the frames afterwards.
	lr := &io.LimitedReader{R: conn, N: maxUpgradeResponse}
	br := bufio.NewReader(lr)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodGet})
	if err != nil {
		conn.Close()
		return nil, nil, errors.New("invalid upgrade response from terminal")
	}
	if resp.StatusCode != http.StatusSwitchingProtocols || resp.Header.Get("Sec-WebSocket-Accept") != acceptKey(key) {
		conn.Close()
		return nil, nil, fmt.Errorf("upstream refused upgrade: %d", resp.StatusCode)
	}
	if resp.Header.Get("Sec-WebSocket-Extensions") != "" {
		conn.Close()
		return nil, nil, errors.New("upstream negotiated an extension")
	}
	_ = conn.SetDeadline(time.Time{})
	lr.N = math.MaxInt64
	return conn, br, nil
}

// maxUpgradeResponse bounds the terminal's WebSocket handshake response.
const maxUpgradeResponse = 64 << 10

// readClientTextFrame reads one complete, masked, unfragmented text frame.
func readClientTextFrame(r io.Reader, max int) ([]byte, error) {
	var h [2]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return nil, err
	}
	fin, op, masked := h[0]&0x80 != 0, h[0]&0x0f, h[1]&0x80 != 0
	if !fin || op != 0x1 || !masked || h[0]&0x70 != 0 {
		return nil, errors.New("expected a single masked text frame")
	}
	n := int(h[1] & 0x7f)
	switch n {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return nil, err
		}
		n = int(binary.BigEndian.Uint16(ext[:]))
	case 127:
		return nil, errors.New("frame too large")
	}
	if n > max {
		return nil, errors.New("frame too large")
	}
	var mask [4]byte
	if _, err := io.ReadFull(r, mask[:]); err != nil {
		return nil, err
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	for i := range buf {
		buf[i] ^= mask[i%4]
	}
	return buf, nil
}

// writeMaskedText writes a client-to-server text frame (clients must mask).
func writeMaskedText(w io.Writer, p []byte) error {
	var hdr []byte
	switch {
	case len(p) < 126:
		hdr = []byte{0x81, 0x80 | byte(len(p))}
	case len(p) <= 0xffff:
		hdr = []byte{0x81, 0x80 | 126, byte(len(p) >> 8), byte(len(p))}
	default:
		return errors.New("message too large")
	}
	var mask [4]byte
	_, _ = rand.Read(mask[:])
	out := make([]byte, 0, len(hdr)+4+len(p))
	out = append(out, hdr...)
	out = append(out, mask[:]...)
	for i, c := range p {
		out = append(out, c^mask[i%4])
	}
	_, err := w.Write(out)
	return err
}

// writeClose sends an unmasked server-to-client close frame.
func writeClose(w io.Writer, code uint16, reason string) {
	if len(reason) > 120 {
		reason = reason[:120]
	}
	p := make([]byte, 2+len(reason))
	binary.BigEndian.PutUint16(p, code)
	copy(p[2:], reason)
	_, _ = w.Write(append([]byte{0x88, byte(len(p))}, p...))
}
