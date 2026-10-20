package sniff

import (
	"crypto/tls"
	"net"
	"testing"
	"time"
)

func TestTLSSNI(t *testing.T) {
	client, server := net.Pipe()
	go func() {
		c := tls.Client(client, &tls.Config{ServerName: "Registry.NPMJS.org", InsecureSkipVerify: true})
		_ = c.Handshake()
	}()
	res, err := Peek(server, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != KindTLS || res.Host != "registry.npmjs.org" || len(res.Buffered) == 0 {
		t.Fatalf("got %+v", res)
	}
}

func TestHTTPHost(t *testing.T) {
	client, server := net.Pipe()
	go func() {
		_, _ = client.Write([]byte("GET /simple/ HTTP/1.1\r\nHost: pypi.org:80\r\nUser-Agent: x\r\n\r\n"))
	}()
	res, err := Peek(server, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != KindHTTP || res.Host != "pypi.org" {
		t.Fatalf("got %+v", res)
	}
}

func TestUnknown(t *testing.T) {
	client, server := net.Pipe()
	go func() { _, _ = client.Write([]byte("SSH-2.0-OpenSSH_9.6\r\n")) }()
	res, _ := Peek(server, time.Second)
	if res.Kind != KindUnknown || res.Host != "" {
		t.Fatalf("got %+v", res)
	}
}
