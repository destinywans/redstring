package main

import (
	"fmt"
	"net/url"
	"os"

	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/v3admin"
	"github.com/wans/aiops-canvas/internal/v3settings"
)

// Invitation delivery is an explicit deployment opt-in. A half-configured
// sender must fail startup instead of exposing a misleading send button.
func configureV3Invitations(info v3Instance, api *httpapi.Server) (*v3admin.InvitationService, error) {
	host := os.Getenv("AIOPS_V3_INVITATION_SMTP_HOST")
	from := os.Getenv("AIOPS_V3_INVITATION_SMTP_FROM")
	username := os.Getenv("AIOPS_V3_INVITATION_SMTP_USERNAME")
	passwordFile := os.Getenv("AIOPS_V3_INVITATION_SMTP_PASSWORD_FILE")
	entry := os.Getenv("AIOPS_V3_INVITATION_ENTRY_URL")
	if host == "" && from == "" && username == "" && passwordFile == "" && entry == "" {
		return nil, nil
	}
	if host == "" || from == "" || username == "" || passwordFile == "" || entry == "" ||
		api == nil || !api.SecureCookies {
		return nil, fmt.Errorf("complete SMTP, product entry and secure-cookie configuration is required")
	}
	trusted, err := url.Parse(api.AllowedOrigin)
	if err != nil || trusted.Scheme != "https" || trusted.Hostname() != info.Domain ||
		trusted.User != nil || (trusted.Path != "" && trusted.Path != "/") ||
		trusted.RawQuery != "" || trusted.Fragment != "" || trusted.Opaque != "" {
		return nil, fmt.Errorf("trusted HTTPS product origin must match this instance")
	}
	page, err := url.Parse(entry)
	if err != nil || page.Scheme != "https" || page.Host != trusted.Host || page.User != nil ||
		page.RawQuery != "" || page.Fragment != "" || page.Opaque != "" {
		return nil, fmt.Errorf("invitation entry must use this instance's trusted HTTPS origin")
	}
	password, err := v3settings.LoadOncallTokenFile(passwordFile)
	if err != nil {
		return nil, fmt.Errorf("invalid protected SMTP password file")
	}
	sender, err := v3admin.NewSMTPInvitationSender(v3admin.SMTPInvitationConfig{
		Host: host, Port: 465, From: from, Username: username, Password: password,
		CAFile: os.Getenv("AIOPS_V3_INVITATION_SMTP_CA_FILE"),
	})
	if err != nil {
		return nil, fmt.Errorf("invalid SMTP invitation configuration")
	}
	invitations, err := v3admin.NewInvitationService(sender, entry)
	if err != nil {
		return nil, fmt.Errorf("invalid invitation product entry")
	}
	return invitations, nil
}
