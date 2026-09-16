package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/tano/cqu-netprobe-gateway/internal/protocol"
	"github.com/tano/cqu-netprobe-gateway/internal/store"
)

// docs/openapi.json is hand-written, so this file is what keeps it from
// drifting. Nothing here parses the prose: every assertion drives the real mux,
// compares the real response, or compares a Go constant with the spec, so a
// change to either side that the other does not follow fails the build.
//
// What it does NOT check: that every documented status is reachable. A few are
// not reachable from a test (push's 500 needs an encoder failure) and one is
// checked by the package that owns the listener (/metrics, in cmd/gateway).

const specPath = "../../docs/openapi.json"

type specDoc struct {
	OpenAPI    string                  `json:"openapi"`
	Info       map[string]any          `json:"info"`
	Paths      map[string]specPathItem `json:"paths"`
	Components specComponents          `json:"components"`
	Raw        map[string]any          `json:"-"`
}

type specPathItem struct {
	Get  *specOperation `json:"get"`
	Post *specOperation `json:"post"`
	Put  *specOperation `json:"put"`
}

type specOperation struct {
	Responses map[string]specResponse `json:"responses"`
}

type specResponse struct {
	Content map[string]struct {
		Example map[string]any `json:"example"`
	} `json:"content"`
}

type specComponents struct {
	Schemas map[string]struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
		Enum       []string                   `json:"enum"`
	} `json:"schemas"`
	SecuritySchemes map[string]struct {
		Type   string `json:"type"`
		Scheme string `json:"scheme"`
	} `json:"securitySchemes"`
}

func loadSpec(t *testing.T) specDoc {
	t.Helper()
	raw, err := os.ReadFile(filepath.FromSlash(specPath))
	if err != nil {
		t.Fatalf("read %s: %v", specPath, err)
	}
	var doc specDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("%s is not valid JSON: %v", specPath, err)
	}
	if err := json.Unmarshal(raw, &doc.Raw); err != nil {
		t.Fatalf("%s is not valid JSON: %v", specPath, err)
	}
	return doc
}

// operations returns the HTTP methods each path documents, so a path that gains
// an operation nobody implemented is noticed.
func (d specDoc) operations() map[string][]string {
	out := make(map[string][]string, len(d.Paths))
	for path, item := range d.Paths {
		var methods []string
		for name, op := range map[string]*specOperation{
			"GET": item.Get, "POST": item.Post, "PUT": item.Put,
		} {
			if op != nil {
				methods = append(methods, name)
			}
		}
		sort.Strings(methods)
		out[path] = methods
	}
	return out
}

func (d specDoc) documentedStatuses(t *testing.T, path, method string) map[int]bool {
	t.Helper()
	item, ok := d.Paths[path]
	if !ok {
		t.Fatalf("the spec documents no path %s", path)
	}
	var op *specOperation
	switch method {
	case http.MethodGet:
		op = item.Get
	case http.MethodPost:
		op = item.Post
	default:
		t.Fatalf("spec helper does not handle %s", method)
	}
	if op == nil {
		t.Fatalf("the spec documents no %s %s", method, path)
	}
	out := make(map[int]bool, len(op.Responses))
	for code := range op.Responses {
		n, err := strconv.Atoi(code)
		if err != nil {
			// A response key like "default" is not a status we can compare.
			continue
		}
		out[n] = true
	}
	return out
}

func TestSpecIsOpenAPI31(t *testing.T) {
	doc := loadSpec(t)
	if doc.OpenAPI != "3.1.0" {
		t.Errorf("openapi = %q, want 3.1.0", doc.OpenAPI)
	}
	if doc.Info["title"] == "" {
		t.Error("info.title is empty")
	}
	if title, _ := doc.Info["title"].(string); !strings.Contains(title, "NetProbe") {
		t.Errorf("info.title = %q, which does not name this service", title)
	}
}

// The endpoint list is the contract's size. Growing it is a protocol decision
// (§32 was one), so it should fail a test rather than slip in.
func TestSpecDocumentsExactlyTheImplementedPaths(t *testing.T) {
	doc := loadSpec(t)
	want := map[string][]string{
		"/api/v1/push":    {"POST"},
		"/api/v1/targets": {"GET"},
		"/metrics":        {"GET"},
	}
	got := doc.operations()
	if len(got) != len(want) {
		t.Fatalf("the spec documents %d paths, want %d: %v", len(got), len(want), got)
	}
	for path, methods := range want {
		if !reflect.DeepEqual(got[path], methods) {
			t.Errorf("%s documents methods %v, want %v", path, got[path], methods)
		}
	}
}

// TestSpecDocumentsEveryStatusTheHandlersReturn is the drift check that matters
// most: it runs each case against the real mux and fails if the status it gets
// back is not in the spec's response list for that operation.
func TestSpecDocumentsEveryStatusTheHandlersReturn(t *testing.T) {
	doc := loadSpec(t)

	targets := func(h *harness) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/targets", nil)
		req.Header.Set("Authorization", h.bearer())
		rec := httptest.NewRecorder()
		h.server.Routes().ServeHTTP(rec, req)
		return rec
	}

	// docMethod names the operation whose response list is consulted. It is the
	// request method except for the wrong-method cases: OpenAPI has no way to
	// document "405 for everything else", so a 405 is covered by the supported
	// operation's response list.
	cases := []struct {
		name      string
		method    string
		path      string
		docMethod string
		run       func(t *testing.T) int
	}{
		{"push accepted", http.MethodPost, "/api/v1/push", "", func(t *testing.T) int {
			h := newHarness(t)
			return h.do(t, http.MethodPost, goodBody, "application/json", h.bearer()).Code
		}},
		{"push without credentials", http.MethodPost, "/api/v1/push", "", func(t *testing.T) int {
			h := newHarness(t)
			return h.do(t, http.MethodPost, goodBody, "application/json", "").Code
		}},
		{"push from a disabled probe", http.MethodPost, "/api/v1/push", "", func(t *testing.T) int {
			h := newHarness(t)
			if err := h.store.SetProbeEnabled(h.probe.ProbeID, false); err != nil {
				t.Fatalf("SetProbeEnabled() error = %v", err)
			}
			return h.do(t, http.MethodPost, goodBody, "application/json", h.bearer()).Code
		}},
		{"push with the wrong method", http.MethodGet, "/api/v1/push", "POST", func(t *testing.T) int {
			h := newHarness(t)
			return h.do(t, http.MethodGet, "", "", h.bearer()).Code
		}},
		{"push without a content type", http.MethodPost, "/api/v1/push", "", func(t *testing.T) int {
			h := newHarness(t)
			return h.do(t, http.MethodPost, goodBody, "", h.bearer()).Code
		}},
		{"push with an oversized body", http.MethodPost, "/api/v1/push", "", func(t *testing.T) int {
			h := newHarness(t)
			big := `{"version":1,"probe_version":"` + strings.Repeat("x", MaxBodyBytes) + `","results":{}}`
			return h.do(t, http.MethodPost, big, "application/json", h.bearer()).Code
		}},
		{"push with malformed json", http.MethodPost, "/api/v1/push", "", func(t *testing.T) int {
			h := newHarness(t)
			return h.do(t, http.MethodPost, `{"version":1,`, "application/json", h.bearer()).Code
		}},
		{"push with an unsupported version", http.MethodPost, "/api/v1/push", "", func(t *testing.T) int {
			h := newHarness(t)
			return h.do(t, http.MethodPost,
				`{"version":2,"probe_version":"0.1.0","results":{"aliyun_dns":{"icmp":{"success":false,"sent":1,"received":0,"loss_ratio":1.0}}}}`,
				"application/json", h.bearer()).Code
		}},
		{"push with a measurement that breaks a rule", http.MethodPost, "/api/v1/push", "", func(t *testing.T) int {
			h := newHarness(t)
			return h.do(t, http.MethodPost,
				`{"version":1,"probe_version":"0.1.0","results":{"aliyun_dns":{"icmp":{"success":false,"sent":0,"received":0,"loss_ratio":1.0}}}}`,
				"application/json", h.bearer()).Code
		}},
		{"push rate limited", http.MethodPost, "/api/v1/push", "", func(t *testing.T) int {
			h := newHarness(t)
			var code int
			for i := 0; i < 10; i++ {
				code = h.do(t, http.MethodPost, goodBody, "application/json", h.bearer()).Code
				if code == http.StatusTooManyRequests {
					break
				}
			}
			return code
		}},
		{"push unavailable", http.MethodPost, "/api/v1/push", "", func(t *testing.T) int {
			h := newHarness(t)
			closedStore(t, h)
			return h.do(t, http.MethodPost, goodBody, "application/json", h.bearer()).Code
		}},
		{"targets listed", http.MethodGet, "/api/v1/targets", "", func(t *testing.T) int {
			return targets(newHarness(t)).Code
		}},
		{"targets without credentials", http.MethodGet, "/api/v1/targets", "", func(t *testing.T) int {
			h := newHarness(t)
			req := httptest.NewRequest(http.MethodGet, "/api/v1/targets", nil)
			rec := httptest.NewRecorder()
			h.server.Routes().ServeHTTP(rec, req)
			return rec.Code
		}},
		{"targets from a disabled probe", http.MethodGet, "/api/v1/targets", "", func(t *testing.T) int {
			h := newHarness(t)
			if err := h.store.SetProbeEnabled(h.probe.ProbeID, false); err != nil {
				t.Fatalf("SetProbeEnabled() error = %v", err)
			}
			return targets(h).Code
		}},
		{"targets with the wrong method", http.MethodPost, "/api/v1/targets", "GET", func(t *testing.T) int {
			h := newHarness(t)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/targets", nil)
			req.Header.Set("Authorization", h.bearer())
			rec := httptest.NewRecorder()
			h.server.Routes().ServeHTTP(rec, req)
			return rec.Code
		}},
		{"targets rate limited after token guessing", http.MethodGet, "/api/v1/targets", "", func(t *testing.T) int {
			h := newHarness(t)
			var code int
			req := httptest.NewRequest(http.MethodGet, "/api/v1/targets", nil)
			for i := 0; i < 40; i++ {
				req.Header.Set("Authorization", "Bearer cqu_probe_wrong")
				rec := httptest.NewRecorder()
				h.server.Routes().ServeHTTP(rec, req)
				code = rec.Code
				if code == http.StatusTooManyRequests {
					break
				}
			}
			return code
		}},
		{"targets unavailable", http.MethodGet, "/api/v1/targets", "", func(t *testing.T) int {
			h := newHarness(t)
			closedStore(t, h)
			return targets(h).Code
		}},
	}

	seen := map[string]map[int]bool{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.run(t)
			docMethod := tc.docMethod
			if docMethod == "" {
				docMethod = tc.method
			}
			if !doc.documentedStatuses(t, tc.path, docMethod)[got] {
				t.Errorf("%s %s returned %d, which the spec does not document for %s %s",
					tc.method, tc.path, got, docMethod, tc.path)
			}
			if seen[tc.path] == nil {
				seen[tc.path] = map[int]bool{}
			}
			seen[tc.path][got] = true
		})
	}

	// The reverse direction, for the statuses that mean something specific: a
	// spec that stopped documenting 413 or 403 would otherwise pass by having no
	// case produce it.
	for path, required := range map[string][]int{
		"/api/v1/push": {
			http.StatusNoContent, http.StatusBadRequest, http.StatusUnauthorized,
			http.StatusForbidden, http.StatusMethodNotAllowed, http.StatusRequestEntityTooLarge,
			http.StatusUnsupportedMediaType, http.StatusTooManyRequests, http.StatusServiceUnavailable,
		},
		"/api/v1/targets": {
			http.StatusOK, http.StatusUnauthorized, http.StatusForbidden,
			http.StatusMethodNotAllowed, http.StatusTooManyRequests, http.StatusServiceUnavailable,
		},
	} {
		item := doc.Paths[path]
		var op *specOperation
		switch path {
		case "/api/v1/push":
			op = item.Post
		default:
			op = item.Get
		}
		for _, status := range required {
			if _, ok := op.Responses[strconv.Itoa(status)]; !ok {
				t.Errorf("%s no longer documents status %d", path, status)
			}
		}
	}
}

// closedStore breaks the store underneath a running server so the failure path
// (a database that cannot answer) is exercised rather than assumed.
func closedStore(t *testing.T, h *harness) {
	t.Helper()
	if err := h.store.Close(); err != nil {
		t.Fatalf("store.Close() error = %v", err)
	}
}

// The error enum is duplicated between Go and the spec on purpose — the spec has
// to be readable without the source — so this is where the two are reconciled.
func TestSpecErrorEnumMatchesProtocolConstants(t *testing.T) {
	doc := loadSpec(t)
	enum := doc.Components.Schemas["ErrorCode"].Enum
	if len(enum) == 0 {
		t.Fatal("the spec defines no ErrorCode enum")
	}

	want := []string{
		protocol.CodeInvalidRequest,
		protocol.CodeInvalidJSON,
		protocol.CodeInvalidPayload,
		protocol.CodeUnsupportedVersion,
		protocol.CodeInvalidTarget,
		protocol.CodeInvalidProbeType,
		protocol.CodeUnauthorized,
		protocol.CodeProbeDisabled,
		protocol.CodeRateLimited,
		protocol.CodeConfigStale,
		protocol.CodeInternalError,
		protocol.CodeServiceUnavailable,
	}
	sort.Strings(want)
	sort.Strings(enum)
	if !reflect.DeepEqual(enum, want) {
		t.Errorf("ErrorCode enum = %v, want %v", enum, want)
	}
}

func TestSpecDeclaresBearerAuth(t *testing.T) {
	doc := loadSpec(t)
	scheme, ok := doc.Components.SecuritySchemes["bearerAuth"]
	if !ok {
		t.Fatal("the spec declares no bearerAuth scheme")
	}
	if scheme.Type != "http" || scheme.Scheme != "bearer" {
		t.Errorf("bearerAuth = %s/%s, want http/bearer", scheme.Type, scheme.Scheme)
	}
}

// The documented numbers are the numbers the gateway sends. Both sides are
// turned into the same map shape first, so the comparison is on values and not
// on struct definitions.
func TestSpecConfigMatchesTheDispatchedConfig(t *testing.T) {
	doc := loadSpec(t)

	documented := doc.Paths["/api/v1/targets"].Get.Responses["200"].
		Content["application/json"].Example["config"]
	if documented == nil {
		t.Fatal("the 200 response carries no example config to compare against")
	}
	// The example's config_id is a concrete UUID, so it has to be the ID of
	// everything shown beside it — the config and the target list both. A reader
	// who copies the example and pushes it back would otherwise get a 409 for
	// following the documentation. Deriving the expectation from the example
	// itself, rather than from a Go constant, catches a stale ID whichever of
	// the three moved.
	example := doc.Paths["/api/v1/targets"].Get.Responses["200"].
		Content["application/json"].Example
	exampleID, _ := example["config_id"].(string)

	var exConfig protocol.MeasurementConfig
	raw, err := json.Marshal(example["config"])
	if err != nil {
		t.Fatalf("marshal the example config: %v", err)
	}
	if err := json.Unmarshal(raw, &exConfig); err != nil {
		t.Fatalf("the example config is not a valid config object: %v", err)
	}
	var exTargets []protocol.DispatchTarget
	if raw, err = json.Marshal(example["targets"]); err != nil {
		t.Fatalf("marshal the example target list: %v", err)
	}
	if err := json.Unmarshal(raw, &exTargets); err != nil {
		t.Fatalf("the example target list is not valid: %v", err)
	}

	if want := protocol.ConfigID(exConfig, exTargets); exampleID != want {
		t.Errorf("the example config_id is %q, but the config and target list shown beside it hash to %q",
			exampleID, want)
	}

	h := newHarness(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/targets", nil)
	req.Header.Set("Authorization", h.bearer())
	rec := httptest.NewRecorder()
	h.server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("targets status = %d, want 200", rec.Code)
	}
	var body struct {
		Config map[string]any `json:"config"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}

	if !reflect.DeepEqual(documented, body.Config) {
		t.Errorf("the spec documents config %v but the gateway sends %v", documented, body.Config)
	}

	encoded, err := json.Marshal(protocol.DefaultMeasurementConfig())
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	var fromConstants map[string]any
	if err := json.Unmarshal(encoded, &fromConstants); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	if !reflect.DeepEqual(fromConstants, body.Config) {
		t.Errorf("protocol.DefaultMeasurementConfig() = %v but the gateway sends %v",
			fromConstants, body.Config)
	}
}

// A documented shape that is not the emitted shape is the most expensive kind of
// spec bug: client code is generated from it and fails in the field. Both the
// top level and the nested config objects are compared key by key.
func TestSpecSchemasMatchTheEmittedResponse(t *testing.T) {
	doc := loadSpec(t)

	h := newHarness(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/targets", nil)
	req.Header.Set("Authorization", h.bearer())
	rec := httptest.NewRecorder()
	h.server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("targets status = %d, want 200", rec.Code)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}

	compareKeys(t, "TargetList", doc, body)

	config, ok := body["config"].(map[string]any)
	if !ok {
		t.Fatalf("config is not an object: %v", body["config"])
	}
	compareKeys(t, "MeasurementConfig", doc, config)
	for _, group := range []struct{ key, schema string }{
		{"icmp", "ICMPConfig"}, {"http", "HTTPConfig"}, {"dns", "DNSConfig"},
	} {
		nested, ok := config[group.key].(map[string]any)
		if !ok {
			t.Fatalf("config.%s is not an object", group.key)
		}
		compareKeys(t, group.schema, doc, nested)
	}

	targets, ok := body["targets"].([]any)
	if !ok {
		t.Fatalf("targets is not an array: %v", body["targets"])
	}
	if len(targets) == 0 {
		t.Fatal("the harness seeded no dispatchable target, so Target is unverified")
	}
	compareKeys(t, "Target", doc, targets[0].(map[string]any))
}

// compareKeys asserts that a schema documents exactly the properties an actual
// object carries, and that everything it marks required is present.
func compareKeys(t *testing.T, schemaName string, doc specDoc, obj map[string]any) {
	t.Helper()
	schema, ok := doc.Components.Schemas[schemaName]
	if !ok {
		t.Fatalf("the spec defines no schema %s", schemaName)
	}

	var documented []string
	for name := range schema.Properties {
		documented = append(documented, name)
	}
	var emitted []string
	for name := range obj {
		emitted = append(emitted, name)
	}
	sort.Strings(documented)
	sort.Strings(emitted)

	if !reflect.DeepEqual(documented, emitted) {
		t.Errorf("%s: the spec documents %v but the gateway emits %v",
			schemaName, documented, emitted)
	}
	for _, name := range schema.Required {
		if _, ok := obj[name]; !ok {
			t.Errorf("%s: the spec requires %q, which the gateway did not emit", schemaName, name)
		}
	}
}

// The push request schema is checked by behaviour rather than by reflection:
// the decoder reads the wire format through unexported structs, so the only
// honest statement of "these keys are the format" is that the gateway accepts a
// body built from them.
func TestSpecPushRequestSchemaIsAccepted(t *testing.T) {
	doc := loadSpec(t)
	schema := doc.Components.Schemas["PushRequest"]

	// The harness is built first because config_id below has to carry the ID it
	// is actually dispensing — the default config and the seeded target list,
	// since it saves neither — so this also proves the happy path accepts one.
	h := newHarness(t)

	// Every documented property, at once, in the documented nesting.
	body := map[string]any{
		"version":       1,
		"timestamp":     1789490000,
		"probe_version": "0.1.0",
		"config_id":     h.configID(t),
		"results": map[string]any{
			"aliyun_dns": map[string]any{
				"icmp": map[string]any{
					"success": true, "sent": 5, "received": 5, "loss_ratio": 0.0,
					"min_rtt_ms": 10.2, "avg_rtt_ms": 12.3, "max_rtt_ms": 15.8, "jitter_ms": 1.4,
				},
			},
			"campus_dns": map[string]any{
				"dns": map[string]any{"success": true, "duration_ms": 8.4},
			},
			"cqu_mirror": map[string]any{
				"http": map[string]any{"success": true, "status_code": 200, "duration_ms": 51.2},
			},
		},
	}
	// A property the spec documents but the body does not would make the test
	// pass for the wrong reason, so the key sets are compared first.
	var keys []string
	for k := range schema.Properties {
		keys = append(keys, k)
	}
	var built []string
	for k := range body {
		built = append(built, k)
	}
	sort.Strings(keys)
	sort.Strings(built)
	if !reflect.DeepEqual(keys, built) {
		t.Fatalf("the spec documents PushRequest properties %v but this test builds %v", keys, built)
	}
	for _, name := range schema.Required {
		if _, ok := body[name]; !ok {
			t.Fatalf("the spec requires %q, which this test does not send", name)
		}
	}

	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	rec := h.do(t, http.MethodPost, string(encoded), "application/json", h.bearer())
	if rec.Code != http.StatusNoContent {
		t.Fatalf("a body built from the documented schema was rejected: %d %s",
			rec.Code, rec.Body.String())
	}
}

// Every $ref must resolve. A dangling one makes the file unusable in the tools
// it exists for, and JSON has no linker to catch it.
func TestSpecRefsResolve(t *testing.T) {
	doc := loadSpec(t)
	var walk func(node any, where string)
	walk = func(node any, where string) {
		switch v := node.(type) {
		case map[string]any:
			if ref, ok := v["$ref"].(string); ok {
				const prefix = "#/"
				if !strings.HasPrefix(ref, prefix) {
					t.Errorf("%s: unsupported $ref %q", where, ref)
					return
				}
				var target any = doc.Raw
				for _, part := range strings.Split(strings.TrimPrefix(ref, prefix), "/") {
					obj, ok := target.(map[string]any)
					if !ok {
						t.Errorf("%s: $ref %q does not resolve", where, ref)
						return
					}
					target, ok = obj[part]
					if !ok {
						t.Errorf("%s: $ref %q does not resolve", where, ref)
						return
					}
				}
			}
			for key, child := range v {
				walk(child, where+"."+key)
			}
		case []any:
			for i, child := range v {
				walk(child, where+"["+strconv.Itoa(i)+"]")
			}
		}
	}
	walk(doc.Raw, "openapi")
}

// The Target schema promises a target_id shape, a non-empty address and at
// least one probe type. Those are promises about the list a probe receives, not
// about the database — the ID pattern is enforced by the admin form, and the
// store deliberately accepts whatever it is given so that DispatchTargets can
// filter unmeasurable rows instead of failing writes. So the assertion belongs
// on the response, which is the only place a probe can observe any of it.
func TestSpecConstraintsHoldForEveryDispatchedTarget(t *testing.T) {
	doc := loadSpec(t)

	var idSchema struct {
		Pattern string `json:"pattern"`
	}
	if err := json.Unmarshal(doc.Components.Schemas["Target"].Properties["target_id"], &idSchema); err != nil {
		t.Fatalf("target_id schema is not an object: %v", err)
	}
	if idSchema.Pattern == "" {
		t.Fatal("the Target schema documents no target_id pattern")
	}
	pattern, err := regexp.Compile(idSchema.Pattern)
	if err != nil {
		t.Fatalf("the documented target_id pattern does not compile: %v", err)
	}

	h := newHarness(t)
	// A target with a blank address and one that is disabled must not appear,
	// which is the other half of §32.5's content rules.
	if err := h.store.CreateTarget(&store.Target{
		TargetID: "blank_addr", Address: "   ", Enabled: true, ProbeTypes: []string{"icmp"},
	}); err != nil {
		t.Fatalf("CreateTarget(blank_addr) error = %v", err)
	}
	if err := h.store.CreateTarget(&store.Target{
		TargetID: "off", Address: "192.0.2.9", Enabled: false, ProbeTypes: []string{"icmp"},
	}); err != nil {
		t.Fatalf("CreateTarget(off) error = %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/targets", nil)
	req.Header.Set("Authorization", h.bearer())
	rec := httptest.NewRecorder()
	h.server.Routes().ServeHTTP(rec, req)

	var body struct {
		Targets []struct {
			TargetID   string   `json:"target_id"`
			Address    string   `json:"address"`
			ProbeTypes []string `json:"probe_types"`
		} `json:"targets"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if len(body.Targets) == 0 {
		t.Fatal("no target was dispatched, so nothing was verified")
	}

	for _, target := range body.Targets {
		if !pattern.MatchString(target.TargetID) {
			t.Errorf("dispatched target_id %q does not match the documented pattern %s",
				target.TargetID, idSchema.Pattern)
		}
		if strings.TrimSpace(target.Address) == "" {
			t.Errorf("target %s was dispatched with a blank address", target.TargetID)
		}
		if len(target.ProbeTypes) == 0 {
			t.Errorf("target %s was dispatched with no probe types", target.TargetID)
		}
		for _, pt := range target.ProbeTypes {
			if pt != "icmp" && pt != "dns" && pt != "http" {
				t.Errorf("target %s was dispatched with probe type %q, which the spec's enum excludes",
					target.TargetID, pt)
			}
		}
	}
}
