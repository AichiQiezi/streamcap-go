package egress

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestProxyForwardsHTTPAndConnect(t *testing.T) {
	body := strings.Repeat("flv-bytes-", 10000)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/x-flv")
		_, _ = io.WriteString(w, body)
	})
	plain := httptest.NewServer(h)
	defer plain.Close()
	tls := httptest.NewTLSServer(h)
	defer tls.Close()

	p := New()
	if p.URL() != "" {
		t.Fatal("proxy URL set before an interface was chosen")
	}
	// A missing interface falls back to system routing instead of failing the recording.
	p.SetInterface("no-such-interface")

	pu, err := url.Parse(p.URL())
	if err != nil || p.URL() == "" {
		t.Fatalf("proxy url %q: %v", p.URL(), err)
	}
	resp, err := (&http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}).Get(plain.URL + "/stream.flv")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(got) != body || resp.Header.Get("Content-Type") != "video/x-flv" {
		t.Errorf("plain: %d bytes, content-type %q", len(got), resp.Header.Get("Content-Type"))
	}

	c := tls.Client() // trusts the test certificate
	c.Transport.(*http.Transport).Proxy = http.ProxyURL(pu)
	resp, err = c.Get(tls.URL + "/stream.flv")
	if err != nil {
		t.Fatal(err)
	}
	got, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(got) != body {
		t.Errorf("CONNECT: got %d bytes", len(got))
	}

	p.SetInterface("")
	if p.URL() != "" {
		t.Error("proxy still advertised after switching back to system routing")
	}
}
