package isolationproxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Config must be generated from trusted namespace/policy state, never an agent
// file. Routes are explicit qualified grants, with no provider default/fallback.
type Config struct {
	Token            string
	Routes           []Route
	AllowedTools     []string
	CACertificatePEM []byte
	CAPrivateKeyPEM  []byte
	MaxRequestBytes  int64
	MaxResponseBytes int64
	MaxDuration      time.Duration
	MaxConnections   int
}

type Proxy struct {
	token                             string
	routes                            []Route
	allowedTools                      map[string]bool
	ca                                []byte
	certificates                      map[string]tls.Certificate
	transport                         http.RoundTripper
	maxRequestBytes, maxResponseBytes int64
	maxDuration                       time.Duration
	slots                             chan struct{}
	mu                                sync.Mutex
	connections                       map[net.Conn]struct{}
	closed                            bool
}

func New(c Config) (*Proxy, error) {
	if len(c.Token) < 32 || len(c.Token) > 512 || strings.ContainsAny(c.Token, "\r\n:") {
		return nil, errors.New("relay requires an opaque capability of at least 32 bytes")
	}
	if len(c.Routes) == 0 || len(c.Routes) > 32 {
		return nil, errors.New("relay requires explicit bounded routes")
	}
	hosts := map[string]bool{}
	routes := append([]Route(nil), c.Routes...)
	seen := map[string]bool{}
	for i, r := range routes {
		if err := validateRoute(r); err != nil {
			return nil, err
		}
		key := r.Host + " " + r.Method + " " + r.Path + "?" + r.Query
		if seen[key] {
			return nil, errors.New("duplicate route")
		}
		seen[key] = true
		hosts[r.Host] = true
		routes[i].JSONFields = append([]string(nil), r.JSONFields...)
	}
	cert, key := c.CACertificatePEM, c.CAPrivateKeyPEM
	if len(cert) == 0 && len(key) == 0 {
		var err error
		cert, key, err = GenerateCA()
		if err != nil {
			return nil, err
		}
	}
	certificates, err := leafCertificates(cert, key, hosts)
	if err != nil {
		return nil, err
	}
	if c.MaxRequestBytes == 0 {
		c.MaxRequestBytes = 4 << 20
	}
	if c.MaxRequestBytes < 1 || c.MaxRequestBytes > 32<<20 {
		return nil, errors.New("invalid request bound")
	}
	if c.MaxResponseBytes == 0 {
		c.MaxResponseBytes = 16 << 20
	}
	if c.MaxResponseBytes < 1 || c.MaxResponseBytes > 128<<20 {
		return nil, errors.New("invalid response bound")
	}
	if c.MaxDuration == 0 {
		c.MaxDuration = 2 * time.Minute
	}
	if c.MaxDuration < time.Second || c.MaxDuration > 10*time.Minute {
		return nil, errors.New("invalid duration bound")
	}
	if c.MaxConnections == 0 {
		c.MaxConnections = 8
	}
	if c.MaxConnections < 1 || c.MaxConnections > 64 {
		return nil, errors.New("invalid connection bound")
	}
	tools := map[string]bool{}
	if len(c.AllowedTools) > 64 {
		return nil, errors.New("too many tool grants")
	}
	for _, name := range c.AllowedTools {
		if name == "" || len(name) > 200 {
			return nil, errors.New("invalid tool grant")
		}
		tools[name] = true
	}
	return &Proxy{token: c.Token, routes: routes, allowedTools: tools, ca: bytes.Clone(cert), certificates: certificates, transport: publicTransport(), maxRequestBytes: c.MaxRequestBytes, maxResponseBytes: c.MaxResponseBytes, maxDuration: c.MaxDuration, slots: make(chan struct{}, c.MaxConnections), connections: map[net.Conn]struct{}{}}, nil
}

func (p *Proxy) CertificatePEM() []byte { return bytes.Clone(p.ca) }

// Close terminates hijacked TLS sessions too; http.Server.Close alone does not.
func (p *Proxy) Close() error {
	p.mu.Lock()
	p.closed = true
	for conn := range p.connections {
		_ = conn.Close()
	}
	p.mu.Unlock()
	if transport, ok := p.transport.(*http.Transport); ok {
		transport.CloseIdleConnections()
	}
	return nil
}

// ServeHTTP accepts only authenticated CONNECT/443 for a granted host. It
// terminates TLS and parses requests; it never offers an opaque TCP tunnel.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect || !p.authenticated(r.Header.Get("Proxy-Authorization")) {
		deny(w, http.StatusForbidden)
		return
	}
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil || port != "443" || !validHost(host) || r.URL.Host != r.Host || r.URL.Path != "" || r.URL.RawQuery != "" || r.URL.User != nil {
		deny(w, http.StatusForbidden)
		return
	}
	cert, ok := p.certificates[host]
	if !ok {
		deny(w, http.StatusForbidden)
		return
	}
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	default:
		deny(w, http.StatusTooManyRequests)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		deny(w, http.StatusServiceUnavailable)
		return
	}
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.connections[conn] = struct{}{}
	p.mu.Unlock()
	defer func() { p.mu.Lock(); delete(p.connections, conn); p.mu.Unlock() }()
	ctx, cancel := context.WithTimeout(r.Context(), p.maxDuration)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	_ = conn.SetDeadline(time.Now().Add(p.maxDuration))
	if _, err = rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	if rw.Flush() != nil {
		return
	}
	tlsConn := tls.Server(&bufferedConn{Conn: conn, reader: rw.Reader}, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}, NextProtos: []string{"http/1.1"}, GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
		if hello.ServerName != host {
			return nil, errors.New("unapproved TLS name")
		}
		return nil, nil
	}})
	handshake, cancelHandshake := context.WithTimeout(ctx, 5*time.Second)
	err = tlsConn.HandshakeContext(handshake)
	cancelHandshake()
	if err != nil {
		return
	}
	// One request per tunnel bounds header parsing and prevents request smuggling
	// across persistent proxy sessions. The native client reconnects normally.
	reader := bufio.NewReaderSize(&headerGuardReader{reader: tlsConn}, 32<<10)
	inner, err := http.ReadRequest(reader)
	if err != nil {
		return
	}
	p.forward(ctx, tlsConn, inner, host, reader)
}

func (p *Proxy) authenticated(header string) bool {
	scheme, value, ok := strings.Cut(header, " ")
	if !ok || scheme != "Basic" {
		return false
	}
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return false
	}
	user, password, ok := strings.Cut(string(decoded), ":")
	return ok && user == "group" && subtle.ConstantTimeCompare([]byte(password), []byte(p.token)) == 1
}
func deny(w http.ResponseWriter, status int) { http.Error(w, "egress unavailable", status) }

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(b []byte) (int, error) { return c.reader.Read(b) }

func (p *Proxy) forward(ctx context.Context, conn net.Conn, r *http.Request, host string, reader io.Reader) bool {
	defer r.Body.Close()
	route, allowed := p.route(r, host)
	if !allowed || r.ProtoMajor != 1 || r.Header.Get("Expect") != "" || r.Header.Get("Proxy-Authorization") != "" || len(r.Trailer) != 0 {
		writeFailure(conn, http.StatusForbidden)
		return false
	}
	body, err := p.validateBody(r, route)
	if err != nil {
		writeFailure(conn, http.StatusForbidden)
		return false
	}
	// There is no second request on this tunnel. Any next byte or disconnect
	// cancels upstream work, rather than leaving inference alive until timeout.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { var b [1]byte; _, _ = reader.Read(b[:]); cancel() }()
	outgoing := r.Clone(ctx)
	outgoing.RequestURI = ""
	outgoing.URL.Scheme = "https"
	outgoing.URL.Host = host
	outgoing.Host = host
	outgoing.Body = io.NopCloser(bytes.NewReader(body))
	outgoing.ContentLength = int64(len(body))
	outgoing.TransferEncoding = nil
	outgoing.Trailer = nil
	outgoing.Close = false
	stripHopHeaders(outgoing.Header)
	outgoing.Header.Del("Accept-Encoding")
	response, err := p.transport.RoundTrip(outgoing)
	if err != nil {
		writeFailure(conn, http.StatusBadGateway)
		return false
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 && response.StatusCode < 400 || response.StatusCode == http.StatusSwitchingProtocols || response.ContentLength > p.maxResponseBytes || response.Header.Get("Content-Encoding") != "" {
		writeFailure(conn, http.StatusBadGateway)
		return false
	}
	stripHopHeaders(response.Header)
	response.Close = true
	response.ProtoMajor, response.ProtoMinor = 1, 1
	if response.ContentLength < 0 {
		response.TransferEncoding = []string{"chunked"}
	}
	response.Request = r
	response.Body = &boundedBody{ReadCloser: response.Body, remaining: p.maxResponseBytes}
	if err = response.Write(conn); err != nil {
		return false
	}
	return false
}
func writeFailure(conn net.Conn, status int) {
	body := "egress unavailable\n"
	response := &http.Response{StatusCode: status, ProtoMajor: 1, ProtoMinor: 1, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body)), Close: true}
	_ = response.Write(conn)
}
func stripHopHeaders(header http.Header) {
	for _, key := range strings.Split(header.Get("Connection"), ",") {
		if key = strings.TrimSpace(key); key != "" {
			header.Del(key)
		}
	}
	for _, key := range []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authorization", "Proxy-Authenticate", "TE", "Trailer", "Transfer-Encoding", "Upgrade", "X-Unix-Socket"} {
		header.Del(key)
	}
}

type boundedBody struct {
	io.ReadCloser
	remaining int64
}

func (b *boundedBody) Read(data []byte) (int, error) {
	if b.remaining == 0 {
		var extra [1]byte
		n, err := b.ReadCloser.Read(extra[:])
		if n > 0 {
			return 0, errors.New("response limit")
		}
		return 0, err
	}
	if int64(len(data)) > b.remaining {
		data = data[:b.remaining]
	}
	n, err := b.ReadCloser.Read(data)
	b.remaining -= int64(n)
	return n, err
}

// Bounds bytes before the first header terminator, including fragmented reads.
type headerGuardReader struct {
	reader io.Reader
	count  int
	tail   uint32
	done   bool
}

func (r *headerGuardReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if !r.done {
		for _, b := range p[:n] {
			r.count++
			r.tail = (r.tail << 8) | uint32(b)
			if r.count > 64<<10 {
				return 0, errors.New("header limit")
			}
			if r.tail == 0x0d0a0d0a {
				r.done = true
				break
			}
		}
	}
	return n, err
}
