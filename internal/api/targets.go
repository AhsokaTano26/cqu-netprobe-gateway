package api

import (
	"encoding/json"
	"net/http"

	"github.com/tano/cqu-netprobe-gateway/internal/protocol"
)

// targetsVersion is the version of the target-list response. It is separate from
// the push protocol version: a probe that speaks push v1 can ignore this
// endpoint entirely, and this can evolve without touching the push contract.
const targetsVersion = 1

// targetsResponse is the body of GET /api/v1/targets.
//
// Only what a probe needs to act is included. Display names and descriptions are
// page-only and stay out, so a probe cannot come to depend on them.
type targetsResponse struct {
	Version int           `json:"version"`
	Targets []targetEntry `json:"targets"`
}

type targetEntry struct {
	TargetID   string   `json:"target_id"`
	Address    string   `json:"address"`
	ProbeTypes []string `json:"probe_types"`
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

	targets, err := s.store.DispatchTargets()
	if err != nil {
		s.logger.Error("failed to list dispatch targets", "error", err)
		s.reject(protocol.CodeInternalError, probe.ProbeID)
		writeError(w, http.StatusServiceUnavailable, protocol.CodeServiceUnavailable, msgUnavailable)
		return
	}

	entries := make([]targetEntry, 0, len(targets))
	for _, t := range targets {
		entries = append(entries, targetEntry{
			TargetID:   t.TargetID,
			Address:    t.Address,
			ProbeTypes: t.ProbeTypes,
		})
	}

	// The list is a few hundred bytes and changes only when an administrator
	// edits it, so let a probe revalidate cheaply instead of re-downloading.
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(targetsResponse{Version: targetsVersion, Targets: entries})

	s.logger.Debug("served target list", "probe_id", probe.ProbeID, "targets", len(entries))
}
