package store

import (
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/destinywans/redstring/internal/core"
)

var catalogKey = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,120}$`)

func safeCatalogURL(value string) bool {
	if value == "" {
		return true
	}
	u, err := url.Parse(value)
	return err == nil && len(value) <= 2048 && (u.Scheme == "https" || u.Scheme == "http") && u.Hostname() != "" && u.User == nil && !strings.ContainsAny(value, "\r\n\x00")
}

// ValidateApplication validates metadata only; it never probes URLs or executes build commands.
func ValidateApplication(a core.Application) error {
	if strings.TrimSpace(a.Name) == "" || utf8.RuneCountInString(a.Name) > 120 || !catalogKey.MatchString(a.Key) || len(a.Description) > 8000 || len(a.Tags) > 24 {
		return ErrInvalidReference
	}
	for _, tag := range a.Tags {
		if strings.TrimSpace(tag) == "" || utf8.RuneCountInString(tag) > 64 {
			return ErrInvalidReference
		}
	}
	for _, value := range []string{a.Profile.BusinessUnit, a.Profile.ServiceType, a.Profile.Lifecycle, a.Profile.Criticality, a.Profile.Language, a.Profile.Framework, a.Profile.DefaultBranch} {
		if utf8.RuneCountInString(value) > 120 || strings.ContainsAny(value, "\r\n\x00") {
			return ErrInvalidReference
		}
	}
	if len(a.Profile.BuildCommand) > 2000 || len(a.Profile.ImageRepository) > 1024 || strings.ContainsRune(a.Profile.BuildCommand, 0) || strings.ContainsAny(a.Profile.ImageRepository, "\r\n\x00") {
		return ErrInvalidReference
	}
	for _, value := range []string{a.RepositoryURL, a.RunbookURL, a.Profile.DocumentationURL} {
		if !safeCatalogURL(value) {
			return ErrInvalidReference
		}
	}
	return nil
}

func validateConfiguration(c *core.EnvironmentConfiguration) error {
	if c == nil {
		return nil
	}
	for _, field := range []struct {
		value    *int
		min, max int
	}{
		{c.Replicas, 0, 10000}, {c.CPURequestMillicores, 0, 1000000}, {c.CPULimitMillicores, 1, 1000000},
		{c.MemoryRequestMiB, 0, 1048576}, {c.MemoryLimitMiB, 1, 1048576}, {c.Port, 1, 65535},
		{c.ConnectionPoolMax, 1, 1000000}, {c.RequestTimeoutMS, 1, 3600000}, {c.RateLimitRPS, 0, 100000000},
		{c.Retries, 0, 100}, {c.CircuitBreakerPercent, 0, 100},
	} {
		if field.value != nil && (*field.value < field.min || *field.value > field.max) {
			return ErrInvalidReference
		}
	}
	if c.CPURequestMillicores != nil && c.CPULimitMillicores != nil && *c.CPURequestMillicores > *c.CPULimitMillicores {
		return ErrInvalidReference
	}
	if c.MemoryRequestMiB != nil && c.MemoryLimitMiB != nil && *c.MemoryRequestMiB > *c.MemoryLimitMiB {
		return ErrInvalidReference
	}
	return nil
}
