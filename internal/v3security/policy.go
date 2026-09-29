package v3security

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var errInvalidView = errors.New("invalid security view")
var errReadOnlyChange = errors.New("security field is not editable")

func validSnapshot(v RuntimeSnapshot, scope RuntimeScope, now time.Time) bool {
	if v.InstanceID != scope.InstanceID || v.EnvironmentID != scope.EnvironmentID ||
		v.Revision < 1 || !v.Complete || !v.Enforced ||
		v.ObservedAt.IsZero() || v.ValidUntil.IsZero() ||
		v.ObservedAt.After(now.Add(5*time.Second)) || now.Sub(v.ObservedAt) > 30*time.Second ||
		!v.ValidUntil.After(now) || v.ValidUntil.After(v.ObservedAt.Add(2*time.Minute)) {
		return false
	}
	return validView(v.View) == nil
}

func validView(v SecurityView) error {
	if v.SSO == nil || v.MFAMethods == nil ||
		(v.MFAPolicy != "all" && v.MFAPolicy != "privileged" && v.MFAPolicy != "none") ||
		!boundedText(v.Session.Idle, 120) || !boundedText(v.Session.Max, 120) ||
		!boundedText(v.Vault.Name, 120) || !boundedText(v.Vault.Sub, 240) ||
		!boundedText(v.TokenTTL, 120) || !boundedText(v.PasswordPolicy, 240) ||
		len(v.SSO) > 32 || len(v.MFAMethods) > 32 || !validIPAllow(v.Session.IPAllow) {
		return errInvalidView
	}
	seenSSO, seenMFA := map[string]bool{}, map[string]bool{}
	for _, provider := range v.SSO {
		if !boundedText(provider.Brand, 80) || !boundedText(provider.Name, 120) ||
			!boundedText(provider.Sub, 240) || seenSSO[provider.Brand+"\x00"+provider.Name] {
			return errInvalidView
		}
		seenSSO[provider.Brand+"\x00"+provider.Name] = true
	}
	for _, method := range v.MFAMethods {
		if !boundedText(method.ID, 80) || !boundedText(method.Label, 120) ||
			utf8.RuneCountInString(method.Note) > 240 || seenMFA[method.ID] {
			return errInvalidView
		}
		seenMFA[method.ID] = true
	}
	if v.MFAPolicy != "none" && len(v.MFAMethods) == 0 {
		return errInvalidView
	}
	return nil
}

func boundedText(value string, max int) bool {
	return value != "" && strings.TrimSpace(value) == value && utf8.ValidString(value) &&
		utf8.RuneCountInString(value) <= max
}

// The form's IP field is a comma-separated list of literal IP addresses or
// CIDR ranges. Empty means no allow-list; hostnames and unconstrained prose
// are rejected so an enforcement adapter cannot interpret them differently.
func validIPAllow(value string) bool {
	if len(value) > 2048 {
		return false
	}
	if value == "" {
		return true
	}
	parts := strings.Split(value, ",")
	if len(parts) > 64 {
		return false
	}
	for _, part := range parts {
		item := strings.TrimSpace(part)
		if item == "" || (net.ParseIP(item) == nil && !validCIDR(item)) {
			return false
		}
	}
	return true
}

func validCIDR(value string) bool { _, _, err := net.ParseCIDR(value); return err == nil }

func controlsFromView(v SecurityView) SecurityControls {
	controls := SecurityControls{SCIM: v.SCIM, MFAPolicy: v.MFAPolicy,
		ApprovalPasskey: v.ApprovalPasskey, IPAllow: v.Session.IPAllow,
		NewDeviceNotice: v.Session.NewDeviceNotice, MFAMethods: make([]MFAMethodControl, 0, len(v.MFAMethods))}
	for _, method := range v.MFAMethods {
		controls.MFAMethods = append(controls.MFAMethods, MFAMethodControl{ID: method.ID, Enabled: method.Enabled})
	}
	sort.Slice(controls.MFAMethods, func(i, j int) bool { return controls.MFAMethods[i].ID < controls.MFAMethods[j].ID })
	return controls
}

func validateEditableView(current, desired SecurityView) (SecurityControls, error) {
	if err := validView(desired); err != nil {
		return SecurityControls{}, err
	}
	if !reflect.DeepEqual(current.SSO, desired.SSO) ||
		current.Session.Idle != desired.Session.Idle || current.Session.Max != desired.Session.Max ||
		!reflect.DeepEqual(current.Vault, desired.Vault) ||
		current.TokenTTL != desired.TokenTTL || current.PasswordPolicy != desired.PasswordPolicy ||
		len(current.MFAMethods) != len(desired.MFAMethods) {
		return SecurityControls{}, errReadOnlyChange
	}
	oldMethods := make(map[string]MFAMethod, len(current.MFAMethods))
	for _, method := range current.MFAMethods {
		oldMethods[method.ID] = method
	}
	for _, method := range desired.MFAMethods {
		old, ok := oldMethods[method.ID]
		if !ok || old.Label != method.Label || old.Note != method.Note {
			return SecurityControls{}, errReadOnlyChange
		}
	}
	controls := controlsFromView(desired)
	if desired.MFAPolicy != "none" {
		oneEnabled := false
		for _, method := range controls.MFAMethods {
			oneEnabled = oneEnabled || method.Enabled
		}
		if !oneEnabled {
			return SecurityControls{}, errInvalidView
		}
	}
	return controls, nil
}

func sameControls(a, b SecurityControls) bool { return reflect.DeepEqual(a, b) }

func scopeDigest(scope RuntimeScope) [32]byte {
	return sha256.Sum256(marshal(scope))
}

func scopeLockKey(scope RuntimeScope) int64 {
	digest := scopeDigest(scope)
	return int64(binary.BigEndian.Uint64(digest[:8]))
}

func securityETag(scope RuntimeScope, revision int64) string {
	digest := scopeDigest(scope)
	return fmt.Sprintf(`"security-%s-%d"`, hex.EncodeToString(digest[:]), revision)
}

func parseSecurityIfMatch(value string, scope RuntimeScope) (int64, bool) {
	value = strings.TrimSpace(value)
	lastDash := strings.LastIndexByte(value, '-')
	if len(value) < 3 || value[0] != '"' || value[len(value)-1] != '"' || lastDash < 0 {
		return 0, false
	}
	revision, err := strconv.ParseInt(value[lastDash+1:len(value)-1], 10, 64)
	return revision, err == nil && revision > 0 && value == securityETag(scope, revision)
}

func marshal(value any) []byte { encoded, _ := json.Marshal(value); return encoded }
