package webui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// fakePageFS stands in for a UI package's embedded templates so the shared
// plumbing can be tested without depending on admin's or portal's file set.
func fakePageFS() fstest.MapFS {
	return fstest.MapFS{
		"hello.html": &fstest.MapFile{Data: []byte(
			`{{define "content"}}<h1>{{.Title}}</h1>{{if .Error}}<p class="error">{{.Error}}</p>{{end}}{{end}}`)},
	}
}

func TestParseAndRender(t *testing.T) {
	tmpl, err := Parse(fakePageFS(), "hello.html")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	rec := httptest.NewRecorder()
	Render(rec, http.StatusOK, tmpl, "hello.html", PageData{Title: "hi", Error: "bad"})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	body := rec.Body.String()
	if !contains(body, "<h1>hi</h1>") || !contains(body, "bad") {
		t.Errorf("body = %s", body)
	}
	// The shared layout must wrap the page.
	if !contains(body, "<!DOCTYPE html>") {
		t.Errorf("page was not wrapped in the layout: %s", body)
	}
}

func TestRenderUnknownPageIs500(t *testing.T) {
	tmpl, err := Parse(fakePageFS(), "hello.html")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	rec := httptest.NewRecorder()
	Render(rec, http.StatusOK, tmpl, "missing.html", PageData{})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestRenderBuffersBeforeWriting(t *testing.T) {
	// A page whose template fails at execution time must not emit a partial
	// body with a 200 already committed.
	tmpl, err := Parse(fstest.MapFS{
		"boom.html": &fstest.MapFile{Data: []byte(`{{define "content"}}{{call .Pages.missing}}{{end}}`)},
	}, "boom.html")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	rec := httptest.NewRecorder()
	Render(rec, http.StatusOK, tmpl, "boom.html", PageData{Pages: map[string]any{}})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("a failed render wrote a body: %q", rec.Body.String())
	}
}

func TestStaticHandlerServesCSS(t *testing.T) {
	rec := httptest.NewRecorder()
	StaticHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/app.css", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !contains(rec.Body.String(), "body") {
		t.Error("served file does not look like the stylesheet")
	}
}

// app.js only rebuilds a <select> that sits inside a .select wrapper, and the
// stylesheet only hides the native control under that same class. A bare
// <select> added later would quietly render as the browser's own dropdown —
// exactly what the custom component exists to replace — so the rule is checked
// here rather than left to whoever reviews the next form.
//
// This walks the two UI packages' template directories, which is unusual for
// this package; it lives here because the wrapper is part of the shared
// component, and neither admin nor portal should own the rule.
func TestEverySelectIsWrappedForTheCustomDropdown(t *testing.T) {
	var files []string
	for _, dir := range []string{"../admin/templates", "../portal/templates"} {
		found, err := filepath.Glob(filepath.Join(dir, "*.html"))
		if err != nil {
			t.Fatalf("glob %s: %v", dir, err)
		}
		if len(found) == 0 {
			t.Fatalf("no templates found in %s", dir)
		}
		files = append(files, found...)
	}

	selects := 0
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		source := string(data)
		for at := 0; ; {
			i := strings.Index(source[at:], "<select")
			if i < 0 {
				break
			}
			i += at
			at = i + len("<select")
			selects++

			before := source[:i]
			if strings.LastIndex(before, `<div class="select"`) < strings.LastIndex(before, "</div>") {
				line := 1 + strings.Count(before, "\n")
				t.Errorf("%s:%d: <select> is not inside a .select wrapper, so it will render as a native dropdown",
					file, line)
			}
		}
	}
	if selects == 0 {
		t.Error("no <select> was found at all, so this test verified nothing")
	}
}

// The token page's copy buttons are inert without this file, and an embed
// pattern that missed it would fail at request time rather than at build time.
func TestStaticHandlerServesCopyScript(t *testing.T) {
	rec := httptest.NewRecorder()
	StaticHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/app.js", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !contains(rec.Body.String(), "data-copy") {
		t.Error("served file does not look like the copy handler")
	}
}

func TestFormatTimeRendersZeroAsDash(t *testing.T) {
	if got := FormatTime(time.Time{}); got != "—" {
		t.Errorf("FormatTime(zero) = %q, want —", got)
	}
	if got := FormatTime(time.Unix(0, 0)); got != "—" {
		t.Errorf("FormatTime(epoch) = %q, want —", got)
	}
	if got := FormatTime(time.Unix(1789490000, 0).UTC()); got == "—" {
		t.Error("FormatTime(real) must not render as —")
	}
}

func TestFormatAgeHandlesZero(t *testing.T) {
	now := time.Unix(1789490000, 0).UTC()
	if got := FormatAge(time.Time{}, now); got != "从未" {
		t.Errorf("FormatAge(zero) = %q, want 从未", got)
	}
	if got := FormatAge(now.Add(-5*time.Second), now); !contains(got, "5s") {
		t.Errorf("FormatAge(5s ago) = %q, want it to mention 5s", got)
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
