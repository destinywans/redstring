package v3admin

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/mail"
	"net/smtp"
	"regexp"
	"strings"
	"time"
)

// SMTPInvitationConfig is supplied by the trusted composition root. Port 465
// requires TLS before credentials or invitation content are transmitted.
type SMTPInvitationConfig struct {
	Host     string
	Port     int
	From     string
	Username string
	Password string
}

type SMTPInvitationSender struct {
	config SMTPInvitationConfig
	from   string
}

var smtpHostPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)

func NewSMTPInvitationSender(config SMTPInvitationConfig) (*SMTPInvitationSender, error) {
	from, err := mail.ParseAddress(config.From)
	if err != nil || from.Address != config.From ||
		!smtpHostPattern.MatchString(config.Host) || strings.Contains(config.Host, "..") ||
		config.Port != 465 || config.Username == "" || config.Password == "" {
		return nil, errInvitationUnavailable
	}
	return &SMTPInvitationSender{config: config, from: from.Address}, nil
}

func (s *SMTPInvitationSender) SendInvitation(ctx context.Context, message InvitationMessage) error {
	if s == nil || message.Code == "" || message.EntryURL == "" || message.Email == "" {
		return errInvitationUnavailable
	}
	address := net.JoinHostPort(s.config.Host, "465")
	dialer := net.Dialer{Timeout: 8 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return errInvitationUnavailable
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	tlsConn := tls.Client(conn, &tls.Config{
		ServerName: s.config.Host, MinVersion: tls.VersionTLS12,
	})
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		return errInvitationUnavailable
	}
	client, err := smtp.NewClient(tlsConn, s.config.Host)
	if err != nil {
		return errInvitationUnavailable
	}
	defer client.Close()
	if err := client.Auth(smtp.PlainAuth("", s.config.Username, s.config.Password, s.config.Host)); err != nil {
		return errInvitationUnavailable
	}
	if err := client.Mail(s.from); err != nil {
		return errInvitationUnavailable
	}
	if err := client.Rcpt(message.Email); err != nil {
		return errInvitationUnavailable
	}
	w, err := client.Data()
	if err != nil {
		return errInvitationUnavailable
	}
	body := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: AIOps Canvas invitation\r\n"+
		"MIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n"+
		"You have been invited to AIOps Canvas as a read-only member.\r\n"+
		"Trusted product entry: %s\r\nOne-time invitation code: %s\r\n"+
		"Code expires at: %s\r\n\r\n"+
		"Set your name and password through the invitation acceptance flow.\r\n"+
		"An administrator must separately grant environment and application access.\r\n",
		s.from, message.Email, message.EntryURL, message.Code,
		message.ExpiresAt.UTC().Format(time.RFC3339))
	if _, err = io.WriteString(w, body); err != nil {
		_ = w.Close()
		return errInvitationUnavailable
	}
	// Close reads the relay's final DATA response. 250 means relay accepted;
	// it does not prove delivery to the recipient's mailbox.
	if err = w.Close(); err != nil {
		return errInvitationUnavailable
	}
	if err = client.Quit(); err != nil && !errors.Is(err, io.EOF) {
		// DATA acceptance already happened. An uncertain QUIT must not trigger
		// a second send; the caller will finalize the one accepted attempt.
		return nil
	}
	return nil
}
