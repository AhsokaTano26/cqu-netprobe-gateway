package admin

import (
	"bytes"
	"embed"
	"html/template"
	"io/fs"
	"net/http"
	"time"
)

//go:embed templates/*.html static/*
var assets embed.FS

// pageTemplates maps a page file name to a template set with the layout applied.
type pageTemplates map[string]*template.Template

// pageFiles lists the pages that have a template file. Tasks 17 and 18 append
// theirs (probes.html, probe_new.html, probe_detail.html, token.html,
// targets.html) as they add the handlers that render them: a name listed here
// without a matching file makes parseTemplates fail and the package unbuildable.
var pageFiles = []string{
	"login.html",
}

// parseTemplates builds one template set per page. Each page file contains only
// a {{define "content"}} block, so the layout can be executed uniformly.
func parseTemplates() (pageTemplates, error) {
	funcs := template.FuncMap{
		"formatTime": func(t time.Time) string {
			if t.IsZero() || t.Unix() <= 0 {
				return "—"
			}
			return t.UTC().Format("2006-01-02 15:04:05 UTC")
		},
		"formatAge": func(t time.Time, now time.Time) string {
			if t.IsZero() || t.Unix() <= 0 {
				return "从未"
			}
			d := now.Sub(t).Round(time.Second)
			if d < 0 {
				d = 0
			}
			return d.String() + " 前"
		},
		"join": func(parts []string) string {
			out := ""
			for i, p := range parts {
				if i > 0 {
					out += ", "
				}
				out += p
			}
			return out
		},
	}

	out := make(pageTemplates, len(pageFiles))
	for _, page := range pageFiles {
		t, err := template.New("layout.html").Funcs(funcs).ParseFS(assets, "templates/layout.html", "templates/"+page)
		if err != nil {
			return nil, err
		}
		out[page] = t
	}
	return out, nil
}

// pageData is the layout's view model. Page-specific fields live in Pages,
// keyed by name, to keep a single ExecuteTemplate call site.
type pageData struct {
	Title    string
	Username string
	CSRF     string
	Error    string
	Pages    map[string]any
}

// render writes a page. A template failure after headers are sent cannot be
// recovered, so rendering happens into a buffer first.
func (s *Server) render(w http.ResponseWriter, status int, page string, data pageData) {
	t, ok := s.templates[page]
	if !ok {
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout.html", data); err != nil {
		s.logger.Error("render failed", "page", page, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// staticHandler serves the embedded stylesheet.
func staticHandler() http.Handler {
	sub, err := fs.Sub(assets, "static")
	if err != nil {
		panic(err)
	}
	return http.StripPrefix("/admin/static/", http.FileServer(http.FS(sub)))
}
