// Package tlsgen maintains the service's own certificate authority and the
// server certificate it signs.
//
// This exists for one concrete reason, recorded in docs/PORTING_NOTES.md
// § "Known design constraint: browser camera access": getUserMedia is gated to
// secure contexts, so a browser reaching this service at http://192.168.x.x
// cannot open the camera at all. Loopback is a secure context; a LAN IP over
// plain HTTP is not. Without a certificate, the flagship feature of the app —
// photograph your fridge — is unreachable from the phone in your hand.
//
// # Shape
//
// A local CA (ECDSA P-256, ten years) signs a leaf (one year) whose SANs cover
// every address this machine actually answers on: every detected non-loopback
// IPv4 and IPv6 address, the hostname, <hostname>.local, localhost, 127.0.0.1
// and ::1. The CA is served unauthenticated at GET /ca.crt (see CACertHandler)
// so that a user can install it once and stop clicking through warnings.
//
// # Rotation is announced, never silent
//
// A home network hands out addresses by DHCP. When the detected address set
// changes, the leaf no longer covers the address the user types, so it is
// regenerated — and that changes the certificate fingerprint. Any browser that
// stored a per-certificate exception for the old leaf will prompt again.
//
// A server certificate that silently changes under a user is indistinguishable,
// from the browser's side, from someone interposing on the connection. So
// Ensure logs the rotation loudly, names both fingerprints, says which
// addresses were added and removed, and says plainly that browsers will
// re-prompt. A user who is paying attention deserves to be able to check.
package tlsgen

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// File names inside the TLS directory.
const (
	CACertFileName   = "ca.crt"
	CAKeyFileName    = "ca.key"
	LeafCertFileName = "server.crt"
	LeafKeyFileName  = "server.key"
)

// Modes. Everything the service writes here is owner-only, including the
// certificates: the CA is published through /ca.crt by the service itself, not
// by being world-readable on disk.
const (
	FileMode os.FileMode = 0o600
	DirMode  os.FileMode = 0o700
)

// Validity windows and the skew allowance.
const (
	// CAYears is the CA's lifetime. Long, because re-installing a root on every
	// phone in a household is the single most annoying thing this design can
	// ask of a user.
	CAYears = 10
	// LeafYears is the server certificate's lifetime.
	LeafYears = 1
	// RenewBefore triggers a proactive reissue while the old certificate is
	// still valid, so expiry never lands mid-use.
	RenewBefore = 30 * 24 * time.Hour
	// ClockSkew backdates NotBefore. A Pi without an RTC can come up with a
	// wrong clock and reject a certificate it generated seconds earlier.
	ClockSkew = time.Hour
)

// CACertContentType is the media type browsers and mobile OSes recognise as a
// certificate to offer for installation.
const CACertContentType = "application/x-x509-ca-cert"

// SANs is the set of names and addresses a certificate must cover.
type SANs struct {
	DNSNames []string
	IPs      []net.IP
}

// Options configures Ensure. Everything that touches the outside world is a
// function field so a test can supply a fixed interface list and a fixed clock.
type Options struct {
	// Dir is the TLS directory, normally $DD_STATE_DIR/tls.
	Dir string
	// Hostname defaults to os.Hostname().
	Hostname string
	// DetectIPs defaults to DetectLocalIPs.
	DetectIPs func() ([]net.IP, error)
	// Now defaults to time.Now.
	Now func() time.Time
	// Logf defaults to log.Printf. Rotation messages go here, and they are
	// meant to be seen.
	Logf func(format string, args ...any)
}

func (o Options) withDefaults() (Options, error) {
	if o.Dir == "" {
		return o, errors.New("tlsgen: Dir is required")
	}
	if o.DetectIPs == nil {
		o.DetectIPs = DetectLocalIPs
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logf == nil {
		o.Logf = log.Printf
	}
	if o.Hostname == "" {
		h, err := os.Hostname()
		if err != nil || strings.TrimSpace(h) == "" {
			// Not fatal. A machine with no usable hostname still has addresses,
			// and "localhost" is always in the SAN set.
			h = ""
		}
		o.Hostname = h
	}
	return o, nil
}

// Material is the certificate state after Ensure.
type Material struct {
	Dir string

	CACertPEM []byte
	CAKeyPEM  []byte
	CACert    *x509.Certificate

	LeafCertPEM []byte
	LeafKeyPEM  []byte
	LeafCert    *x509.Certificate

	SANs SANs

	// CAGenerated reports that a new certificate authority was created by this
	// call. Every previously installed copy of the old CA is now useless.
	CAGenerated bool
	// LeafGenerated reports that a new server certificate was issued by this
	// call. False on the idempotent path.
	LeafGenerated bool
	// RotatedFrom is the fingerprint of the leaf this call replaced. Empty when
	// there was no previous leaf, or when nothing was regenerated.
	RotatedFrom string
}

// CACertPath is where the authority certificate lives on disk.
func (m *Material) CACertPath() string { return filepath.Join(m.Dir, CACertFileName) }

// LeafCertPath is where the server certificate lives on disk.
func (m *Material) LeafCertPath() string { return filepath.Join(m.Dir, LeafCertFileName) }

// LeafFingerprint is the SHA-256 of the server certificate, formatted the way
// browsers display it, so a user can compare the two by eye.
func (m *Material) LeafFingerprint() string { return Fingerprint(m.LeafCert) }

// CAFingerprint is the SHA-256 of the authority certificate.
func (m *Material) CAFingerprint() string { return Fingerprint(m.CACert) }

// ServerTLSConfig returns a *tls.Config serving the leaf.
func (m *Material) ServerTLSConfig() (*tls.Config, error) {
	cert, err := tls.X509KeyPair(m.LeafCertPEM, m.LeafKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("tlsgen: server keypair: %w", err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}, nil
}

// Fingerprint formats a certificate's SHA-256 as colon-separated uppercase hex.
func Fingerprint(c *x509.Certificate) string {
	if c == nil {
		return ""
	}
	sum := sha256.Sum256(c.Raw)
	h := strings.ToUpper(hex.EncodeToString(sum[:]))
	var b strings.Builder
	for i := 0; i < len(h); i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(h[i : i+2])
	}
	return b.String()
}

// Ensure loads or creates the CA and the server certificate.
//
// It is idempotent: called twice with an unchanged address set it does no work
// and rewrites nothing. It regenerates the leaf when the detected address set
// changes, when the leaf is within RenewBefore of expiry, or when the leaf no
// longer chains to the CA on disk — and it says so, loudly, when it does.
func Ensure(opts Options) (*Material, error) {
	o, err := opts.withDefaults()
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(o.Dir, DirMode); err != nil {
		return nil, fmt.Errorf("tlsgen: create %s: %w", o.Dir, err)
	}
	// MkdirAll honours the umask, and a pre-existing directory keeps whatever
	// mode it had. Private keys live here; assert the mode rather than hope.
	if err := os.Chmod(o.Dir, DirMode); err != nil {
		return nil, fmt.Errorf("tlsgen: chmod %s: %w", o.Dir, err)
	}

	now := o.Now()

	m := &Material{Dir: o.Dir}
	if err := ensureCA(o, now, m); err != nil {
		return nil, err
	}

	ips, err := o.DetectIPs()
	if err != nil {
		return nil, fmt.Errorf("tlsgen: detect local addresses: %w", err)
	}
	want := ComputeSANs(o.Hostname, ips)
	m.SANs = want

	oldLeaf, oldLeafPEM, oldLeafKeyPEM, reason := loadLeaf(o, m.CACert, want, now)
	if reason == "" {
		m.LeafCert, m.LeafCertPEM, m.LeafKeyPEM = oldLeaf, oldLeafPEM, oldLeafKeyPEM
		return m, nil
	}

	previous := ""
	if oldLeaf != nil {
		previous = Fingerprint(oldLeaf)
	}

	leafCert, leafPEM, leafKeyPEM, err := issueLeaf(o, m, want, now)
	if err != nil {
		return nil, err
	}
	m.LeafCert, m.LeafCertPEM, m.LeafKeyPEM = leafCert, leafPEM, leafKeyPEM
	m.LeafGenerated = true
	m.RotatedFrom = previous

	announceRotation(o, m, oldLeaf, want, reason)
	return m, nil
}

// ensureCA loads the authority, or creates one when it is missing, unreadable,
// expired or nearly so.
func ensureCA(o Options, now time.Time, m *Material) error {
	certPath := filepath.Join(o.Dir, CACertFileName)
	keyPath := filepath.Join(o.Dir, CAKeyFileName)

	certPEM, errCert := os.ReadFile(certPath)
	keyPEM, errKey := os.ReadFile(keyPath)
	if errCert == nil && errKey == nil {
		cert, err := parseCertPEM(certPEM)
		if err == nil && cert.IsCA && now.Add(RenewBefore).Before(cert.NotAfter) {
			if _, err := parseKeyPEM(keyPEM); err == nil {
				m.CACert, m.CACertPEM, m.CAKeyPEM = cert, certPEM, keyPEM
				return nil
			}
		}
	}

	hadCA := errCert == nil
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("tlsgen: generate CA key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return err
	}
	name := o.Hostname
	if name == "" {
		name = "this machine"
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{"DinnerDecider"},
			CommonName:   "DinnerDecider local CA (" + name + ")",
		},
		NotBefore:             now.Add(-ClockSkew),
		NotAfter:              now.AddDate(CAYears, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("tlsgen: create CA certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return fmt.Errorf("tlsgen: parse new CA certificate: %w", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM, err = encodeKeyPEM(key)
	if err != nil {
		return err
	}
	if err := writeSecret(certPath, certPEM); err != nil {
		return err
	}
	if err := writeSecret(keyPath, keyPEM); err != nil {
		return err
	}

	m.CACert, m.CACertPEM, m.CAKeyPEM, m.CAGenerated = cert, certPEM, keyPEM, true
	if hadCA {
		o.Logf("TLS: the local certificate authority was REPLACED (the old one was missing a key, corrupt, or expiring). " +
			"Any device that installed the previous DinnerDecider CA must install the new one from /ca.crt.")
	}
	return nil
}

// loadLeaf returns the existing server certificate and an empty reason when it
// is still correct. A non-empty reason is why it has to be reissued, and is
// written into the rotation log.
func loadLeaf(o Options, ca *x509.Certificate, want SANs, now time.Time) (cert *x509.Certificate, certPEM, keyPEM []byte, reason string) {
	certPath := filepath.Join(o.Dir, LeafCertFileName)
	keyPath := filepath.Join(o.Dir, LeafKeyFileName)

	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, nil, nil, "no server certificate on disk"
	}
	keyPEM, err = os.ReadFile(keyPath)
	if err != nil {
		return nil, nil, nil, "the server private key is missing"
	}
	cert, err = parseCertPEM(certPEM)
	if err != nil {
		return nil, nil, nil, "the server certificate could not be parsed"
	}
	if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
		return cert, nil, nil, "the server certificate and key do not match"
	}
	if err := cert.CheckSignatureFrom(ca); err != nil {
		return cert, nil, nil, "the server certificate was not signed by the certificate authority on disk"
	}
	if !now.Add(RenewBefore).Before(cert.NotAfter) {
		return cert, nil, nil, "the server certificate has expired or is about to"
	}
	if !sansEqual(sansOf(cert), want) {
		return cert, nil, nil, "the set of local addresses changed"
	}
	return cert, certPEM, keyPEM, ""
}

func issueLeaf(o Options, m *Material, want SANs, now time.Time) (*x509.Certificate, []byte, []byte, error) {
	caKey, err := parseKeyPEM(m.CAKeyPEM)
	if err != nil {
		return nil, nil, nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("tlsgen: generate server key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, nil, err
	}
	cn := o.Hostname
	if cn == "" {
		cn = "localhost"
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{"DinnerDecider"},
			CommonName:   cn,
		},
		NotBefore:             now.Add(-ClockSkew),
		NotAfter:              now.AddDate(LeafYears, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              append([]string(nil), want.DNSNames...),
		IPAddresses:           append([]net.IP(nil), want.IPs...),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, m.CACert, &key.PublicKey, caKey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("tlsgen: create server certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("tlsgen: parse new server certificate: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM, err := encodeKeyPEM(key)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := writeSecret(filepath.Join(o.Dir, LeafCertFileName), certPEM); err != nil {
		return nil, nil, nil, err
	}
	if err := writeSecret(filepath.Join(o.Dir, LeafKeyFileName), keyPEM); err != nil {
		return nil, nil, nil, err
	}
	return cert, certPEM, keyPEM, nil
}

// announceRotation is the loud part. A server certificate that changes under a
// user looks exactly like an interposed connection, so say what happened, name
// both fingerprints so they can be checked, and do not bury the consequence.
func announceRotation(o Options, m *Material, oldLeaf *x509.Certificate, want SANs, reason string) {
	if oldLeaf == nil {
		o.Logf("TLS: issued a server certificate for %s", strings.Join(describeSANs(want), ", "))
		o.Logf("TLS: reason: %s", reason)
		o.Logf("TLS: fingerprint %s", m.LeafFingerprint())
		o.Logf("TLS: browsers will warn until the certificate authority is installed. Fetch it from http(s)://<this host>/ca.crt")
		return
	}

	added, removed := diffSANs(sansOf(oldLeaf), want)
	o.Logf("TLS: ***** THE SERVER CERTIFICATE HAS BEEN REGENERATED *****")
	o.Logf("TLS: reason: %s", reason)
	if len(added) > 0 {
		o.Logf("TLS: addresses added:   %s", strings.Join(added, ", "))
	}
	if len(removed) > 0 {
		o.Logf("TLS: addresses removed: %s", strings.Join(removed, ", "))
	}
	o.Logf("TLS: old fingerprint: %s", Fingerprint(oldLeaf))
	o.Logf("TLS: new fingerprint: %s", m.LeafFingerprint())
	o.Logf("TLS: EVERY BROWSER THAT TRUSTED THE OLD CERTIFICATE WILL PROMPT AGAIN. That is expected here, " +
		"but a certificate changing without explanation is also what an interposed connection looks like — " +
		"so check the new fingerprint above against what your browser shows.")
	o.Logf("TLS: installing the certificate authority from /ca.crt makes future address changes silent and safe.")
}

// ComputeSANs builds the name set a server certificate must cover: localhost
// and both loopback literals so a local browser always works, the machine's own
// hostname and its .local form for mDNS, and every detected non-loopback
// address so a phone can reach the service by IP.
//
// The result is sorted, because it is compared against the previous
// certificate's SANs to decide whether the address set changed; an unsorted set
// would report a change every time an interface enumerated in a different
// order, and rotate the certificate for no reason.
func ComputeSANs(hostname string, ips []net.IP) SANs {
	dns := map[string]struct{}{"localhost": {}}
	h := strings.TrimSpace(strings.ToLower(hostname))
	h = strings.TrimSuffix(h, ".")
	if h != "" && h != "localhost" {
		dns[h] = struct{}{}
		short := h
		if i := strings.IndexByte(short, '.'); i > 0 {
			short = short[:i]
		}
		if short != "" && short != "localhost" {
			dns[short+".local"] = struct{}{}
		}
	}

	names := make([]string, 0, len(dns))
	for n := range dns {
		names = append(names, n)
	}
	sort.Strings(names)

	seen := map[string]struct{}{}
	out := make([]net.IP, 0, len(ips)+2)
	add := func(ip net.IP) {
		if ip == nil || ip.IsUnspecified() {
			return
		}
		k := ip.String()
		if _, dup := seen[k]; dup {
			return
		}
		seen[k] = struct{}{}
		out = append(out, ip)
	}
	add(net.ParseIP("127.0.0.1"))
	add(net.ParseIP("::1"))
	for _, ip := range ips {
		if ip.IsLoopback() {
			// Already covered, and a second loopback literal adds nothing.
			add(ip)
			continue
		}
		add(ip)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })

	return SANs{DNSNames: names, IPs: out}
}

// DetectLocalIPs enumerates every address on every interface that is up and is
// not the loopback interface.
//
// Link-local IPv6 addresses are included deliberately. They are stable across
// reboots, and a phone that reaches the service over one still needs the
// certificate to cover it.
func DetectLocalIPs() ([]net.IP, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []net.IP
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 {
			continue
		}
		if ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() || ip.IsUnspecified() {
				continue
			}
			out = append(out, ip)
		}
	}
	return out, nil
}

// CACertHandler serves the certificate authority.
//
// It is mounted unauthenticated, and that is not an oversight. The CA is a
// public certificate — it carries no private key and grants nothing. Requiring
// a credential to fetch the thing a client needs in order to trust the
// connection is a bootstrap deadlock: the user cannot pair without trusting the
// connection, and cannot trust the connection without pairing.
func CACertHandler(caCertPEM []byte) http.Handler {
	body := append([]byte(nil), caCertPEM...)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", CACertContentType)
		w.Header().Set("Content-Disposition", `attachment; filename="dinnerdecider-ca.crt"`)
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write(body)
	})
}

// ---- helpers ---------------------------------------------------------------

func sansOf(c *x509.Certificate) SANs {
	if c == nil {
		return SANs{}
	}
	return SANs{DNSNames: c.DNSNames, IPs: c.IPAddresses}
}

func describeSANs(s SANs) []string {
	out := append([]string(nil), s.DNSNames...)
	for _, ip := range s.IPs {
		out = append(out, ip.String())
	}
	sort.Strings(out)
	return out
}

func sansEqual(a, b SANs) bool {
	x, y := describeSANs(a), describeSANs(b)
	if len(x) != len(y) {
		return false
	}
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

func diffSANs(old, want SANs) (added, removed []string) {
	o := map[string]struct{}{}
	for _, v := range describeSANs(old) {
		o[v] = struct{}{}
	}
	w := map[string]struct{}{}
	for _, v := range describeSANs(want) {
		w[v] = struct{}{}
	}
	for v := range w {
		if _, ok := o[v]; !ok {
			added = append(added, v)
		}
	}
	for v := range o {
		if _, ok := w[v]; !ok {
			removed = append(removed, v)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}

func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	n, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("tlsgen: serial number: %w", err)
	}
	return n, nil
}

func encodeKeyPEM(key *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("tlsgen: marshal private key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

func parseCertPEM(data []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("tlsgen: not a PEM certificate")
	}
	return x509.ParseCertificate(block.Bytes)
}

func parseKeyPEM(data []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("tlsgen: not a PEM private key")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("tlsgen: parse private key: %w", err)
	}
	ec, ok := key.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("tlsgen: expected an ECDSA key, got %T", key)
	}
	return ec, nil
}

// writeSecret writes owner-only, and asserts the mode afterwards so that a file
// left behind at a looser mode by an earlier version, a restore, or a careless
// hand is corrected rather than inherited.
func writeSecret(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, FileMode)
	if err != nil {
		return fmt.Errorf("tlsgen: write %s: %w", path, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("tlsgen: write %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("tlsgen: write %s: %w", path, err)
	}
	if err := os.Chmod(path, FileMode); err != nil {
		return fmt.Errorf("tlsgen: chmod %s: %w", path, err)
	}
	return nil
}
