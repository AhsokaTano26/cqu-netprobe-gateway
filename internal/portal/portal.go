package portal

import (
	"embed"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/tano/cqu-netprobe-gateway/internal/config"
	"github.com/tano/cqu-netprobe-gateway/internal/store"
	"github.com/tano/cqu-netprobe-gateway/internal/webui"
)

//go:embed templates/*.html
var pageFS embed.FS

// portalPages are the templates this package owns.
var portalPages = []string{"register.html", "token.html"}

// oneShotTTL bounds how long a freshly generated token stays displayable.
const oneShotTTL = 60 * time.Second

// Deps are the portal's collaborators.
type Deps struct {
	Store   *store.Store
	Config  *config.Config
	Limiter *RegisterLimiter
	Logger  *slog.Logger
	Now     func() time.Time
}

// Server serves the public, unauthenticated pages. It holds no session store and
// imports no authentication code, so no route here can accidentally require a
// login — and none can accidentally be granted one.
type Server struct {
	store     *store.Store
	cfg       *config.Config
	limiter   *RegisterLimiter
	logger    *slog.Logger
	now       func() time.Time
	oneShot   *oneShotStore
	templates webui.Templates
}

// NewServer builds the portal.
func NewServer(d Deps) (*Server, error) {
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := d.Now
	if now == nil {
		now = time.Now
	}
	// The embed pattern keeps this package's page files under templates/; Parse
	// wants them at the root of the FS it is handed, exactly as admin does it.
	pages, err := fs.Sub(pageFS, "templates")
	if err != nil {
		return nil, err
	}
	tmpl, err := webui.Parse(pages, portalPages...)
	if err != nil {
		return nil, err
	}
	return &Server{
		store:     d.Store,
		cfg:       d.Config,
		limiter:   d.Limiter,
		logger:    logger,
		now:       now,
		oneShot:   newOneShotStore(oneShotTTL, now),
		templates: tmpl,
	}, nil
}

// MintTokenSlot stores a plaintext token in the one-shot display store and
// returns the slot that redeems it. back is the path the token page's return
// button leads to, so the admin flows can send the operator back to the probe
// list instead of to the public registration form.
//
// This package owns that store because it owns the page that redeems it: the
// admin UI calls this so its create and rotate flows redirect to the single
// public /token/{slot} instead of minting into a store of its own, which the
// page could never read. The slot carries the probe ID server-side, so a crafted
// URL cannot put another ID beside a real token.
//
// back is stored, never read from the request. A caller-supplied return path
// would be an open redirect; one that arrives through this method is chosen by
// this program.
func (s *Server) MintTokenSlot(probeID, token, back string) (string, error) {
	return s.oneShot.putPair(probeID, token, back)
}

// Routes returns the public mux.
//
// The form routes use the {$} anchor so they match ONLY the root path. Without
// it, "GET /" is a subtree pattern and the portal would answer every unmatched
// GET on the listener — /metrics, a typo'd admin path, anything — with the
// registration form instead of a 404.
func (s *Server) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("GET /{$}", http.HandlerFunc(s.handleForm))
	mux.Handle("POST /{$}", http.HandlerFunc(s.handleRegister))
	mux.Handle("GET /token/{slot}", http.HandlerFunc(s.handleTokenShow))
	return mux
}

// clientIP strips the port from the peer address. Forwarded headers are
// deliberately ignored: they are caller-controlled, and no trusted proxy sits in
// front of this gateway by design.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// originAllowed reports whether a state-changing request came from our own
// origin. POST / is the only unauthenticated state-changing route in the
// gateway, so it has no session to bind a CSRF token to; checking Origin blocks
// the drive-by case where a third-party page makes a visitor's browser submit.
// A request with no Origin is allowed — some older clients omit it, and the
// worst outcome is one extra record awaiting approval.
func (s *Server) originAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	base, err := url.Parse(s.cfg.PublicBaseURL)
	if err != nil || base.Host == "" {
		// Without a configured base URL there is nothing to compare against;
		// failing closed here would break every registration.
		return true
	}
	return u.Host == base.Host
}
