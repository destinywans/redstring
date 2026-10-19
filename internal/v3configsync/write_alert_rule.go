package v3configsync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"strings"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/store"
	"github.com/destinywans/redstring/internal/v3rulemodel"
	"github.com/jackc/pgx/v5"
)

// AlertRuleResolver only accepts the process-owned Prometheus source and exact
// file/group bindings used by the observed alert-rule API. Provider labels can
// corroborate ownership, but cannot assign a rule to an application.
type AlertRuleResolver struct {
	source   v3rulemodel.Source
	bindings map[string]string
	// labels maps environment + application ID to the value of its rules'
	// "application" label (D32): the bound key, else the application ID.
	labels map[string]string
}

type alertRuleCandidate struct {
	ApplicationID string
	Snapshot      readAlertSnapshot
	// Refused says why the rule cannot be promoted. The caller reports it
	// only after authorizing the rule's application, so its reason does not
	// describe a rule the caller may not see.
	Refused error
}

var (
	// ~ is only for the =~ and !~ matchers of a quoted value below.
	safeAlertExpressionRE = regexp.MustCompile(`^[A-Za-z0-9_:.{}\[\](),+*/%<>=!~^ -]+$`)
	// A quoted label matcher value an expression may carry (PA-97).
	safeMatcherValueRE = regexp.MustCompile(`^[A-Za-z0-9._:-]{0,128}$`)
	matcherBeforeRE    = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)\s*(=|!=|=~|!~)\s*$`)
	// Label names whose values are credentials by name are never carried.
	secretLabelNameRE = regexp.MustCompile(`(?i)token|secret|passw|credential|api_?key|auth`)
)

// safeAlertExpression accepts an expression of the characters PromQL
// selectors and operators need, whose only string literals are label
// matcher values inside braces made of [A-Za-z0-9._:-], such as
// {application="checkout-api"} (PA-97), on labels not named like a
// credential. Any other literal, an escape or a regex metacharacter can
// carry text that should not leave the environment.
func safeAlertExpression(expr string) bool {
	var stripped strings.Builder
	depth := 0
	for i := 0; i < len(expr); i++ {
		c := expr[i]
		switch c {
		case '{':
			depth++
		case '}':
			depth--
		case '"', '\'', '`':
			end := strings.IndexByte(expr[i+1:], c)
			matcher := matcherBeforeRE.FindStringSubmatch(expr[:i])
			if depth != 1 || end < 0 || matcher == nil || secretLabelNameRE.MatchString(matcher[1]) ||
				!safeMatcherValueRE.MatchString(expr[i+1:i+1+end]) {
				return false
			}
			stripped.WriteByte('v')
			i += end + 1
			continue
		}
		if depth < 0 || depth > 1 {
			return false
		}
		stripped.WriteByte(c)
	}
	return depth == 0 && safeAlertExpressionRE.MatchString(stripped.String())
}

func NewAlertRuleResolver(source v3rulemodel.Source, bindings []v3rulemodel.RuleGroupBinding) (*AlertRuleResolver, error) {
	if source == nil || len(bindings) == 0 {
		return nil, errUnavailable
	}
	index := make(map[string]string, len(bindings))
	labels := make(map[string]string, len(bindings))
	for _, b := range bindings {
		if b.EnvironmentID == "" || b.EnvironmentID == "all" ||
			b.ApplicationID == "" || b.File == "" || b.Group == "" ||
			strings.ContainsRune(b.EnvironmentID, 0) || strings.ContainsRune(b.ApplicationID, 0) ||
			strings.ContainsRune(b.File, 0) || strings.ContainsRune(b.Group, 0) {
			return nil, errInvalid
		}
		key := b.EnvironmentID + "\x00" + b.File + "\x00" + b.Group
		if _, duplicate := index[key]; duplicate {
			return nil, errInvalid
		}
		index[key] = b.ApplicationID
		label := b.ApplicationLabel
		if label == "" {
			label = b.ApplicationID
		}
		appKey := b.EnvironmentID + "\x00" + b.ApplicationID
		if prior, ok := labels[appKey]; ok && prior != label {
			return nil, errInvalid
		}
		labels[appKey] = label
	}
	return &AlertRuleResolver{source: source, bindings: index, labels: labels}, nil
}

// Resolve captures an observed, bound configuration rather than a runtime
// firing state. Its digest-derived version identifies exactly the captured
// source definition. A rule is matched and identified as the observed rule
// list does (D32): its "application" label is the binding's label value and
// its prom- ID derives from the application ID. Free-form annotations are
// left out of the snapshot; quoted literals other than safe label matcher
// values and unrecognized labels are refused, because their contents may
// include secrets.
func (r *AlertRuleResolver) Resolve(ctx context.Context, environment, observedID string) (alertRuleCandidate, error) {
	if r == nil || r.source == nil || environment == "" || environment == "prod" || environment == "all" ||
		len(observedID) != len("prom-")+24 || !strings.HasPrefix(observedID, "prom-") {
		return alertRuleCandidate{}, errInvalid
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(observedID, "prom-")); err != nil {
		return alertRuleCandidate{}, errInvalid
	}
	rules, err := r.source.List(ctx, environment)
	if err != nil {
		return alertRuleCandidate{}, errUnavailable
	}
	if len(rules) > 500 {
		return alertRuleCandidate{}, errResultLimit
	}
	var result alertRuleCandidate
	found := false
	for _, rule := range rules {
		app, bound := r.bindings[environment+"\x00"+rule.GroupIdentity]
		label := r.labels[environment+"\x00"+app]
		if !bound || rule.Labels["environment"] != environment || rule.Labels["application"] != label {
			continue
		}
		identity := sha256.Sum256([]byte(environment + "\x00" + app + "\x00" + rule.GroupIdentity + "\x00" + rule.Name))
		id := "prom-" + hex.EncodeToString(identity[:12])
		if id != observedID {
			continue
		}
		if found {
			return alertRuleCandidate{}, errUntrusted
		}
		found = true
		parts := strings.Split(rule.GroupIdentity, "\x00")
		if len(parts) != 2 || parts[0] == "" || parts[1] != rule.Group {
			return alertRuleCandidate{}, errUntrusted
		}
		snapshot, err := alertSnapshot(rule, environment, app, label, identity)
		var refused *configRefusal
		if errors.As(err, &refused) {
			result = alertRuleCandidate{ApplicationID: app, Refused: err}
			continue
		}
		if err != nil {
			return alertRuleCandidate{}, err
		}
		result = alertRuleCandidate{ApplicationID: app, Snapshot: snapshot}
	}
	if !found {
		return alertRuleCandidate{}, store.ErrNotFound
	}
	return result, nil
}

// alertSnapshot is the promoted definition of a bound rule whose
// "application" label is label. Its annotations (summary, description) are
// free text and are not promoted (PA-97).
func alertSnapshot(rule v3rulemodel.ObservedRule, environment, app, label string, identity [32]byte) (readAlertSnapshot, error) {
	if !boundedText(rule.Name, 200) || !boundedText(rule.Expression, 4096) || !boundedText(rule.Group, 200) ||
		rule.Duration < 0 || rule.Duration > 86400 || math.Trunc(rule.Duration) != rule.Duration {
		return readAlertSnapshot{}, errAlertRuleShape
	}
	if !safeAlertExpression(rule.Expression) {
		return readAlertSnapshot{}, errAlertRuleExpression
	}
	if len(rule.Labels) < 2 || len(rule.Labels) > 3 ||
		rule.Labels["environment"] != environment || rule.Labels["application"] != label {
		return readAlertSnapshot{}, errAlertRuleLabels
	}
	labels := make(map[string]string, len(rule.Labels))
	for key, value := range rule.Labels {
		if !exportableAlertLabel(key, value, environment, label) {
			return readAlertSnapshot{}, errAlertRuleLabels
		}
		labels[key] = value
	}
	snapshot := readAlertSnapshot{Name: rule.Name, Expression: rule.Expression,
		DurationSeconds: int(rule.Duration), Labels: labels, Annotations: map[string]string{},
		SourceIdentityDigest: hex.EncodeToString(identity[:])}
	if !boundedMap(snapshot.Labels) || !boundedMap(snapshot.Annotations) {
		return readAlertSnapshot{}, errInvalidManifest
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return readAlertSnapshot{}, errInvalidManifest
	}
	version := sha256.Sum256(raw)
	snapshot.Version = "sha256:" + hex.EncodeToString(version[:])
	if !sanitizedAlertSnapshot(environment, label, "prom-"+hex.EncodeToString(identity[:12]), snapshot) {
		return readAlertSnapshot{}, errInvalidManifest
	}
	return snapshot, nil
}

// exportableAlertLabel is a label a promoted rule may carry: its scope
// labels, and a severity of the product's P1-P4 or Prometheus's usual
// info/warning/critical. label is the expected "application" value; empty
// accepts any object key (an import cannot know the source's D32 label).
func exportableAlertLabel(key, value, environment, label string) bool {
	switch key {
	case "environment":
		return value == environment && objectKeyRE.MatchString(value)
	case "application":
		return (label == "" || value == label) && objectKeyRE.MatchString(value)
	case "severity":
		return oneOf(value, "P1", "P2", "P3", "P4", "info", "warning", "critical")
	}
	return false
}

// Re-run source sanitization on import. A trusted signature proves provenance,
// but does not by itself prove that free text was safe to promote. label is
// the expected "application" label value; on import it is empty, since the
// source instance's D32 label is not known here, and any object key passes.
func sanitizedAlertSnapshot(environment, label, sourceID string, snapshot readAlertSnapshot) bool {
	if !boundedText(snapshot.Name, 200) || !boundedText(snapshot.Expression, 4096) ||
		!safeAlertExpression(snapshot.Expression) || len(snapshot.Annotations) != 0 ||
		len(snapshot.Labels) < 2 || len(snapshot.Labels) > 3 ||
		snapshot.Labels["environment"] != environment || snapshot.Labels["application"] == "" ||
		!hexDigestRE.MatchString(snapshot.SourceIdentityDigest) ||
		sourceID != "prom-"+snapshot.SourceIdentityDigest[:24] ||
		snapshot.DurationSeconds < 0 || snapshot.DurationSeconds > 86400 {
		return false
	}
	for key, value := range snapshot.Labels {
		if !exportableAlertLabel(key, value, environment, label) {
			return false
		}
	}
	withoutVersion := snapshot
	withoutVersion.Version = ""
	raw, err := json.Marshal(withoutVersion)
	if err != nil {
		return false
	}
	version := sha256.Sum256(raw)
	return snapshot.Version == "sha256:"+hex.EncodeToString(version[:])
}

// saveAlertRuleTarget persists a reviewed production definition. The record is
// deliberately not applied to Prometheus; activation_state remains not_applied.
// The caller owns the surrounding transaction, import ledger and success audit.
func (s *Service) saveAlertRuleTarget(ctx context.Context, tx pgx.Tx, _ core.User, m readManifestV2) (string, error) {
	if tx == nil || m.Kind != "alert_rule" || m.AlertRule == nil {
		return "", errInvalidManifest
	}
	if _, err := m.validate(m.PromotionID); err != nil {
		return "", errInvalidManifest
	}
	if !sanitizedAlertSnapshot(m.SourceEnvironment, "", m.SourceID, *m.AlertRule) {
		return "", errInvalidManifest
	}
	var activeApp string
	err := tx.QueryRow(ctx, `SELECT a.id FROM applications a
 JOIN v3_application_environments ve ON ve.application_id=a.id AND ve.environment_id='prod'
 JOIN application_environments ae ON ae.application_id=a.id AND ae.key='prod' AND NOT ae.archived
 WHERE a.id=$1 AND NOT a.archived FOR KEY SHARE OF a,ve,ae`, m.ApplicationID).Scan(&activeApp)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", store.ErrConflict
	}
	if err != nil {
		return "", err
	}
	targetID := "CAR-" + m.PromotionID
	labels, err := json.Marshal(m.AlertRule.Labels)
	if err != nil {
		return "", errInvalidManifest
	}
	annotations, err := json.Marshal(m.AlertRule.Annotations)
	if err != nil {
		return "", errInvalidManifest
	}
	var existingID string
	err = tx.QueryRow(ctx, `SELECT id FROM v3_config_alert_rules WHERE id=$1 FOR SHARE`, targetID).Scan(&existingID)
	if err == nil {
		var recorded bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM v3_config_imports
 WHERE kind='alert_rule' AND promotion_id=$1 AND target_id=$2)`, m.PromotionID, targetID).Scan(&recorded); err != nil {
			return "", err
		}
		if !recorded {
			return "", store.ErrConflict
		}
		var exact bool
		err = tx.QueryRow(ctx, `SELECT application_id=$2 AND environment_id='prod' AND name=$3
 AND source_version=$4 AND expression=$5 AND duration_seconds=$6
 AND labels=$7::jsonb AND annotations=$8::jsonb AND source_identity_digest=$9
 AND source_digest=$10 AND source_instance=$11 AND source_environment=$12
 AND source_id=$13 AND promotion_id=$14 AND activation_state='not_applied'
 FROM v3_config_alert_rules WHERE id=$1 FOR SHARE`, targetID, m.ApplicationID,
			m.AlertRule.Name, m.SourceVersion, m.AlertRule.Expression, m.AlertRule.DurationSeconds,
			labels, annotations, m.AlertRule.SourceIdentityDigest, m.SourceDigest,
			m.SourceInstance, m.SourceEnvironment, m.SourceID, m.PromotionID).Scan(&exact)
		if err != nil {
			return "", err
		}
		if !exact {
			return "", store.ErrConflict
		}
		return targetID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	_, err = tx.Exec(ctx, `INSERT INTO v3_config_alert_rules
 (id,application_id,environment_id,name,source_version,expression,duration_seconds,
 labels,annotations,source_identity_digest,source_digest,source_instance,
 source_environment,source_id,promotion_id,activation_state)
 VALUES($1,$2,'prod',$3,$4,$5,$6,$7::jsonb,$8::jsonb,$9,$10,$11,$12,$13,$14,'not_applied')`,
		targetID, m.ApplicationID, m.AlertRule.Name, m.SourceVersion, m.AlertRule.Expression,
		m.AlertRule.DurationSeconds, labels, annotations, m.AlertRule.SourceIdentityDigest, m.SourceDigest,
		m.SourceInstance, m.SourceEnvironment, m.SourceID, m.PromotionID)
	if err != nil {
		if importUniqueConflict(err, "v3_config_alert_rules") {
			return "", store.ErrConflict
		}
		return "", err
	}
	return targetID, nil
}

// verifyImportedAlertRule is the read-side target proof for an imported item.
// The caller separately verifies the signed MR, import ledger and success
// audit. A production rule is a staged definition, not a live provider rule.
func (s *Service) verifyImportedAlertRule(ctx context.Context, m readManifestV2, targetID string) error {
	if s == nil || s.DB == nil || m.Kind != "alert_rule" || m.AlertRule == nil ||
		targetID != "CAR-"+m.PromotionID {
		return errUntrusted
	}
	if _, err := m.validate(m.PromotionID); err != nil {
		return errUntrusted
	}
	if !sanitizedAlertSnapshot(m.SourceEnvironment, "", m.SourceID, *m.AlertRule) {
		return errUntrusted
	}
	labels, err := json.Marshal(m.AlertRule.Labels)
	if err != nil {
		return errUntrusted
	}
	annotations, err := json.Marshal(m.AlertRule.Annotations)
	if err != nil {
		return errUntrusted
	}
	var exact bool
	err = s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM v3_config_alert_rules r
 JOIN applications a ON a.id=r.application_id AND NOT a.archived
 JOIN v3_application_environments ve ON ve.application_id=a.id AND ve.environment_id='prod'
 JOIN application_environments ae ON ae.application_id=a.id AND ae.key='prod' AND NOT ae.archived
 WHERE r.id=$1 AND r.application_id=$2 AND r.environment_id='prod' AND r.name=$3
 AND r.source_version=$4 AND r.expression=$5 AND r.duration_seconds=$6
 AND r.labels=$7::jsonb AND r.annotations=$8::jsonb AND r.source_identity_digest=$9
 AND r.source_digest=$10 AND r.source_instance=$11 AND r.source_environment=$12
 AND r.source_id=$13 AND r.promotion_id=$14 AND r.activation_state='not_applied')`, targetID, m.ApplicationID,
		m.AlertRule.Name, m.SourceVersion, m.AlertRule.Expression, m.AlertRule.DurationSeconds,
		labels, annotations, m.AlertRule.SourceIdentityDigest, m.SourceDigest,
		m.SourceInstance, m.SourceEnvironment, m.SourceID, m.PromotionID).Scan(&exact)
	if err != nil {
		return err
	}
	if !exact {
		return errUntrusted
	}
	return nil
}
