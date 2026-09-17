package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/IniTechInc/dinnerDecider/linux/internal/api"
	"github.com/IniTechInc/dinnerDecider/linux/internal/auth"
	"github.com/IniTechInc/dinnerDecider/linux/internal/config"
	"github.com/IniTechInc/dinnerDecider/linux/internal/contract"
	"github.com/IniTechInc/dinnerDecider/linux/internal/demo"
	"github.com/IniTechInc/dinnerDecider/linux/internal/llm"
	"github.com/IniTechInc/dinnerDecider/linux/internal/model"
	"github.com/IniTechInc/dinnerDecider/linux/internal/scan"
	"github.com/IniTechInc/dinnerDecider/linux/internal/store"
	"github.com/IniTechInc/dinnerDecider/linux/internal/tlsgen"
	"github.com/IniTechInc/dinnerDecider/linux/internal/vision"
	"github.com/IniTechInc/dinnerDecider/linux/web"
)

// service is the assembled process: every long-lived object, plus the listeners
// serving them and the teardown that takes them all down in the right order.
//
// This file is the composition root api.Deps names in its doc comment -- "the
// only place that knows about all of these at once". Nothing below it imports
// anything above it; every package here was written to be constructed from the
// outside and none of them reach for a global.
type service struct {
	cfg  config.Config
	logf func(format string, args ...any)

	db     *store.DB
	photos *store.PhotoStore

	tokens *auth.TokenStore
	authn  *auth.Authenticator
	tlsMat *tlsgen.Material

	// sup is the real inference backend, or nil when this process is serving
	// sample data. See chooseBackend for what decides that.
	sup  *llm.Supervisor
	hub  *api.ModelHub
	dl   *model.Downloader
	stop []func()

	router *api.Router
}

// sweepInterval is how often expired photos are swept. The retention itself is
// configurable in hours, so an hourly sweep is the finest granularity that
// setting can express.
const sweepInterval = time.Hour

// shutdownGrace bounds the whole teardown. A resident model child gets SIGTERM
// and a drain inside this, so it is generous by the standards of an HTTP
// server: leaving a 4 GB llama-server orphaned is the worst available outcome.
const shutdownGrace = 90 * time.Second

// buildService assembles everything. It deliberately does NOT do anything slow:
// no model load, no llama-server download, no network. Those are started by run
// once the listeners are up, because a box whose first boot has to fetch a
// runtime must still answer its own web UI while it does.
func buildService(ctx context.Context, cfg config.Config, logf func(string, ...any)) (*service, error) {
	s := &service{cfg: cfg, logf: logf}

	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return nil, fmt.Errorf("state dir %s: %w", cfg.StateDir, err)
	}

	db, err := store.Open(filepath.Join(cfg.StateDir, "dinnerdecider.db"))
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	s.db = db
	if err := db.Migrate(ctx); err != nil {
		s.closeAll()
		return nil, fmt.Errorf("migrate database: %w", err)
	}

	photos, err := store.NewPhotoStore(db, cfg.StateDir)
	if err != nil {
		s.closeAll()
		return nil, fmt.Errorf("photo store: %w", err)
	}
	s.photos = photos

	if err := s.setupTLS(); err != nil {
		s.closeAll()
		return nil, err
	}
	if err := s.setupAuth(); err != nil {
		s.closeAll()
		return nil, err
	}

	locator := s.locator(ctx)
	target := llm.Detect()
	s.logf("[service] hardware: %s", describeTarget(target))

	backend := s.chooseBackend(locator, target)

	s.hub = api.NewModelHub()
	s.dl, err = model.NewDownloader(model.Options{
		Dir:      filepath.Join(cfg.StateDir, "models"),
		Metered:  cfg.Metered,
		OnUpdate: s.hub.Publish,
	})
	if err != nil {
		// A downloader that cannot be built is not fatal: the operator may have
		// side-loaded the weights, and every other route still works. Say so
		// rather than refusing to start.
		s.logf("[service] model downloads unavailable: %v", err)
		s.dl = nil
	}

	engine := &scan.Engine{
		Model:      backend,
		OCR:        s.ocr(),
		CallBudget: cfg.ScanCallBudget,
		Logf:       s.logf,
	}
	scans := scan.NewManager(engine, scan.ManagerOptions{
		Photos: photos,
		// BaseContext is deliberately context.Background() and NOT the
		// request-scoped ctx passed in here: a scan outlives the HTTP request
		// that started it by minutes, which is the entire reason the manager
		// takes this as a named option.
		BaseContext: context.Background(),
		MaxScans:    cfg.MaxConcurrentScans,
		Logf:        s.logf,
	})

	deps := api.Deps{
		DB:          db,
		Photos:      photos,
		Scans:       scans,
		Recipes:     engine,
		Downloads:   s.downloads(),
		ModelEvents: s.hub,
		Locator:     locator,
		Auth:        s.authn,
		UI:          web.Handler(),
		Config:      cfg,
		Kroger:      api.LoadKrogerConfig(""),
		OCRMode:     engine.OCR.Mode(),
		Target:      target,
		Build:       buildInfo(),
		Limits:      s.limits(),
		Logf:        s.logf,
	}
	// Model stays nil unless a real backend exists. That nil is load-bearing:
	// api.Router.demoMode reads it to tell the user "this service is running
	// without an inference backend, so scans return sample data". Handing it a
	// wrapper that reports Unloaded would make the status surface claim a real
	// backend that is not there.
	if s.sup != nil {
		deps.Model = s.sup
	}
	if s.tlsMat != nil {
		deps.CACertPEM = s.tlsMat.CACertPEM
	}

	rt, err := api.New(deps)
	if err != nil {
		s.closeAll()
		return nil, fmt.Errorf("build router: %w", err)
	}
	s.router = rt
	return s, nil
}

// setupTLS resolves the certificate material for the configured mode.
func (s *service) setupTLS() error {
	switch s.cfg.TLS {
	case config.TLSOff:
		s.logf("[service] TLS off: the browser camera API is unavailable over plain HTTP on a LAN " +
			"address; the file-upload path is the fallback")
		return nil

	case config.TLSSelfSigned:
		mat, err := tlsgen.Ensure(tlsgen.Options{
			Dir:  filepath.Join(s.cfg.StateDir, "tls"),
			Logf: s.logf,
		})
		if err != nil {
			return fmt.Errorf("self-signed TLS: %w", err)
		}
		s.tlsMat = mat
		if mat.CAGenerated {
			s.logf("[service] a NEW certificate authority was generated: every previously paired " +
				"device must install the new CA from /ca.crt")
		}
		return nil

	case config.TLSFiles:
		// Validated by config, loaded per-listener in tlsConfig.
		return nil

	default:
		return fmt.Errorf("unknown TLS mode %q", s.cfg.TLS)
	}
}

// setupAuth builds the authenticator. In token mode the token is Ensured here,
// so a first boot prints one rather than refusing every request with no way to
// find out why.
func (s *service) setupAuth() error {
	s.tokens = auth.NewTokenStore(s.cfg.StateDir)

	if s.cfg.Auth == config.AuthToken {
		token, created, err := s.tokens.Ensure()
		if err != nil {
			return fmt.Errorf("pairing token: %w", err)
		}
		if created {
			s.logf("[service] a new pairing token was generated: %s", token)
			s.logf("[service] show it again with: dinnerdecider token show")
		}
	}

	authn, err := auth.New(auth.Params{
		Mode:       s.cfg.Auth,
		Tokens:     s.tokens,
		SessionTTL: auth.DefaultSessionTTL,
		// The cookie is Secure whenever this process terminates TLS. With TLS
		// off it must not be, or the browser drops it and pairing silently
		// never sticks.
		SecureCookies: s.cfg.TLSEnabled(),
	})
	if err != nil {
		return fmt.Errorf("authenticator: %w", err)
	}
	s.authn = authn
	return nil
}

// locator resolves where the weights are, honouring the user's pinned choice.
func (s *service) locator(ctx context.Context) model.Locator {
	preferred, err := s.db.SettingOr(ctx, model.SelectedModelKey, "")
	if err != nil {
		s.logf("[service] could not read the selected model setting: %v", err)
	}
	return model.Locator{
		ModelsDir: s.cfg.ModelsDir,
		StateDir:  s.cfg.StateDir,
		Preferred: preferred,
	}
}

// chooseBackend decides between the real supervisor and the demo backend, and
// says out loud which one it picked and why.
//
// The decision is made ONCE, at startup, and the process does not change its
// mind later. A model downloaded while the service is running therefore needs a
// restart to take effect, which is logged at the moment the download lands
// rather than left for the user to discover from sample data.
func (s *service) chooseBackend(locator model.Locator, target llm.Target) scan.Model {
	demoBackend := &demo.Model{Logf: s.logf}

	if !target.HasOfficialBuild() {
		s.logf("[service] DEMO MODE: no official llama-server build exists for %s; "+
			"scans return sample data", target.GOARCH)
		return demoBackend
	}

	weights, haveWeights := locator.ModelPath()
	mmproj, haveMmproj := locator.MmprojPath()
	switch {
	case !haveWeights && !haveMmproj:
		s.logf("[service] DEMO MODE: no model weights or vision projector under %v; "+
			"scans return sample data until they are installed",
			locator.SearchDirectories())
		return demoBackend
	case !haveWeights:
		s.logf("[service] DEMO MODE: the model weights are missing; scans return sample data")
		return demoBackend
	case !haveMmproj:
		// Worth its own message: weights without an mmproj is the failure that
		// looks like a working install right up until the model cannot see the
		// photo at all.
		s.logf("[service] DEMO MODE: the vision projector (mmproj) is missing, so the model " +
			"cannot see images; scans return sample data")
		return demoBackend
	}

	mem, err := llm.CheckMemory()
	idle := time.Duration(s.cfg.IdleUnloadMinutes) * time.Minute
	if err != nil {
		s.logf("[service] memory preflight unavailable (%v); using the configured idle unload of %s", err, idle)
	} else {
		// IdleUnloadPolicy turns the preflight into the right dwell time: a box
		// with little headroom gives the weights back sooner.
		idle = llm.IdleUnloadPolicy(mem)
		s.logf("[service] memory: %s; idle unload after %s", mem, idle)
	}

	sup, err := llm.New(llm.Options{
		ModelPath:         weights,
		MMProjPath:        mmproj,
		ContextSize:       s.cfg.ContextSize,
		BatchSize:         s.cfg.BatchSize,
		ParallelSlots:     s.cfg.ParallelSlots,
		InferQueueTimeout: s.cfg.InferQueueTimeout,
		IdleUnload:        idle,
		KnownAnswerProbe:  true,
		Logf:              s.logf,
	})
	if err != nil {
		// A rejected supervisor configuration is an operator error worth
		// shouting about, but it is not a reason to serve nothing.
		s.logf("[service] DEMO MODE: the inference backend was refused: %v", err)
		return demoBackend
	}

	s.sup = sup
	s.logf("[service] inference backend ready: weights=%s mmproj=%s", weights, mmproj)
	return sup
}

// ocr picks the OCR implementation for the configured mode.
func (s *service) ocr() vision.OCR {
	switch s.cfg.OCR {
	case config.OCROff:
		s.logf("[vision] OCR disabled by configuration; scans run image-only")
		return vision.NoopOCR{}
	case config.OCROn:
		ocr := vision.DetectOCR()
		if ocr.Mode() == vision.OCRModeNone {
			// ocr=on is a request, not a guarantee. Saying so beats letting the
			// operator believe OCR is running.
			s.logf("[vision] OCR was requested but tesseract is not on PATH; scans run image-only")
		}
		return ocr
	default:
		return vision.DetectOCR()
	}
}

// downloads adapts the concrete downloader to the API's interface without
// handing the router a typed nil, which would be non-nil as an interface and
// defeat every `if deps.Downloads != nil` guard downstream.
func (s *service) downloads() api.ModelDownloader {
	if s.dl == nil {
		return nil
	}
	return s.dl
}

// limits folds the operator's configured caps into the shipped defaults.
func (s *service) limits() api.Limits {
	return api.Limits{
		MaxFileBytes:       s.cfg.MaxUploadBytes,
		MaxUploadPixels:    s.cfg.MaxUploadPixels,
		MaxConcurrentScans: s.cfg.MaxConcurrentScans,
		MaxSSEPerScan:      s.cfg.MaxSSEPerScan,
		MaxSSETotal:        s.cfg.MaxSSETotal,
	}
}

// run serves until ctx is cancelled, then shuts everything down.
func (s *service) run(ctx context.Context) error {
	defer s.closeAll()

	servers, err := s.listeners()
	if err != nil {
		return err
	}

	errs := make(chan error, len(servers))
	for _, srv := range servers {
		go func(srv *listener) {
			s.logf("[service] listening on %s", srv.url())
			err := srv.serve()
			if errors.Is(err, http.ErrServerClosed) {
				err = nil
			}
			errs <- err
		}(srv)
	}

	// Only now, with the listeners up, do the slow things start.
	s.startBackground(ctx)

	var runErr error
	select {
	case <-ctx.Done():
		s.logf("[service] shutting down")
	case runErr = <-errs:
		if runErr != nil {
			s.logf("[service] listener failed: %v", runErr)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownGrace)
	defer cancel()
	for _, srv := range servers {
		if err := srv.server.Shutdown(shutdownCtx); err != nil && runErr == nil {
			runErr = err
		}
	}
	if s.sup != nil {
		if err := s.sup.Shutdown(shutdownCtx); err != nil {
			s.logf("[service] inference shutdown: %v", err)
		}
	}
	return runErr
}

// listener pairs a server with the address and scheme it serves, so the startup
// log can print a URL a user can actually paste.
type listener struct {
	server *http.Server
	scheme string
	host   string
	port   int
	tls    *tls.Config
}

func (l *listener) url() string {
	return fmt.Sprintf("%s://%s", l.scheme, net.JoinHostPort(l.host, fmt.Sprint(l.port)))
}

func (l *listener) serve() error {
	if l.tls != nil {
		l.server.TLSConfig = l.tls
		return l.server.ListenAndServeTLS("", "")
	}
	return l.server.ListenAndServe()
}

// listeners builds the one or two servers this configuration calls for.
//
// With TLS on, BOTH are served, and that is deliberate. getUserMedia is gated
// to secure contexts, so the camera only works on the HTTPS origin -- but a
// browser cannot reach that origin at all until it trusts the CA, and the CA is
// fetched from the unauthenticated /ca.crt route. Serving only HTTPS makes the
// first visit a dead end; serving only HTTP makes the camera permanently
// unavailable. The plain listener is the bootstrap and the upload fallback.
func (s *service) listeners() ([]*listener, error) {
	plain := &listener{
		server: s.router.Server(net.JoinHostPort(s.cfg.Bind, fmt.Sprint(s.cfg.Port))),
		scheme: "http",
		host:   s.cfg.Bind,
		port:   s.cfg.Port,
	}
	if !s.cfg.TLSEnabled() {
		return []*listener{plain}, nil
	}

	tlsCfg, err := s.tlsConfig()
	if err != nil {
		return nil, err
	}
	secure := &listener{
		server: s.router.Server(net.JoinHostPort(s.cfg.Bind, fmt.Sprint(s.cfg.TLSPort))),
		scheme: "https",
		host:   s.cfg.Bind,
		port:   s.cfg.TLSPort,
		tls:    tlsCfg,
	}
	return []*listener{secure, plain}, nil
}

func (s *service) tlsConfig() (*tls.Config, error) {
	var cert tls.Certificate
	var err error

	switch s.cfg.TLS {
	case config.TLSFiles:
		cert, err = tls.LoadX509KeyPair(s.cfg.TLSCertFile, s.cfg.TLSKeyFile)
		if err != nil {
			return nil, fmt.Errorf("load TLS keypair: %w", err)
		}
	case config.TLSSelfSigned:
		if s.tlsMat == nil {
			return nil, errors.New("self-signed TLS was selected but no material was generated")
		}
		cert, err = tls.X509KeyPair(s.tlsMat.LeafCertPEM, s.tlsMat.LeafKeyPEM)
		if err != nil {
			return nil, fmt.Errorf("self-signed keypair: %w", err)
		}
	default:
		return nil, fmt.Errorf("TLS is not enabled for mode %q", s.cfg.TLS)
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		// TLS 1.2 floor: a self-signed LAN service still has no reason to
		// negotiate anything older, and every browser that can run this UI
		// speaks 1.2.
		MinVersion: tls.VersionTLS12,
	}, nil
}

// startBackground starts everything that may be slow or may fail: the inference
// monitors, the runtime resolution (which can mean a several-hundred-megabyte
// download on first boot), the memory-pressure poller and the photo sweeper.
func (s *service) startBackground(ctx context.Context) {
	s.startPhotoSweeper(ctx)

	if s.sup == nil {
		return
	}

	s.startPressurePoller(ctx)
	go s.resolveRuntime(ctx)
}

// resolveRuntime finds a llama-server and hands it to the supervisor.
//
// This runs after the listeners are up because on a fresh box it downloads a
// runtime, and a Pi on a slow link should be serving its own UI while that
// happens rather than looking dead.
func (s *service) resolveRuntime(ctx context.Context) {
	resolver := &llm.Resolver{
		Target:        llm.Detect(),
		StateDir:      s.cfg.StateDir,
		ExplicitPath:  s.cfg.LlamaServerPath,
		State:         llm.NewFileStateStore(s.cfg.StateDir),
		AllowDownload: !s.cfg.Metered,
		Logf:          s.logf,
	}

	rt, err := resolver.Resolve(ctx)
	if err != nil {
		s.logf("[service] no llama-server could be resolved: %v", err)
		s.logf("[service] scans will fail until one is available; set --llama-server-path " +
			"or allow downloads")
		return
	}

	s.sup.SetRuntime(rt)
	if rt.Degraded() {
		s.logf("[service] runtime %s (%s) DEGRADED: %s", rt.Version, rt.Variant, rt.FallbackReason)
	} else {
		s.logf("[service] runtime %s (%s, from %s)", rt.Version, rt.Variant, rt.Source)
	}

	if err := s.sup.Start(ctx); err != nil {
		s.logf("[service] inference monitors failed to start: %v", err)
	}
}

// startPressurePoller funnels memory pressure into the supervisor.
//
// The handler must be idempotent, which it is: HandleMemoryPressure is a
// lifecycle transition, not an action, and re-delivering the same level is a
// no-op the poller's contract explicitly permits.
func (s *service) startPressurePoller(ctx context.Context) {
	poller := llm.NewPoller(llm.PollerOptions{
		Handler: func(level contract.MemoryPressureLevel, _ llm.Reading) {
			decision := s.sup.HandleMemoryPressure(level)
			s.logf("[service] memory pressure %v -> %v", level, decision)
		},
		Logf: s.logf,
	})
	s.stop = append(s.stop, poller.Start(ctx))
}

// startPhotoSweeper deletes staged photos past their retention.
//
// It sweeps once immediately: a process that crashed mid-scan yesterday left
// photos behind, and waiting an hour to clean them up on a 32 GB SD card is a
// real cost.
func (s *service) startPhotoSweeper(ctx context.Context) {
	retention := time.Duration(s.cfg.PhotoRetentionHours) * time.Hour
	if retention <= 0 {
		s.logf("[service] photo retention is disabled; staged photos are kept until a scan removes them")
		return
	}

	sweep := func() {
		n, err := s.photos.SweepExpired(ctx, retention)
		if err != nil {
			s.logf("[service] photo sweep failed: %v", err)
			return
		}
		if n > 0 {
			s.logf("[service] swept %d photo(s) older than %s", n, retention)
		}
	}

	go func() {
		sweep()
		ticker := time.NewTicker(sweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sweep()
			}
		}
	}()
}

// closeAll releases everything buildService acquired, in reverse order, and is
// safe to call on a partially built service.
func (s *service) closeAll() {
	for i := len(s.stop) - 1; i >= 0; i-- {
		if s.stop[i] != nil {
			s.stop[i]()
		}
	}
	s.stop = nil

	if s.hub != nil {
		s.hub.Close()
		s.hub = nil
	}
	if s.db != nil {
		if err := s.db.Close(); err != nil {
			s.logf("[service] closing the database: %v", err)
		}
		s.db = nil
	}
}

// describeTarget renders the detection result for the startup log.
func describeTarget(t llm.Target) string {
	if !t.HasOfficialBuild() {
		return fmt.Sprintf("%s (no official llama-server build)", t.GOARCH)
	}
	return fmt.Sprintf("%s, llama-server variant %q", t.GOARCH, t.AssetVariant)
}
