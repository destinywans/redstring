package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/destinywans/redstring/internal/store"
	"github.com/destinywans/redstring/internal/v3admin"
)

const maxV3MemberSourcesConfigSize = 32 << 10
const maxV3MemberSecretSize = 4096

var v3MemberTenantBindingID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)
var v3MemberEvidenceHost = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]{0,251}[a-z0-9])?$`)

type v3MemberSources struct {
	MemberSync   *v3admin.MemberSyncService
	MFAEvidence  v3admin.MFAEvidenceSource
	MFAReminders *v3admin.MFAReminderService
}

type v3MemberSourcesFile struct {
	Tier            string                    `json:"tier"`
	InstanceID      string                    `json:"instance_id"`
	EnvironmentID   string                    `json:"environment_id"`
	TenantBindingID string                    `json:"tenant_binding_id"`
	Directory       *v3MemberDirectoryFile    `json:"directory"`
	MFAEvidence     *v3MemberMFAEvidenceFile  `json:"mfa_evidence"`
	MFAReminders    *v3MemberMFARemindersFile `json:"mfa_reminders"`
}

type v3MemberDirectoryFile struct {
	AppID                    string `json:"app_id"`
	AppSecretFile            string `json:"app_secret_file"`
	RootDepartmentID         string `json:"root_department_id"`
	AddressBookScopeApproved bool   `json:"address_book_scope_approved"`
}

type v3MemberMFAEvidenceFile struct {
	URL             string `json:"url"`
	BearerTokenFile string `json:"bearer_token_file"`
}

type v3MemberMFARemindersFile struct {
	AppID               string `json:"app_id"`
	AppSecretFile       string `json:"app_secret_file"`
	TenantScopeApproved bool   `json:"tenant_scope_approved"`
}

// loadV3MemberSources only constructs adapters. It never reads the remote
// directory, requests MFA evidence or sends Feishu messages during startup.
// One private file binds these capabilities to one exact V3 environment.
func loadV3MemberSources(path string, instance v3Instance, s *store.Store) (v3MemberSources, error) {
	var sources v3MemberSources
	if path == "" {
		return sources, nil
	}
	if s == nil {
		return sources, errors.New("member sources require a store")
	}
	content, err := readV3MemberPrivateFile(path, maxV3MemberSourcesConfigSize)
	if err != nil {
		return sources, errors.New("member sources require an absolute 0600 regular config file of at most 32 KiB")
	}
	var config v3MemberSourcesFile
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&config) != nil || decoder.Decode(new(any)) != io.EOF {
		return sources, errors.New("member sources config must contain one object with known fields")
	}
	if config.Tier != instance.Tier || config.InstanceID != instance.Domain ||
		!v3MemberTenantBindingID.MatchString(config.TenantBindingID) ||
		config.EnvironmentID == "" || config.EnvironmentID == "all" ||
		(config.Directory == nil && config.MFAEvidence == nil && config.MFAReminders == nil) {
		return sources, errors.New("member sources config does not match the V3 instance or has no source")
	}
	knownEnvironment := false
	for _, environment := range instance.Environments {
		if environment.ID == config.EnvironmentID {
			knownEnvironment = true
			break
		}
	}
	if !knownEnvironment {
		return sources, errors.New("member sources environment is outside the V3 instance")
	}
	if config.MFAReminders != nil && config.MFAEvidence == nil {
		return sources, errors.New("MFA reminders require authoritative MFA evidence")
	}
	if config.Directory != nil {
		secret, err := readV3MemberSecret(config.Directory.AppSecretFile)
		if err != nil {
			return sources, errors.New("invalid protected Feishu directory secret file")
		}
		reader, err := v3admin.NewFeishuDirectoryReader(v3admin.FeishuDirectoryConfig{
			AppID: config.Directory.AppID, AppSecret: secret,
			BindingID:                config.TenantBindingID,
			RootDepartmentID:         config.Directory.RootDepartmentID,
			AddressBookScopeApproved: config.Directory.AddressBookScopeApproved,
		})
		if err != nil {
			return sources, errors.New("invalid reviewed Feishu directory binding")
		}
		sources.MemberSync = &v3admin.MemberSyncService{Store: s, EnvironmentID: config.EnvironmentID, Reader: reader}
	}
	if config.MFAEvidence != nil {
		u, err := url.Parse(config.MFAEvidence.URL)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.Port() != "" ||
			u.User != nil || u.Fragment != "" || u.RawQuery != "" || u.Opaque != "" ||
			net.ParseIP(u.Hostname()) != nil || !v3MemberEvidenceHost.MatchString(u.Hostname()) {
			return sources, errors.New("MFA evidence requires a reviewed exact HTTPS DNS endpoint without port")
		}
		token, err := readV3MemberSecret(config.MFAEvidence.BearerTokenFile)
		if err != nil {
			return sources, errors.New("invalid protected MFA evidence token file")
		}
		sources.MFAEvidence, err = v3admin.NewBoundHTTPMFAEvidenceSource(v3admin.BoundHTTPMFAEvidenceConfig{
			HTTPMFAEvidenceConfig: v3admin.HTTPMFAEvidenceConfig{URL: config.MFAEvidence.URL, BearerToken: token},
			InstanceID:            config.InstanceID, TenantBindingID: config.TenantBindingID,
			EnvironmentID: config.EnvironmentID,
		})
		if err != nil {
			return sources, errors.New("invalid bound MFA evidence source")
		}
	}
	if config.MFAReminders != nil {
		if !config.MFAReminders.TenantScopeApproved || config.MFAReminders.AppID == "" ||
			(config.Directory != nil && config.MFAReminders.AppID == config.Directory.AppID) {
			return sources, errors.New("MFA reminders require a reviewed dedicated Feishu bot application")
		}
		secret, err := readV3MemberSecret(config.MFAReminders.AppSecretFile)
		if err != nil {
			return sources, errors.New("invalid protected Feishu bot secret file")
		}
		sender, err := v3admin.NewFeishuAPIMFAReminderSender(v3admin.FeishuMFAReminderConfig{
			AppID: config.MFAReminders.AppID, AppSecret: secret,
		})
		if err != nil {
			return sources, errors.New("invalid Feishu MFA reminder bot")
		}
		sources.MFAReminders = &v3admin.MFAReminderService{Sender: sender}
	}
	return sources, nil
}

func readV3MemberPrivateFile(path string, maxSize int64) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("path must be absolute")
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm() != 0600 ||
		before.Size() < 1 || before.Size() > maxSize {
		return nil, errors.New("private file must be regular and mode 0600")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot open private file")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) || !opened.Mode().IsRegular() || opened.Mode().Perm() != 0600 {
		return nil, errors.New("private file changed during opening")
	}
	content, err := io.ReadAll(io.LimitReader(file, maxSize+1))
	if err != nil || len(content) < 1 || int64(len(content)) > maxSize {
		return nil, errors.New("private file is outside its size limit")
	}
	return content, nil
}

func readV3MemberSecret(path string) (string, error) {
	content, err := readV3MemberPrivateFile(path, maxV3MemberSecretSize)
	if err != nil {
		return "", err
	}
	secret := strings.TrimSpace(string(content))
	if secret == "" || strings.ContainsAny(secret, "\r\n\t ") {
		return "", errors.New("invalid secret value")
	}
	return secret, nil
}
