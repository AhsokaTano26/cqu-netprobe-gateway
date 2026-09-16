// Package webui holds the template plumbing shared by the authenticated admin UI
// and the public portal. Keeping it here rather than inside either UI is what
// makes the authentication boundary a package boundary: internal/portal never
// has the session middleware in scope, so a public route cannot be accidentally
// authenticated, and an admin route cannot accidentally lose its guard.
package webui

import (
	"bytes"
	"embed"
	"html/template"
	"io/fs"
	"net/http"
	"time"
)

//go:embed templates/layout.html static/*
var assets embed.FS

// PageData is the layout's view model. The portal leaves Username and CSRF
// empty; a single structure keeps one render path for both UIs.
type PageData struct {
	Title    string
	Username string
	CSRF     string
	Error    string
	Pages    map[string]any
}

// Templates maps a page file name to a parsed template set with the layout applied.
type Templates map[string]*template.Template

var funcs = template.FuncMap{
	"formatTime": FormatTime,
	"formatAge":  FormatAge,
	"join":       join,
}

// FormatTime renders a timestamp for display. The zero time and the Unix epoch
// render as an em dash rather than 1970: "never" and "1970" are different facts,
// and a bogus epoch reads as real data on a dashboard.
func FormatTime(tm time.Time) string {
	if tm.IsZero() || tm.Unix() <= 0 {
		return "—"
	}
	return tm.UTC().Format("2006-01-02 15:04:05 UTC")
}

// FormatAge renders how long ago tm was, or 从未 if it never happened.
func FormatAge(tm time.Time, now time.Time) string {
	if tm.IsZero() || tm.Unix() <= 0 {
		return "从未"
	}
	d := now.Sub(tm).Round(time.Second)
	if d < 0 {
		d = 0
	}
	return d.String() + " 前"
}

func join(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}

// Parse builds one template set per page: the shared layout plus the calling
// package's page files. Each page file contains only a {{define "content"}}
// block, which the layout renders.
//
// pageFS is the caller's own embed.FS; its files live in a different package
// directory, which is why this cannot be one ParseFS call.
func Parse(pageFS fs.FS, pages ...string) (Templates, error) {
	out := make(Templates, len(pages))
	for _, page := range pages {
		t := template.New("layout.html").Funcs(funcs)

		var err error
		if t, err = t.ParseFS(assets, "templates/layout.html"); err != nil {
			return nil, err
		}
		if t, err = t.ParseFS(pageFS, page); err != nil {
			return nil, err
		}
		out[page] = t
	}
	return out, nil
}

// Render writes a page. Rendering happens into a buffer first, so a template
// failure produces a clean 500 instead of a half-written body under a status
// that has already been committed.
func Render(w http.ResponseWriter, status int, t Templates, page string, data PageData) {
	tmpl, ok := t[page]
	if !ok {
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "layout.html", data); err != nil {
		// The buffer absorbed the partial render, so nothing of the page has
		// reached w. Status only: a failure must not write a body it cannot
		// retract, and an empty 500 leaks neither partial HTML nor internals.
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// StaticHandler serves the shared stylesheet. Both UIs link /static/app.css, so
// this is registered once on the public listener rather than per UI.
func StaticHandler() http.Handler {
	sub, err := fs.Sub(assets, "static")
	if err != nil {
		panic(err)
	}
	return http.StripPrefix("/static/", http.FileServer(http.FS(sub)))
}
