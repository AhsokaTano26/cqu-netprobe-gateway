package admin

import (
	"net/http"

	"github.com/tano/cqu-netprobe-gateway/internal/clientip"
	"github.com/tano/cqu-netprobe-gateway/internal/webui"
)

// sessionFromRequest returns the live session for a request, if any.
func (s *Server) sessionFromRequest(r *http.Request) (*session, bool) {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return nil, false
	}
	return s.sessions.get(c.Value)
}

// requireSession redirects unauthenticated browsers to the login page.
func (s *Server) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.sessionFromRequest(r); !ok {
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requireCSRF enforces a per-session token on every state-changing request.
// Session cookies are SameSite=Lax, which already blocks cross-site form
// posts; this is the second layer, and it also covers same-site mistakes.
func (s *Server) requireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, ok := s.sessionFromRequest(r)
		if !ok {
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
		if r.PostFormValue("csrf") != sess.csrf {
			http.Error(w, "invalid CSRF token", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// setSessionCookie issues the session cookie. Secure is set when the public
// base URL is HTTPS, so a plain-HTTP development deployment still works.
func (s *Server) setSessionCookie(w http.ResponseWriter, id string) {
	c := &http.Cookie{
		Name:     sessionCookieName,
		Value:    id,
		Path:     "/admin",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   isHTTPS(s.cfg.PublicBaseURL),
	}
	http.SetCookie(w, c)
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/admin",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   isHTTPS(s.cfg.PublicBaseURL),
	})
}

func isHTTPS(baseURL string) bool {
	return len(baseURL) >= 8 && baseURL[:8] == "https://"
}

// handleLoginPage renders the login form.
func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.sessionFromRequest(r); ok {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	webui.Render(w, http.StatusOK, s.templates, "login.html", webui.PageData{Title: "登录"})
}

// handleLogin verifies credentials and starts a session.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	username := r.PostFormValue("username")
	password := r.PostFormValue("password")

	// Always run the password comparison, even on a wrong username, so the
	// response time does not reveal whether the username exists.
	passwordErr := verifyPassword(s.passwordHash, password)
	if username != s.username || passwordErr != nil {
		// The submitted password is never logged or echoed.
		s.logger.Warn("admin login failed", "username", username, "remote_ip", clientip.From(r, s.cfg.TrustedProxyCIDRs))
		webui.Render(w, http.StatusUnauthorized, s.templates, "login.html", webui.PageData{
			Title: "登录",
			Error: "用户名或密码错误",
		})
		return
	}

	sess, err := s.sessions.create(username)
	if err != nil {
		s.logger.Error("failed to create session", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.setSessionCookie(w, sess.id)
	s.logger.Info("admin login succeeded", "username", username, "remote_ip", clientip.From(r, s.cfg.TrustedProxyCIDRs))
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

// handleLogout ends the session.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookieName); err == nil {
		s.sessions.destroy(c.Value)
	}
	s.clearSessionCookie(w)
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}
