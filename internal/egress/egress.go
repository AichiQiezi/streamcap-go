// Package egress sends recording traffic out of a chosen network interface, past a VPN that has
// taken over the default route (OpenVPN's 0.0.0.0/1 + 128.0.0.0/1). Live streams pulled through
// such a tunnel can arrive slower than real time, which loses most of a high-bitrate recording.
//
// It runs a forward proxy on 127.0.0.1 whose outgoing connections are pinned to the interface
// (IP_UNICAST_IF on Windows, IP_BOUND_IF on macOS). ffmpeg and the platform HTTP clients already
// take a proxy URL, so nothing else has to know about interfaces.
package egress

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Auto picks the interface that holds the machine's real default gateway (see autoInterface).
const Auto = "auto"

// Proxy is the loopback forward proxy. The zero value is not usable; use New.
type Proxy struct {
	mu     sync.Mutex
	choice string // "", Auto or an interface name
	ln     net.Listener
	tr     *http.Transport
	warned string // last interface problem logged, to log it once
}

// New returns a proxy that is not listening yet; it starts on the first SetInterface that needs it.
func New() *Proxy {
	p := &Proxy{}
	p.tr = &http.Transport{
		Proxy:               nil, // never chain into the system proxy
		DialContext:         p.dial,
		DisableCompression:  true, // pass bytes through as the server sent them
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 15 * time.Second,
	}
	return p
}

// SetInterface chooses where connections go out: "" (system routing, proxy unused), Auto, or an
// interface name.
func (p *Proxy) SetInterface(choice string) {
	choice = strings.TrimSpace(choice)
	p.mu.Lock()
	defer p.mu.Unlock()
	if choice == p.choice {
		return
	}
	p.choice, p.warned = choice, ""
	if choice != "" && p.ln == nil {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			slog.Error("egress: cannot start the local proxy, recordings use system routing", "err", err)
			p.choice = ""
			return
		}
		p.ln = ln
		srv := &http.Server{Handler: http.HandlerFunc(p.serve), ReadHeaderTimeout: 15 * time.Second}
		go func() { _ = srv.Serve(ln) }()
	}
	if choice != "" {
		slog.Info("egress: recording traffic pinned", "interface", choice, "proxy", p.ln.Addr().String())
	}
}

// URL is the proxy to hand to ffmpeg / HTTP clients, or "" when traffic should use system routing.
func (p *Proxy) URL() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.choice == "" || p.ln == nil {
		return ""
	}
	return "http://" + p.ln.Addr().String()
}

// target resolves the current choice to an interface, or nil to use system routing.
func (p *Proxy) target() *net.Interface {
	p.mu.Lock()
	choice := p.choice
	p.mu.Unlock()
	var (
		ifc *net.Interface
		err error
	)
	if choice == Auto {
		ifc, err = autoInterface()
	} else if choice != "" {
		ifc, err = net.InterfaceByName(choice)
	}
	if err == nil && ifc != nil && ifc.Flags&net.FlagUp == 0 {
		err = errors.New("interface is down")
	}
	if err != nil || ifc == nil {
		p.mu.Lock()
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		if p.warned != choice+msg {
			p.warned = choice + msg
			slog.Warn("egress: interface unavailable, using system routing", "interface", choice, "err", err)
		}
		p.mu.Unlock()
		return nil
	}
	return ifc
}

func ipv4Of(ifc *net.Interface) net.IP {
	addrs, _ := ifc.Addrs()
	for _, a := range addrs {
		if ip, ok := a.(*net.IPNet); ok && ip.IP.To4() != nil && !ip.IP.IsLinkLocalUnicast() {
			return ip.IP.To4()
		}
	}
	return nil
}

// dial connects out of the chosen interface (IPv4 only: the pin is an IPv4 socket option).
func (p *Proxy) dial(ctx context.Context, _, addr string) (net.Conn, error) {
	d := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	if ifc := p.target(); ifc != nil {
		if ip := ipv4Of(ifc); ip != nil {
			d.LocalAddr = &net.TCPAddr{IP: ip}
			d.Control = pin(ifc.Index)
			return d.DialContext(ctx, "tcp4", addr)
		}
	}
	return d.DialContext(ctx, "tcp", addr)
}

var hopHeaders = []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
	"Te", "Trailer", "Transfer-Encoding", "Upgrade"}

func (p *Proxy) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.tunnel(w, r)
		return
	}
	if !r.URL.IsAbs() {
		http.Error(w, "this is StreamCap's recording proxy", http.StatusBadRequest)
		return
	}
	out := r.Clone(r.Context())
	out.RequestURI = ""
	for _, h := range hopHeaders {
		out.Header.Del(h)
	}
	resp, err := p.tr.RoundTrip(out)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for _, h := range hopHeaders {
		resp.Header.Del(h)
	}
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	// A live stream never ends: flush as data arrives instead of buffering.
	fl, _ := w.(http.Flusher)
	buf := make([]byte, 64<<10)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if fl != nil {
				fl.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

// tunnel serves CONNECT (https and wss through the proxy).
func (p *Proxy) tunnel(w http.ResponseWriter, r *http.Request) {
	up, err := p.dial(r.Context(), "tcp", r.Host)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		up.Close()
		http.Error(w, "hijack unsupported", http.StatusInternalServerError)
		return
	}
	down, rw, err := hj.Hijack()
	if err != nil {
		up.Close()
		return
	}
	_, _ = down.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
	// Bytes the client sent right after its CONNECT may already sit in the reader's buffer.
	if n := rw.Reader.Buffered(); n > 0 {
		b, _ := rw.Reader.Peek(n)
		_, _ = up.Write(b)
	}
	go func() {
		_, _ = io.Copy(up, down)
		closeWrite(up)
	}()
	_, _ = io.Copy(down, up)
	down.Close()
	up.Close()
}

func closeWrite(c net.Conn) {
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.CloseWrite()
		return
	}
	c.Close()
}

// Choice is one option for the settings page.
type Choice struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// Choices lists the interfaces that can carry recordings: up, not loopback, with an IPv4 address.
func Choices() []Choice {
	out := []Choice{}
	ifs, _ := net.Interfaces()
	for i := range ifs {
		ifc := &ifs[i]
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		if ip := ipv4Of(ifc); ip != nil {
			out = append(out, Choice{Value: ifc.Name, Label: ifc.Name + "（" + ip.String() + "）"})
		}
	}
	return out
}

// AutoName is the interface Auto currently resolves to, or "".
func AutoName() string {
	if ifc, err := autoInterface(); err == nil && ifc != nil {
		return ifc.Name
	}
	return ""
}
