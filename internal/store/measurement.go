package store

import (
	"encoding/json"
	"fmt"

	"github.com/tano/cqu-netprobe-gateway/internal/protocol"
)

// measurementConfigKey is the settings row holding the dispatched measurement
// parameters. Absent means "the protocol defaults", which is what every
// deployment starts with.
const measurementConfigKey = "measurement_config"

// MeasurementConfig returns the parameters probes are told to measure with.
//
// The bool reports whether an administrator has saved a set of their own; when
// it is false the protocol defaults are returned.
//
// A stored value that no longer parses or no longer validates yields the
// defaults and custom=false rather than an error. Only one writer can produce
// such a row — the settings page, which validates first — so it takes a manual
// edit of the database to get here, and refusing to dispatch a target list over
// it would stop every probe in the fleet for a reason none of them can act on.
func (s *Store) MeasurementConfig() (protocol.MeasurementConfig, bool, error) {
	raw, ok, err := s.Setting(measurementConfigKey)
	if err != nil {
		return protocol.MeasurementConfig{}, false, err
	}
	if !ok {
		return protocol.DefaultMeasurementConfig(), false, nil
	}

	var cfg protocol.MeasurementConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return protocol.DefaultMeasurementConfig(), false, nil
	}
	if err := cfg.Validate(); err != nil {
		return protocol.DefaultMeasurementConfig(), false, nil
	}
	return cfg, true, nil
}

// SetMeasurementConfig stores a parameter set. It validates before writing, so
// an unusable set cannot reach probes through the admin UI.
func (s *Store) SetMeasurementConfig(cfg protocol.MeasurementConfig) error {
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("store: measurement config is not valid: %w", err)
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("store: encode measurement config: %w", err)
	}
	return s.SetSetting(measurementConfigKey, string(encoded))
}

// ResetMeasurementConfig drops a stored set, returning the deployment to the
// protocol defaults.
func (s *Store) ResetMeasurementConfig() error {
	return s.DeleteSetting(measurementConfigKey)
}
