package v3admin

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"os"
	"path/filepath"
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
	// CAFile optionally adds a PEM bundle to the system roots for a relay
	// behind an internal CA. Hostname verification still applies.
	CAFile string
}

type SMTPInvitationSender struct {
	config SMTPInvitationConfig
	from   string
	roots  *x509.CertPool
}

var smtpHostPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)

func NewSMTPInvitationSender(config SMTPInvitationConfig) (*SMTPInvitationSender, error) {
	from, err := mail.ParseAddress(config.From)
	if err != nil || from.Address != config.From ||
		!smtpHostPattern.MatchString(config.Host) || strings.Contains(config.Host, "..") ||
		config.Port != 465 || config.Username == "" || config.Password == "" {
		return nil, errInvitationUnavailable
	}
	sender := &SMTPInvitationSender{config: config, from: from.Address}
	if config.CAFile != "" {
		pem, err := os.ReadFile(config.CAFile)
		if err != nil || !filepath.IsAbs(config.CAFile) || len(pem) > 1<<20 {
			return nil, errInvitationUnavailable
		}
		roots, err := x509.SystemCertPool()
		if err != nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(pem) {
			return nil, errInvitationUnavailable
		}
		sender.roots = roots
	}
	return sender, nil
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
		ServerName: s.config.Host, MinVersion: tls.VersionTLS12, RootCAs: s.roots,
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
	if _, err = io.WriteString(w, invitationMail(s.from, message)); err != nil {
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

// invitationMail renders the invitation in Chinese and English: the
// invitee's language is unknown before they sign in (PA-33). The subject is
// an RFC 2047 encoded word because it is not ASCII.
func invitationMail(from string, message InvitationMessage) string {
	expires := message.ExpiresAt.UTC().Format(time.RFC3339)
	subject := mime.BEncoding.Encode("utf-8", "Redstring 邀请 / Redstring invitation")
	return fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\n"+
		"MIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n"+
		"你已受邀以只读成员身份加入 Redstring。\r\n"+
		"可信产品入口：%s\r\n一次性邀请码：%s\r\n邀请码过期时间（UTC）：%s\r\n\r\n"+
		"请通过接受邀请流程设置姓名和密码。环境和应用的访问权限需要管理员另行授予。\r\n"+
		"\r\n----\r\n\r\n"+
		"You have been invited to Redstring as a read-only member.\r\n"+
		"Trusted product entry: %s\r\nOne-time invitation code: %s\r\n"+
		"Code expires at: %s\r\n\r\n"+
		"Set your name and password through the invitation acceptance flow.\r\n"+
		"An administrator must separately grant environment and application access.\r\n",
		from, message.Email, subject, message.EntryURL, message.Code, expires,
		message.EntryURL, message.Code, expires)
}
