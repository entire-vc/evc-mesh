// Command keepstub stands in for Keep in CI jobs that run the whole API
// (tenancy-rbac-gate): workspace secrets have no home but Keep since an internal ticket,
// so creating one needs a Keep to answer. It serves the two routes of Keep's
// Mesh secrets contract (evc-keep docs/mesh-extension.md, the same two
// HTTPKeepClient calls) over TLS with a throwaway self-signed certificate,
// checks the request signature with the events secret, and keeps values in
// memory. It is a test fixture, never deployed.
//
//	keepstub -addr 127.0.0.1:8443 -cert-out /tmp/keepstub.pem -secret "$MESH_KEEP_EVENTS_SECRET"
//
// The API trusts the certificate through SSL_CERT_FILE=/tmp/keepstub.pem.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"flag"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

type store struct {
	secret []byte

	mu      sync.Mutex
	values  map[string]string // org/ref -> value
	version map[string]int
	nonces  map[string]bool
}

// authorized checks the v1 signature over the request exactly as Keep does.
func (s *store) authorized(r *http.Request, body []byte) bool {
	ts, nonce := r.Header.Get("X-Mesh-Timestamp"), r.Header.Get("X-Mesh-Nonce")
	if ts == "" || nonce == "" || r.Header.Get("X-Mesh-Workspace") == "" {
		return false
	}
	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || time.Since(time.Unix(sec, 0)).Abs() > 5*time.Minute {
		return false
	}
	sum := sha256.Sum256(body)
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte("keep-mesh-secret-v1\n" + ts + "\n" + nonce + "\n" + r.Method + "\n" + r.URL.EscapedPath() + "\n" + hex.EncodeToString(sum[:])))
	want := "v1=" + hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(r.Header.Get("X-Mesh-Signature"))) {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nonces[nonce] {
		return false
	}
	s.nonces[nonce] = true
	return true
}

func (s *store) put(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if !s.authorized(r, body) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var in struct {
		Value string `json:"value"`
	}
	if json.Unmarshal(body, &in) != nil || in.Value == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	key := r.PathValue("org_id") + "/" + r.PathValue("ref")
	s.mu.Lock()
	s.values[key] = in.Value
	s.version[key]++
	v := s.version[key]
	s.mu.Unlock()
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"ref": r.PathValue("ref"), "version": strconv.Itoa(v)})
}

func (s *store) resolve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if !s.authorized(r, body) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	key := r.PathValue("org_id") + "/" + r.PathValue("ref")
	s.mu.Lock()
	v, ok := s.values[key]
	n := s.version[key]
	s.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"value": v, "version": strconv.Itoa(n)})
}

// selfSigned makes a certificate for localhost and 127.0.0.1 and writes it,
// PEM, to certOut for the client to trust.
func selfSigned(certOut string) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "keepstub"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if werr := os.WriteFile(certOut, certPEM, 0o644); werr != nil {
		return tls.Certificate{}, werr
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.X509KeyPair(certPEM, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8443", "listen address")
	certOut := flag.String("cert-out", "/tmp/keepstub.pem", "where to write the certificate the client trusts")
	secret := flag.String("secret", "", "events secret the requests are signed with")
	flag.Parse()
	if *secret == "" {
		log.Fatal("keepstub: -secret is required")
	}
	cert, err := selfSigned(*certOut)
	if err != nil {
		log.Fatalf("keepstub: certificate: %v", err)
	}
	s := &store{secret: []byte(*secret), values: map[string]string{}, version: map[string]int{}, nonces: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /orgs/{org_id}/mesh/secrets/{ref}", s.put)
	mux.HandleFunc("POST /orgs/{org_id}/mesh/secrets/{ref}/resolve", s.resolve)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	srv := &http.Server{Addr: *addr, Handler: mux, TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}, ReadHeaderTimeout: 5 * time.Second}
	log.Printf("keepstub: listening on https://%s", *addr)
	log.Fatal(srv.ListenAndServeTLS("", ""))
}
