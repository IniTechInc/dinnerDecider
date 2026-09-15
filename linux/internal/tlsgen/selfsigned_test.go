package tlsgen

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var baseTime = time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)

// recorder collects the loud rotation messages so a test can assert on them.
type recorder struct {
	mu    sync.Mutex
	lines []string
}

func (r *recorder) logf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, strings.TrimSpace(fmt.Sprintf(format, args...)))
}

func (r *recorder) text() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.lines, "\n")
}

func (r *recorder) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = nil
}

// fixture builds Options over a temp dir with a fixed clock, a fixed hostname
// and a fake interface list. Every external input this package has is a
// function field precisely so a test can pin all of them.
type fixture struct {
	dir  string
	log  *recorder
	now  time.Time
	ips  []net.IP
	host string
}

func newFixture(t *testing.T, host string, ips ...string) *fixture {
	t.Helper()
	f := &fixture{
		dir:  t.TempDir(),
		log:  &recorder{},
		now:  baseTime,
		host: host,
	}
	f.setIPs(ips...)
	return f
}

func (f *fixture) setIPs(ips ...string) {
	f.ips = nil
	for _, s := range ips {
		ip := net.ParseIP(s)
		if ip == nil {
			panic("bad test IP " + s)
		}
		f.ips = append(f.ips, ip)
	}
}

func (f *fixture) options() Options {
	return Options{
		Dir:       f.dir,
		Hostname:  f.host,
		DetectIPs: func() ([]net.IP, error) { return append([]net.IP(nil), f.ips...), nil },
		Now:       func() time.Time { return f.now },
		Logf:      f.log.logf,
	}
}

func (f *fixture) ensure(t *testing.T) *Material {
	t.Helper()
	m, err := Ensure(f.options())
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	return m
}

func perm(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return fi.Mode().Perm()
}

func TestEnsureGeneratesACAAndALeaf(t *testing.T) {
	f := newFixture(t, "dinnerpi", "192.168.1.42")
	m := f.ensure(t)

	if !m.CAGenerated {
		t.Error("first Ensure must create the certificate authority")
	}
	if !m.LeafGenerated {
		t.Error("first Ensure must issue a server certificate")
	}
	if m.RotatedFrom != "" {
		t.Errorf("RotatedFrom = %q, want empty on a first issue", m.RotatedFrom)
	}

	for _, name := range []string{CACertFileName, CAKeyFileName, LeafCertFileName, LeafKeyFileName} {
		if _, err := os.Stat(filepath.Join(f.dir, name)); err != nil {
			t.Errorf("%s was not written: %v", name, err)
		}
	}

	if !m.CACert.IsCA {
		t.Error("the CA certificate must have IsCA set")
	}
	if !m.CACert.BasicConstraintsValid || m.CACert.MaxPathLen != 0 || !m.CACert.MaxPathLenZero {
		t.Error("the CA must be constrained to a path length of zero")
	}
	if m.LeafCert.IsCA {
		t.Error("the server certificate must not be a CA")
	}
	if m.CACert.PublicKeyAlgorithm != x509.ECDSA || m.LeafCert.PublicKeyAlgorithm != x509.ECDSA {
		t.Error("both keys must be ECDSA P-256")
	}

	if err := m.LeafCert.CheckSignatureFrom(m.CACert); err != nil {
		t.Errorf("the leaf must be signed by the CA: %v", err)
	}

	wantCANotAfter := baseTime.AddDate(CAYears, 0, 0)
	if !m.CACert.NotAfter.Equal(wantCANotAfter) {
		t.Errorf("CA NotAfter = %s, want %s (ten years)", m.CACert.NotAfter, wantCANotAfter)
	}
	wantLeafNotAfter := baseTime.AddDate(LeafYears, 0, 0)
	if !m.LeafCert.NotAfter.Equal(wantLeafNotAfter) {
		t.Errorf("leaf NotAfter = %s, want %s (one year)", m.LeafCert.NotAfter, wantLeafNotAfter)
	}
	// A Pi with no RTC can boot with a wrong clock and reject a certificate it
	// generated moments earlier.
	if !m.LeafCert.NotBefore.Equal(baseTime.Add(-ClockSkew)) {
		t.Errorf("leaf NotBefore = %s, want a %s skew allowance", m.LeafCert.NotBefore, ClockSkew)
	}

	if len(m.LeafCert.ExtKeyUsage) != 1 || m.LeafCert.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth {
		t.Errorf("leaf ExtKeyUsage = %v, want ServerAuth only", m.LeafCert.ExtKeyUsage)
	}
}

func TestSANsCoverEveryDetectedAddress(t *testing.T) {
	tests := []struct {
		name     string
		hostname string
		ips      []string
		wantDNS  []string
		wantIPs  []string
	}{
		{
			name:     "one IPv4",
			hostname: "dinnerpi",
			ips:      []string{"192.168.1.42"},
			wantDNS:  []string{"dinnerpi", "dinnerpi.local", "localhost"},
			wantIPs:  []string{"127.0.0.1", "192.168.1.42", "::1"},
		},
		{
			name:     "IPv4 and IPv6 on several interfaces",
			hostname: "kitchen",
			ips:      []string{"192.168.1.42", "10.8.0.3", "2001:db8::5", "fe80::1"},
			wantDNS:  []string{"kitchen", "kitchen.local", "localhost"},
			wantIPs:  []string{"10.8.0.3", "127.0.0.1", "192.168.1.42", "2001:db8::5", "::1", "fe80::1"},
		},
		{
			name:     "no addresses at all still covers loopback",
			hostname: "offline",
			ips:      nil,
			wantDNS:  []string{"localhost", "offline", "offline.local"},
			wantIPs:  []string{"127.0.0.1", "::1"},
		},
		{
			name:     "an FQDN hostname keeps both forms",
			hostname: "pi.lan",
			ips:      []string{"192.168.1.42"},
			wantDNS:  []string{"localhost", "pi.lan", "pi.local"},
			wantIPs:  []string{"127.0.0.1", "192.168.1.42", "::1"},
		},
		{
			name:     "a hostname that is already .local is not doubled",
			hostname: "pi.local",
			ips:      nil,
			wantDNS:  []string{"localhost", "pi.local"},
			wantIPs:  []string{"127.0.0.1", "::1"},
		},
		{
			name:     "a hostname of localhost is not duplicated",
			hostname: "localhost",
			ips:      nil,
			wantDNS:  []string{"localhost"},
			wantIPs:  []string{"127.0.0.1", "::1"},
		},
		{
			name:     "a detected loopback address is not duplicated",
			hostname: "dup",
			ips:      []string{"127.0.0.1", "::1", "192.168.1.42"},
			wantDNS:  []string{"dup", "dup.local", "localhost"},
			wantIPs:  []string{"127.0.0.1", "192.168.1.42", "::1"},
		},
		{
			name:     "a repeated address is not duplicated",
			hostname: "dup2",
			ips:      []string{"192.168.1.42", "192.168.1.42"},
			wantDNS:  []string{"dup2", "dup2.local", "localhost"},
			wantIPs:  []string{"127.0.0.1", "192.168.1.42", "::1"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, tc.hostname, tc.ips...)
			m := f.ensure(t)

			if got := strings.Join(m.LeafCert.DNSNames, ","); got != strings.Join(tc.wantDNS, ",") {
				t.Errorf("DNS SANs = %v, want %v", m.LeafCert.DNSNames, tc.wantDNS)
			}
			var gotIPs []string
			for _, ip := range m.LeafCert.IPAddresses {
				gotIPs = append(gotIPs, ip.String())
			}
			if strings.Join(gotIPs, ",") != strings.Join(tc.wantIPs, ",") {
				t.Errorf("IP SANs = %v, want %v", gotIPs, tc.wantIPs)
			}
		})
	}
}

func TestComputeSANsWithoutAHostname(t *testing.T) {
	// A machine with no usable hostname still has addresses, and localhost is
	// always present, so the certificate is still useful.
	got := ComputeSANs("", []net.IP{net.ParseIP("192.168.1.42")})
	if strings.Join(got.DNSNames, ",") != "localhost" {
		t.Errorf("DNS SANs = %v, want [localhost]", got.DNSNames)
	}
	var ips []string
	for _, ip := range got.IPs {
		ips = append(ips, ip.String())
	}
	if strings.Join(ips, ",") != "127.0.0.1,192.168.1.42,::1" {
		t.Errorf("IP SANs = %v", ips)
	}
}

func TestIdempotentWhenTheAddressSetIsUnchanged(t *testing.T) {
	f := newFixture(t, "dinnerpi", "192.168.1.42")
	first := f.ensure(t)
	f.log.reset()

	second := f.ensure(t)

	if second.CAGenerated {
		t.Error("the CA must not be regenerated on an unchanged second run")
	}
	if second.LeafGenerated {
		t.Error("the leaf must not be regenerated on an unchanged second run")
	}
	if string(first.LeafCertPEM) != string(second.LeafCertPEM) {
		t.Error("the server certificate bytes changed on an unchanged second run")
	}
	if string(first.CACertPEM) != string(second.CACertPEM) {
		t.Error("the CA certificate bytes changed on an unchanged second run")
	}
	if first.LeafFingerprint() != second.LeafFingerprint() {
		t.Error("the fingerprint changed on an unchanged second run")
	}
	if got := f.log.text(); strings.Contains(got, "REGENERATED") {
		t.Errorf("an unchanged run must say nothing about rotation, got:\n%s", got)
	}
}

func TestInterfaceEnumerationOrderDoesNotRotate(t *testing.T) {
	// Interfaces do not enumerate in a guaranteed order. If the SAN set were
	// compared unsorted, every restart would look like an address change and
	// rotate the certificate — re-prompting every browser for nothing.
	f := newFixture(t, "dinnerpi", "192.168.1.42", "10.8.0.3", "2001:db8::5")
	first := f.ensure(t)

	f.setIPs("2001:db8::5", "192.168.1.42", "10.8.0.3")
	second := f.ensure(t)

	if second.LeafGenerated {
		t.Error("re-ordered interfaces must not rotate the certificate")
	}
	if first.LeafFingerprint() != second.LeafFingerprint() {
		t.Error("re-ordered interfaces changed the fingerprint")
	}
}

func TestRegeneratesWhenTheAddressSetChanges(t *testing.T) {
	f := newFixture(t, "dinnerpi", "192.168.1.42")
	first := f.ensure(t)
	f.log.reset()

	// DHCP hands out a different lease.
	f.setIPs("192.168.1.77")
	second := f.ensure(t)

	if !second.LeafGenerated {
		t.Fatal("a changed address set must reissue the server certificate")
	}
	if second.CAGenerated {
		t.Error("a changed address set must NOT replace the CA; only the leaf rotates")
	}
	if string(first.CACertPEM) != string(second.CACertPEM) {
		t.Error("the CA bytes must survive a leaf rotation, or every installed root is wasted")
	}
	if second.RotatedFrom != first.LeafFingerprint() {
		t.Errorf("RotatedFrom = %q, want the previous fingerprint %q", second.RotatedFrom, first.LeafFingerprint())
	}
	if second.LeafFingerprint() == first.LeafFingerprint() {
		t.Error("the fingerprint must change when the certificate is reissued")
	}

	var gotIPs []string
	for _, ip := range second.LeafCert.IPAddresses {
		gotIPs = append(gotIPs, ip.String())
	}
	if strings.Join(gotIPs, ",") != "127.0.0.1,192.168.1.77,::1" {
		t.Errorf("new IP SANs = %v, want the new lease", gotIPs)
	}

	// The loud part. A certificate that changes under a user is exactly what an
	// interposed connection looks like, so the log must say so unmistakably and
	// must name both fingerprints.
	logged := f.log.text()
	for _, want := range []string{
		"REGENERATED",
		"the set of local addresses changed",
		"192.168.1.77",
		"192.168.1.42",
		"old fingerprint",
		"new fingerprint",
		first.LeafFingerprint(),
		second.LeafFingerprint(),
		"WILL PROMPT AGAIN",
		"/ca.crt",
	} {
		if !strings.Contains(logged, want) {
			t.Errorf("rotation log does not mention %q; got:\n%s", want, logged)
		}
	}
	if !strings.Contains(logged, "addresses added") || !strings.Contains(logged, "addresses removed") {
		t.Errorf("rotation log must say which addresses changed; got:\n%s", logged)
	}
}

func TestRegeneratesWhenTheStoredLeafIsUnusable(t *testing.T) {
	tests := []struct {
		name    string
		damage  func(t *testing.T, dir string)
		reason  string
		wantNew bool
	}{
		{
			name:    "certificate deleted",
			damage:  func(t *testing.T, dir string) { os.Remove(filepath.Join(dir, LeafCertFileName)) },
			reason:  "no server certificate on disk",
			wantNew: true,
		},
		{
			name:    "private key deleted",
			damage:  func(t *testing.T, dir string) { os.Remove(filepath.Join(dir, LeafKeyFileName)) },
			reason:  "the server private key is missing",
			wantNew: true,
		},
		{
			name: "certificate corrupt",
			damage: func(t *testing.T, dir string) {
				os.WriteFile(filepath.Join(dir, LeafCertFileName), []byte("not a certificate"), 0o600)
			},
			reason:  "could not be parsed",
			wantNew: true,
		},
		{
			name: "signed by a CA that is no longer on disk",
			damage: func(t *testing.T, dir string) {
				// Replace the CA, leaving the old leaf in place.
				os.Remove(filepath.Join(dir, CACertFileName))
				os.Remove(filepath.Join(dir, CAKeyFileName))
			},
			reason:  "not signed by the certificate authority",
			wantNew: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, "dinnerpi", "192.168.1.42")
			f.ensure(t)
			f.log.reset()
			tc.damage(t, f.dir)

			m := f.ensure(t)
			if m.LeafGenerated != tc.wantNew {
				t.Fatalf("LeafGenerated = %v, want %v", m.LeafGenerated, tc.wantNew)
			}
			if tc.reason != "" && !strings.Contains(f.log.text(), tc.reason) {
				t.Errorf("log does not give the reason %q; got:\n%s", tc.reason, f.log.text())
			}
		})
	}
}

func TestRenewsBeforeExpiry(t *testing.T) {
	f := newFixture(t, "dinnerpi", "192.168.1.42")
	first := f.ensure(t)

	// Well inside the one-year window: nothing happens.
	f.now = baseTime.AddDate(0, 6, 0)
	if m := f.ensure(t); m.LeafGenerated {
		t.Error("a certificate with six months left must not be reissued")
	}

	// Inside RenewBefore of expiry: reissue while the old one still works.
	f.now = first.LeafCert.NotAfter.Add(-RenewBefore).Add(time.Hour)
	m := f.ensure(t)
	if !m.LeafGenerated {
		t.Fatal("a certificate about to expire must be reissued")
	}
	if !strings.Contains(f.log.text(), "expired or is about to") {
		t.Errorf("log does not give the expiry reason; got:\n%s", f.log.text())
	}
}

func TestCAIsReplacedWhenItExpires(t *testing.T) {
	f := newFixture(t, "dinnerpi", "192.168.1.42")
	first := f.ensure(t)

	f.now = first.CACert.NotAfter.Add(time.Hour)
	m := f.ensure(t)
	if !m.CAGenerated {
		t.Fatal("an expired CA must be replaced")
	}
	if !m.LeafGenerated {
		t.Fatal("a new CA means the old leaf no longer chains; it must be reissued too")
	}
	if !strings.Contains(f.log.text(), "install the new one from /ca.crt") {
		t.Errorf("replacing the CA must tell the user to reinstall it; got:\n%s", f.log.text())
	}
}

func TestFileModes(t *testing.T) {
	f := newFixture(t, "dinnerpi", "192.168.1.42")
	f.ensure(t)

	if got := perm(t, f.dir); got != DirMode {
		t.Errorf("directory mode = %#o, want %#o", got, DirMode)
	}
	for _, name := range []string{CACertFileName, CAKeyFileName, LeafCertFileName, LeafKeyFileName} {
		if got := perm(t, filepath.Join(f.dir, name)); got != FileMode {
			t.Errorf("%s mode = %#o, want %#o", name, got, FileMode)
		}
	}
}

func TestExistingLooseModesAreTightened(t *testing.T) {
	// A restore, an earlier version, or a careless hand can leave a private key
	// world-readable. Inheriting that mode silently is how a key leaks.
	f := newFixture(t, "dinnerpi", "192.168.1.42")
	if err := os.MkdirAll(f.dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(f.dir, 0o777); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{CACertFileName, CAKeyFileName, LeafCertFileName, LeafKeyFileName} {
		if err := os.WriteFile(filepath.Join(f.dir, name), []byte("stale"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	f.ensure(t)

	if got := perm(t, f.dir); got != DirMode {
		t.Errorf("directory mode = %#o, want %#o", got, DirMode)
	}
	for _, name := range []string{CACertFileName, CAKeyFileName, LeafCertFileName, LeafKeyFileName} {
		if got := perm(t, filepath.Join(f.dir, name)); got != FileMode {
			t.Errorf("%s mode = %#o, want %#o", name, got, FileMode)
		}
	}
}

func TestRequiresADirectory(t *testing.T) {
	if _, err := Ensure(Options{}); err == nil {
		t.Fatal("Ensure without a Dir must fail rather than write somewhere surprising")
	}
}

func TestTheCertificateActuallyVerifies(t *testing.T) {
	// The point of all of this is a browser that can open the camera, so prove
	// a real handshake against the generated material verifies against the CA.
	f := newFixture(t, "dinnerpi", "192.168.1.42")
	// This one needs the real clock: a certificate dated from a fixed test time
	// would be rejected by the handshake for not being valid yet.
	f.now = time.Now()
	m := f.ensure(t)

	tlsCfg, err := m.ServerTLSConfig()
	if err != nil {
		t.Fatalf("ServerTLSConfig: %v", err)
	}

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "ok")
	}))
	srv.TLS = tlsCfg
	srv.StartTLS()
	defer srv.Close()

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(m.CACertPEM) {
		t.Fatal("the CA PEM did not parse into a pool")
	}
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
	}}

	// httptest listens on 127.0.0.1, which is an IP SAN on the leaf, so this
	// verifies the address path a phone uses rather than a name.
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("handshake against the generated certificate failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Errorf("body = %q", body)
	}

	// And without the CA it must fail, or the test above proves nothing.
	bare := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
	}}
	if _, err := bare.Get(srv.URL); err == nil {
		t.Error("a client without the CA must not trust this certificate")
	}
}

func TestCACertHandler(t *testing.T) {
	f := newFixture(t, "dinnerpi", "192.168.1.42")
	m := f.ensure(t)
	h := CACertHandler(m.CACertPEM)

	t.Run("GET serves the certificate", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ca.crt", nil))

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if got := rec.Header().Get("Content-Type"); got != CACertContentType {
			t.Errorf("Content-Type = %q, want %q", got, CACertContentType)
		}
		if rec.Body.String() != string(m.CACertPEM) {
			t.Error("body is not the CA certificate")
		}
		if !strings.Contains(rec.Body.String(), "BEGIN CERTIFICATE") {
			t.Error("body is not PEM")
		}
		// The private key must never be within reach of this handler.
		if strings.Contains(rec.Body.String(), "PRIVATE KEY") {
			t.Fatal("the handler served a private key")
		}
	})

	t.Run("HEAD sends headers only", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/ca.crt", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if rec.Body.Len() != 0 {
			t.Error("HEAD must not send a body")
		}
	})

	t.Run("POST is rejected", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/ca.crt", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status = %d, want 405", rec.Code)
		}
	})
}

func TestFingerprintFormat(t *testing.T) {
	f := newFixture(t, "dinnerpi", "192.168.1.42")
	m := f.ensure(t)

	fp := m.LeafFingerprint()
	parts := strings.Split(fp, ":")
	if len(parts) != 32 {
		t.Fatalf("fingerprint has %d octets, want 32 (SHA-256)", len(parts))
	}
	for _, p := range parts {
		if len(p) != 2 {
			t.Fatalf("octet %q is not two hex digits", p)
		}
		if strings.ToUpper(p) != p {
			t.Fatalf("octet %q is not uppercase; browsers display uppercase and a user has to compare by eye", p)
		}
	}
	if Fingerprint(nil) != "" {
		t.Error("Fingerprint(nil) must be empty, not a panic")
	}
}

func TestDetectLocalIPsExcludesLoopback(t *testing.T) {
	ips, err := DetectLocalIPs()
	if err != nil {
		t.Fatalf("DetectLocalIPs: %v", err)
	}
	for _, ip := range ips {
		if ip.IsLoopback() {
			t.Errorf("%s is loopback; the loopback literals are added separately", ip)
		}
		if ip.IsUnspecified() {
			t.Errorf("%s is unspecified and cannot appear in a certificate", ip)
		}
	}
}
