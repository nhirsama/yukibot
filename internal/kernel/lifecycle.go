package kernel

import (
	"context"
	"errors"
	"fmt"
)

// LifecycleState is the process lifecycle phase.
type LifecycleState string

const (
	StateNew      LifecycleState = "new"
	StateStarting LifecycleState = "starting"
	StateRunning  LifecycleState = "running"
	StateStopping LifecycleState = "stopping"
	StateStopped  LifecycleState = "stopped"
	StateFailed   LifecycleState = "failed"
)

// LifecycleManager starts features in order and stops them in reverse.
type LifecycleManager struct {
	features []Feature
	started  []Feature
	state    LifecycleState
	log      Logger
}

// NewLifecycleManager rejects duplicate feature names.
func NewLifecycleManager(features []Feature, logger Logger) (*LifecycleManager, error) {
	names := make([]string, len(features))
	seen := map[string]int{}
	for i, feature := range features {
		names[i] = feature.Name()
		seen[names[i]]++
	}
	for _, name := range names {
		if seen[name] > 1 {
			return nil, &DuplicateFeatureError{Message: "duplicate feature name: " + name}
		}
	}
	copied := append([]Feature(nil), features...)
	return &LifecycleManager{features: copied, state: StateNew, log: orLogger(logger)}, nil
}

// State returns the current phase.
func (m *LifecycleManager) State() LifecycleState { return m.state }

// StartedFeatures returns names started and not yet stopped, in start order.
func (m *LifecycleManager) StartedFeatures() []string {
	names := make([]string, len(m.started))
	for i, feature := range m.started {
		names[i] = feature.Name()
	}
	return names
}

// Start moves new → running. A second call while running does nothing.
func (m *LifecycleManager) Start(ctx context.Context) error {
	if m.state == StateRunning {
		return nil
	}
	if m.state != StateNew {
		return fmt.Errorf("cannot start lifecycle in state %s", m.state)
	}
	m.state = StateStarting
	for _, feature := range m.features {
		if err := feature.Start(ctx); err != nil {
			m.state = StateFailed
			rollback, stopErr := m.stopStarted(ctx)
			for _, failure := range rollback {
				m.log.Error("feature rollback failed", "feature", failure.Name, "error_type", typeName(failure.Err))
			}
			if stopErr != nil {
				return stopErr
			}
			if errors.Is(err, context.Canceled) {
				return err
			}
			return &LifecycleStartError{FeatureName: feature.Name(), Cause: err}
		}
		m.started = append(m.started, feature)
		m.log.Info("feature started", "feature", feature.Name())
	}
	m.state = StateRunning
	return nil
}

// Stop moves the lifecycle to stopped, or failed when a feature stop errors.
func (m *LifecycleManager) Stop(ctx context.Context) error {
	if m.state == StateStopped || m.state == StateStopping {
		return nil
	}
	if m.state == StateNew {
		m.state = StateStopped
		return nil
	}
	m.state = StateStopping
	failures, err := m.stopStarted(ctx)
	if err != nil {
		return err
	}
	if len(failures) == 0 {
		m.state = StateStopped
		return nil
	}
	m.state = StateFailed
	return &LifecycleStopError{Failures: failures}
}

func (m *LifecycleManager) stopStarted(ctx context.Context) ([]StopFailure, error) {
	var failures []StopFailure
	for len(m.started) > 0 {
		feature := m.started[len(m.started)-1]
		m.started = m.started[:len(m.started)-1]
		err := feature.Stop(ctx)
		if errors.Is(err, context.Canceled) {
			return failures, err
		}
		if err != nil {
			failures = append(failures, StopFailure{Name: feature.Name(), Err: err})
			continue
		}
		m.log.Info("feature stopped", "feature", feature.Name())
	}
	return failures, nil
}
