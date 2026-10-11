// Package v3rulemodel contains provider observation types shared by alert-rule
// reads and configuration promotion without depending on either domain.
package v3rulemodel

import (
	"context"
	"time"
)

// Source reads rules from a trusted, environment-bound provider.
type Source interface {
	List(context.Context, string) ([]ObservedRule, error)
}

type ObservedRule struct {
	GroupIdentity      string // exact provider file + NUL + group name
	Group              string
	Name               string
	Expression         string
	State              string
	Duration           float64
	EvaluationInterval time.Duration // zero when provider omits it
	Labels             map[string]string
	Annotations        map[string]string
	LastEvaluation     time.Time
	EvaluationTime     float64
	// Health and LastError are Prometheus's rule evaluation state: ok, err
	// or unknown (D11 readback).
	Health    string
	LastError string
}

// RuleGroupBinding is reviewed process configuration. Provider labels may
// corroborate ownership but never establish it by themselves.
type RuleGroupBinding struct {
	EnvironmentID string
	ApplicationID string
	File          string
	Group         string
}
