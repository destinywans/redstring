package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/v3configsync"
)

const maxV3ConfigSyncConfigSize = 16 << 10

var v3ConfigSyncSourceID = regexp.MustCompile(`^[a-z][a-z0-9-]{1,63}$`)

type v3ConfigSyncFile struct {
	Tier             string             `json:"tier"`
	GitLab           v3ConfigSyncGitLab `json:"gitlab"`
	SourceInstanceID string             `json:"source_instance_id"`
	SigningKeyFile   string             `json:"signing_key_file"`
	TrustedSources   map[string]string  `json:"trusted_sources"`
}

type v3ConfigSyncGitLab struct {
	BaseURL           string `json:"base_url"`
	ProjectID         int64  `json:"project_id"`
	TargetBranch      string `json:"target_branch"`
	BranchPrefix      string `json:"branch_prefix"`
	TokenFile         string `json:"token_file"`
	AllowLoopbackHTTP bool   `json:"allow_loopback_http"`
	// CAFile is an optional PEM bundle for a GitLab behind an internal CA;
	// only this client trusts it.
	CAFile string `json:"ca_file"`
	// WebURL is GitLab's external_url when the API is reached through
	// another address; merge request links must be on it.
	WebURL string `json:"web_url"`
}

// loadV3ConfigSync binds the server to one dedicated configuration repository.
// The optional file contains paths to secrets, never token or key material.
// An unset path leaves routes registered but explicitly unavailable (501).
func loadV3ConfigSync(path string, tier string, db *pgxpool.Pool, access v3configsync.Access) (*v3configsync.Service, error) {
	service := v3configsync.New(db, access, nil)
	if path == "" {
		return service, nil
	}
	if tier != "nonprod" && tier != "production" {
		return nil, errors.New("configuration sync requires a valid instance tier")
	}
	if !filepath.IsAbs(path) {
		return nil, errors.New("configuration sync file path must be absolute")
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm() != 0600 || before.Size() < 1 || before.Size() > maxV3ConfigSyncConfigSize {
		return nil, errors.New("configuration sync file must be regular, mode 0600 and at most 16 KiB")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot read configuration sync file")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) || !opened.Mode().IsRegular() || opened.Mode().Perm() != 0600 {
		return nil, errors.New("configuration sync file changed during opening")
	}
	content, err := io.ReadAll(io.LimitReader(file, maxV3ConfigSyncConfigSize+1))
	if err != nil || len(content) > maxV3ConfigSyncConfigSize {
		return nil, errors.New("configuration sync file exceeds 16 KiB")
	}
	var config v3ConfigSyncFile
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("configuration sync file must contain one valid JSON object with known fields")
	}
	if config.Tier != tier {
		return nil, errors.New("configuration sync tier does not match the instance")
	}
	if !filepath.IsAbs(config.GitLab.TokenFile) {
		return nil, errors.New("configuration sync GitLab token file path must be absolute")
	}
	repo, err := v3configsync.NewGitLabClient(v3configsync.Config{
		BaseURL: config.GitLab.BaseURL, ProjectID: config.GitLab.ProjectID,
		TargetBranch: config.GitLab.TargetBranch, BranchPrefix: config.GitLab.BranchPrefix,
		TokenFile: config.GitLab.TokenFile, AllowLoopbackHTTP: config.GitLab.AllowLoopbackHTTP,
		CAFile: config.GitLab.CAFile, WebURL: config.GitLab.WebURL,
	})
	if err != nil {
		return nil, fmt.Errorf("invalid configuration sync GitLab binding: %w", err)
	}
	service.Repository = repo
	if tier == "nonprod" {
		if !v3ConfigSyncSourceID.MatchString(config.SourceInstanceID) || config.SourceInstanceID == "prod" ||
			!filepath.IsAbs(config.SigningKeyFile) || len(config.TrustedSources) != 0 {
			return nil, errors.New("non-production configuration sync requires one source ID and an absolute signing key file, without trusted sources")
		}
		key, err := v3configsync.LoadSigningKeyFile(config.SigningKeyFile)
		if err != nil {
			return nil, errors.New("invalid configuration sync signing key file")
		}
		service.SourceInstanceID, service.SigningKey = config.SourceInstanceID, key
		return service, nil
	}
	if config.SourceInstanceID != "" || config.SigningKeyFile != "" || len(config.TrustedSources) == 0 || len(config.TrustedSources) > 32 {
		return nil, errors.New("production configuration sync requires 1 to 32 trusted source keys and no signing key")
	}
	service.TrustedSources = make(map[string]ed25519.PublicKey, len(config.TrustedSources))
	for id, keyPath := range config.TrustedSources {
		if !v3ConfigSyncSourceID.MatchString(id) || id == "prod" || !filepath.IsAbs(keyPath) {
			return nil, errors.New("invalid configuration sync trusted source ID or key file path")
		}
		key, err := v3configsync.LoadTrustedPublicKeyFile(keyPath)
		if err != nil {
			return nil, errors.New("invalid configuration sync trusted public key file")
		}
		service.TrustedSources[id] = key
	}
	return service, nil
}
