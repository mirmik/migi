// Command migi-probe verifies that a Migi endpoint is reachable over HTTP/3.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

func main() {
	endpoint := flag.String("endpoint", "https://127.0.0.1:8443", "Migi HTTPS endpoint")
	caPath := flag.String("ca", "", "PEM CA certificate")
	serverName := flag.String("server-name", "", "override the TLS server name for diagnostics")
	localPort := flag.Int("local-port", 0, "bind the diagnostic UDP client to this local port")
	flag.Parse()
	roots, err := x509.SystemCertPool()
	if err != nil {
		fatal(err)
	}
	if *caPath != "" {
		caPEM, err := os.ReadFile(*caPath)
		if err != nil {
			fatal(err)
		}
		if !roots.AppendCertsFromPEM(caPEM) {
			fatal(fmt.Errorf("no certificates found in %s", *caPath))
		}
	}
	transport := &http3.Transport{TLSClientConfig: &tls.Config{
		RootCAs:    roots,
		MinVersion: tls.VersionTLS13,
		ServerName: *serverName,
	}}
	if *localPort != 0 {
		udpConn, err := net.ListenUDP("udp", &net.UDPAddr{Port: *localPort})
		if err != nil {
			fatal(err)
		}
		quicTransport := &quic.Transport{Conn: udpConn}
		defer quicTransport.Close()
		transport.Dial = func(ctx context.Context, addr string, tlsConfig *tls.Config, config *quic.Config) (*quic.Conn, error) {
			remote, err := net.ResolveUDPAddr("udp", addr)
			if err != nil {
				return nil, err
			}
			return quicTransport.DialEarly(ctx, remote, tlsConfig, config)
		}
	}
	defer transport.Close()
	client := http.Client{Transport: transport, Timeout: 5 * time.Second}
	response, err := client.Get(*endpoint + "/healthz")
	if err != nil {
		fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4096))
	if err != nil {
		fatal(err)
	}
	if response.StatusCode != http.StatusOK || response.ProtoMajor != 3 {
		fatal(fmt.Errorf("unexpected response: %s over %s: %s", response.Status, response.Proto, body))
	}
	fmt.Printf("ok: %s over %s\n", body, response.Proto)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
