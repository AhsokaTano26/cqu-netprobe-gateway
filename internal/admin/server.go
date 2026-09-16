package admin

import (
	"embed"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/tano/cqu-netprobe-gateway/internal/config"
	"github.com/tano/cqu-netprobe-gateway/internal/latest"
	"github.com/tano/cqu-netprobe-gateway/internal/store"
	"github.com/tano/cqu-netprobe-gateway/internal/webui"
)

// adminPages are the templates this package owns. The shared layout lives in
// internal/webui; the portal declares its own list separately.
var adminPages = []string{
	"login.html",
	"probes.html",
	"probe_new.html",
	"probe_detail.html",
	"targets.html",
	"campuses.html",
	"buildings.html",
}

// pageFS holds this package's page files. An embed pattern keeps the directory
// name in every path, so it is rooted at templates/ below: a page is named by
// its file name alone, which is the key Render looks it up by.
//
//go:embed templates/*.html
var pageFS embed.FS

// sessionCookieName is the cookie holding the admin session ID.
const sessionCookieName = "netprobe_admin_session"

// adminPasswordSettingKey is the settings row holding the bcrypt hash of a
// gateway-generated admin password.
const adminPasswordSettingKey = "admin_password_hash"

// defaultOnlineThreshold is how recently a probe must have pushed to count as
// online. It matches the metrics collector's window so the UI and /metrics
// never disagree.
const defaultOnlineThreshold = 30 * time.Second

// Limiter drops per-probe state when a probe is deleted or disabled. It is
// declared here as a one-method interface so this package does not import api.
type Limiter interface {
	Remove(probeID string)
}

// OneShot mints the single-use display slot a freshly generated plaintext token
// is shown through. It is declared here as a one-method interface so this
// package does not import portal, exactly as Limiter keeps it out of api.
//
// The sink must be the portal's slot store, never a store of this package's own:
// create and rotate redirect to the portal's public /token/{slot}, so a slot held
// in admin's memory would be a URL that page could never redeem. One store, one
// page, one URL — and the 60 second TTL lives with the page that enforces it.
type OneShot interface {
	MintTokenSlot(probeID, token string) (string, error)
}

// Deps are the admin server's collaborators.
type Deps struct {
	Store           *store.Store
	Config          *config.Config
	Logger          *slog.Logger
	Now             func() time.Time
	Latest          *latest.Store
	Limiter         Limiter
	OneShot         OneShot
	OnlineThreshold time.Duration
}

// Server renders the management interface.
type Server struct {
	store           *store.Store
	cfg             *config.Config
	logger          *slog.Logger
	now             func() time.Time
	sessions        *sessionStore
	oneShot         OneShot
	templates       webui.Templates
	latest          *latest.Store
	limiter         Limiter
	onlineThreshold time.Duration

	username string
	// passwordHash is the bcrypt hash the login handler compares against.
	passwordHash string
	// generatedPassword holds the plaintext only when this process generated it,
	// so main can log it exactly once. It is empty otherwise.
	generatedPassword string
}

// NewServer builds the admin server and resolves the admin password.
//
// Password resolution, in order:
//  1. ADMIN_PASSWORD set  -> hash it in memory for this process only.
//  2. a hash already stored in settings -> reuse it (a previous run generated it).
//  3. otherwise -> generate one, store its hash, and expose the plaintext via
//     PasswordGeneration so main can log it once.
//
// Generating only once matters: regenerating on every start would silently
// invalidate the password an operator already read from the logs, and would
// leave several live passwords scattered across log files.
func NewServer(d Deps) (*Server, error) {
	if d.OneShot == nil {
		// A nil sink would only surface as a panic on the first probe create,
		// after the probe row was already written.
		return nil, errors.New("admin: Deps.OneShot is required")
	}
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := d.Now
	if now == nil {
		now = time.Now
	}
	onlineThreshold := d.OnlineThreshold
	if onlineThreshold <= 0 {
		onlineThreshold = defaultOnlineThreshold
	}

	pages, err := fs.Sub(pageFS, "templates")
	if err != nil {
		return nil, err
	}
	tmpl, err := webui.Parse(pages, adminPages...)
	if err != nil {
		return nil, err
	}

	s := &Server{
		store:           d.Store,
		cfg:             d.Config,
		logger:          logger,
		now:             now,
		sessions:        newSessionStore(sessionTTL, now),
		oneShot:         d.OneShot,
		templates:       tmpl,
		latest:          d.Latest,
		limiter:         d.Limiter,
		onlineThreshold: onlineThreshold,
		username:        d.Config.AdminUsername,
	}

	if err := s.resolvePassword(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Server) resolvePassword() error {
	if s.cfg.AdminPassword != "" {
		hash, err := hashPassword(s.cfg.AdminPassword)
		if err != nil {
			return err
		}
		s.passwordHash = hash
		return nil
	}

	stored, ok, err := s.store.Setting(adminPasswordSettingKey)
	if err != nil {
		return err
	}
	if ok {
		s.passwordHash = stored
		return nil
	}

	password, hash, err := generateAdminPassword()
	if err != nil {
		return err
	}
	if err := s.store.SetSetting(adminPasswordSettingKey, hash); err != nil {
		return err
	}
	s.passwordHash = hash
	s.generatedPassword = password
	return nil
}

// PasswordGeneration reports the plaintext password if this process generated
// one at startup, and false if the password came from configuration or from a
// previous run. The caller logs it exactly once.
func (s *Server) PasswordGeneration() (string, bool) {
	return s.generatedPassword, s.generatedPassword != ""
}

// Routes returns the admin mux: the session routes, the eight probe routes, the
// four target routes and the catalog routes. The shared stylesheet is not served
// here; main registers webui.StaticHandler once on the public listener.
func (s *Server) Routes() *http.ServeMux {
	mux := http.NewServeMux()

	mux.Handle("GET /admin/login", http.HandlerFunc(s.handleLoginPage))
	mux.Handle("POST /admin/login", http.HandlerFunc(s.handleLogin))
	mux.Handle("POST /admin/logout", s.requireSession(http.HandlerFunc(s.handleLogout)))

	// Probe management (added by Task 17).
	mux.Handle("GET /admin", s.requireSession(http.HandlerFunc(s.handleProbeList)))
	mux.Handle("GET /admin/probes/new", s.requireSession(http.HandlerFunc(s.handleProbeNewForm)))
	mux.Handle("POST /admin/probes/new", s.requireSession(s.requireCSRF(http.HandlerFunc(s.handleProbeCreate))))
	mux.Handle("GET /admin/probes/{id}", s.requireSession(http.HandlerFunc(s.handleProbeDetail)))
	mux.Handle("POST /admin/probes/{id}/toggle", s.requireSession(s.requireCSRF(http.HandlerFunc(s.handleProbeToggle))))
	mux.Handle("POST /admin/probes/{id}/rotate", s.requireSession(s.requireCSRF(http.HandlerFunc(s.handleProbeRotate))))
	mux.Handle("POST /admin/probes/{id}/delete", s.requireSession(s.requireCSRF(http.HandlerFunc(s.handleProbeDelete))))
	// There is no admin token page: create and rotate redirect to the portal's
	// public /token/{slot}, which is the single implementation of that page.

	// Target management (added by Task 18).
	mux.Handle("GET /admin/targets", s.requireSession(http.HandlerFunc(s.handleTargetList)))
	mux.Handle("POST /admin/targets/new", s.requireSession(s.requireCSRF(http.HandlerFunc(s.handleTargetCreate))))
	mux.Handle("POST /admin/targets/{id}/update", s.requireSession(s.requireCSRF(http.HandlerFunc(s.handleTargetUpdate))))
	mux.Handle("POST /admin/targets/{id}/delete", s.requireSession(s.requireCSRF(http.HandlerFunc(s.handleTargetDelete))))

	// Campus catalog (Task 4).
	mux.Handle("GET /admin/campuses", s.requireSession(http.HandlerFunc(s.handleCampusList)))
	mux.Handle("POST /admin/campuses/new", s.requireSession(s.requireCSRF(http.HandlerFunc(s.handleCampusCreate))))
	mux.Handle("POST /admin/campuses/{code}/update", s.requireSession(s.requireCSRF(http.HandlerFunc(s.handleCampusUpdate))))
	mux.Handle("POST /admin/campuses/{code}/delete", s.requireSession(s.requireCSRF(http.HandlerFunc(s.handleCampusDelete))))

	// Building catalog (Task 5).
	mux.Handle("GET /admin/buildings", s.requireSession(http.HandlerFunc(s.handleBuildingList)))
	mux.Handle("POST /admin/buildings/new", s.requireSession(s.requireCSRF(http.HandlerFunc(s.handleBuildingCreate))))
	mux.Handle("POST /admin/buildings/{code}/update", s.requireSession(s.requireCSRF(http.HandlerFunc(s.handleBuildingUpdate))))
	mux.Handle("POST /admin/buildings/{code}/delete", s.requireSession(s.requireCSRF(http.HandlerFunc(s.handleBuildingDelete))))

	return mux
}
