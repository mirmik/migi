// Command migi-pair creates a short-lived QR invitation for one Migi device.
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/mirmik/migi/server/internal/events"
	qrcode "github.com/skip2/go-qrcode"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	databasePath := flag.String("db", "migi.db", "SQLite event journal path")
	endpoint := flag.String("endpoint", "", "public Migi HTTPS endpoint")
	certificatePath := flag.String("cert", "", "server leaf certificate in PEM format")
	ttl := flag.Duration("ttl", 10*time.Minute, "pairing invitation lifetime")
	output := flag.String("output", "migi-pair.png", "QR image output path")
	terminal := flag.Bool("terminal", true, "also print the QR in the terminal")
	printURI := flag.Bool("print-uri", false, "print the secret deep link (for testing/manual transfer)")
	flag.Parse()

	if *endpoint == "" || *certificatePath == "" {
		return errors.New("-endpoint and -cert are required")
	}
	if *ttl < time.Minute || *ttl > time.Hour {
		return errors.New("-ttl must be between 1 minute and 1 hour")
	}
	parsedEndpoint, err := url.Parse(*endpoint)
	if err != nil || parsedEndpoint.Scheme != "https" || parsedEndpoint.Host == "" ||
		parsedEndpoint.User != nil || parsedEndpoint.RawQuery != "" || parsedEndpoint.Fragment != "" {
		return errors.New("-endpoint must be a plain https://host[:port] URL")
	}
	parsedEndpoint.Path = strings.TrimRight(parsedEndpoint.Path, "/")

	certificateDER, err := readLeafCertificate(*certificatePath)
	if err != nil {
		return err
	}
	pin := sha256.Sum256(certificateDER)
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return fmt.Errorf("generate pairing secret: %w", err)
	}
	secretHash := sha256.Sum256(secret)
	expiresAt := time.Now().UTC().Add(*ttl)

	journal, err := events.OpenSQLite(*databasePath)
	if err != nil {
		return err
	}
	defer journal.Close()
	if err := journal.CreatePairingCode(context.Background(), secretHash[:], expiresAt); err != nil {
		return err
	}

	invitation := &url.URL{Scheme: "migi", Host: "pair"}
	query := invitation.Query()
	query.Set("endpoint", parsedEndpoint.String())
	query.Set("pin", strings.ToUpper(hex.EncodeToString(pin[:])))
	query.Set("secret", base64.RawURLEncoding.EncodeToString(secret))
	query.Set("expires", expiresAt.Format(time.RFC3339))
	invitation.RawQuery = query.Encode()

	code, err := qrcode.New(invitation.String(), qrcode.Medium)
	if err != nil {
		return fmt.Errorf("create QR: %w", err)
	}
	if err := code.WriteFile(512, *output); err != nil {
		return fmt.Errorf("write QR: %w", err)
	}
	if err := os.Chmod(*output, 0o600); err != nil {
		return fmt.Errorf("protect QR file: %w", err)
	}

	if *terminal {
		fmt.Print(code.ToSmallString(false))
	}
	if *printURI {
		fmt.Printf("Pairing URI: %s\n", invitation.String())
	}
	fmt.Printf("\nQR image: %s\n", *output)
	fmt.Printf("Endpoint: %s\n", parsedEndpoint.String())
	fmt.Printf("Certificate SHA-256: %X\n", pin)
	fmt.Printf("Expires: %s\n", expiresAt.Format(time.RFC3339))
	fmt.Println("The QR contains a one-time secret. Delete the image after pairing or expiry.")
	return nil
}

func readLeafCertificate(path string) ([]byte, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read certificate: %w", err)
	}
	block, _ := pem.Decode(contents)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("certificate file does not start with a PEM certificate")
	}
	if _, err := x509.ParseCertificate(block.Bytes); err != nil {
		return nil, fmt.Errorf("parse certificate: %w", err)
	}
	return block.Bytes, nil
}
