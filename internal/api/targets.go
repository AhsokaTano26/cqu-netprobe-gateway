package api

import (
	"encoding/json"
	"net/http"

	"github.com/tano/cqu-netprobe-gateway/internal/protocol"
	"github.com/tano/cqu-netprobe-gateway/internal/store"
)

// targetsVersion is the version of the target-list response. It is separate from
// the push protocol version: a probe that speaks push v1 can ignore this
// endpoint entirely, and this can evolve without touching the push contract.
const targetsVersion = 1

// targetsResponse is the body of GET /api/v1/targets (Protocol v1 §32.3).
//
// Only what a probe needs to act is included. Display names and descriptions are
// page-only and stay out, so a probe cannot come to depend on them.
type targetsResponse struct {
	Version int `json:"version"`
	// Config tells the probe how to measure, so the schedule and the per-type
	// parameters have one definition instead of one per probe build. It is
	// constant across targets: the gateway does not model a target that needs a
	// different timeout.
	Config protocol.MeasurementConfig `json:"config"`
	// ConfigID identifies what this response dispenses — the parameters and the
	// target list together. The probe echoes it on every push so the gateway can
	// tell it, with a 409, that anything here has moved on.
	//
	// It is derived from Targets below, so the ID and the list a probe is about
	// to act on cannot disagree.
	ConfigID string `json:"config_id"`
	// Targets is hashed for the ID as well as sent, which is why its element
	// type is the protocol's own: one value, used twice, with no mapping step
	// that could drift.
	Targets []protocol.DispatchTarget `json:"targets"`
}

// dispatchEntries converts the store's rows into the shape that is both sent to
// the probe and hashed for its config_id.
//
// One function, used by both endpoints, because the target list and the
// push-side staleness check have to agree exactly: if the list served here and
// the list the ID was derived from could differ, a probe could be handed a
// config_id that the push handler then rejects. Guaranteeing that by
// construction is cheaper than keeping two copies in step.
func dispatchEntries(targets []store.DispatchTarget) []protocol.DispatchTarget {
	entries := make([]protocol.DispatchTarget, 0, len(targets))
	for _, t := range targets {
		entries = append(entries, protocol.DispatchTarget{
			TargetID:   t.TargetID,
			Address:    t.Address,
			ProbeTypes: t.ProbeTypes,
		})
	}
	return entries
}

// handleTargets serves the target list to an authenticated probe.
//
// The probe authenticates with the same Bearer token it pushes with. The list is
// global rather than per-probe: the gateway does not model which probe measures
// which target, it only maintains what may be measured and by what method.
//
// This endpoint deliberately shares the push endpoint's authentication-failure
// throttle. Without it, a guessing attacker would simply move here, where the
// per-probe push bucket does not apply, and get unlimited attempts.
func (s *Server) handleTargets(w http.ResponseWriter, r *http.Request) {
	probe, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	if !probe.Enabled {
		s.reject(protocol.CodeProbeDisabled, probe.ProbeID)
		writeError(w, http.StatusForbidden, protocol.CodeProbeDisabled, msgDisabled)
		return
	}

	// Both reads happen per request rather than being cached: each is one
	// indexed lookup, and a cache would have to be invalidated by the admin UI,
	// which is the only writer. An edit therefore reaches probes on their next
	// refresh, with nothing to restart.
	targets, err := s.store.DispatchTargets()
	if err != nil {
		s.logger.Error("failed to list dispatch targets", "error", err)
		s.reject(protocol.CodeInternalError, probe.ProbeID)
		writeError(w, http.StatusServiceUnavailable, protocol.CodeServiceUnavailable, msgUnavailable)
		return
	}
	config, custom, err := s.store.MeasurementConfig()
	if err != nil {
		s.logger.Error("failed to read measurement config", "error", err)
		s.reject(protocol.CodeInternalError, probe.ProbeID)
		writeError(w, http.StatusServiceUnavailable, protocol.CodeServiceUnavailable, msgUnavailable)
		return
	}

	entries := dispatchEntries(targets)

	// The list is a few hundred bytes and changes only when an administrator
	// edits it, so let a probe revalidate cheaply instead of re-downloading.
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(targetsResponse{
		Version:  targetsVersion,
		Config:   config,
		ConfigID: protocol.ConfigID(config, entries),
		Targets:  entries,
	})

	s.logger.Debug("served target list", "probe_id", probe.ProbeID,
		"targets", len(entries), "custom_config", custom)
}
