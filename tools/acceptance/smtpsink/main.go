// Command smtpsink is a minimal implicit-TLS SMTP relay for acceptance tests.
// It accepts one AUTH PLAIN login and stores every message as a 0600 .eml file
// under -dir; nothing is forwarded. It only listens on loopback.
package main

import (
	"bufio"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

func main() {
	addrs := flag.String("listen", "127.0.0.1:465,[::1]:465", "comma-separated loopback listen addresses")
	cert := flag.String("cert", "", "TLS certificate (PEM)")
	key := flag.String("key", "", "TLS key (PEM)")
	userFile := flag.String("user-file", "", "file with the accepted username")
	passFile := flag.String("password-file", "", "file with the accepted password")
	dir := flag.String("dir", "", "directory for received messages")
	container := flag.Bool("container", false, "allow any listen address; only inside a container whose port is published on host loopback")
	flag.Parse()
	pair, err := tls.LoadX509KeyPair(*cert, *key)
	if err != nil {
		log.Fatal(err)
	}
	user, pass := readSecret(*userFile), readSecret(*passFile)
	if user == "" || pass == "" || *dir == "" {
		log.Fatal("user, password and dir are required")
	}
	if err := os.MkdirAll(*dir, 0o700); err != nil {
		log.Fatal(err)
	}
	config := &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
	var seq atomic.Int64
	for _, addr := range strings.Split(*addrs, ",") {
		host, _, err := net.SplitHostPort(addr)
		if ip := net.ParseIP(host); !*container && (err != nil || ip == nil || !ip.IsLoopback()) {
			log.Fatalf("refusing non-loopback address %q", addr)
		}
		ln, err := tls.Listen("tcp", addr, config)
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("smtpsink listening on %s", addr)
		go func() {
			for {
				conn, err := ln.Accept()
				if err != nil {
					log.Print(err)
					return
				}
				go serve(conn, user, pass, *dir, &seq)
			}
		}()
	}
	select {}
}

func readSecret(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func serve(conn net.Conn, user, pass, dir string, seq *atomic.Int64) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Minute))
	r := bufio.NewReader(conn)
	say := func(line string) { fmt.Fprintf(conn, "%s\r\n", line) }
	say("220 localhost ESMTP acceptance smtpsink")
	authed := false
	var from string
	var rcpt []string
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		verb := strings.ToUpper(strings.SplitN(line, " ", 2)[0])
		switch verb {
		case "EHLO", "HELO":
			say("250-localhost")
			say("250 AUTH PLAIN")
		case "AUTH":
			parts := strings.Fields(line)
			if len(parts) < 2 || strings.ToUpper(parts[1]) != "PLAIN" {
				say("504 only PLAIN")
				continue
			}
			payload := ""
			if len(parts) == 3 {
				payload = parts[2]
			} else {
				say("334 ")
				payload, _ = r.ReadString('\n')
				payload = strings.TrimSpace(payload)
			}
			raw, err := base64.StdEncoding.DecodeString(payload)
			fields := strings.Split(string(raw), "\x00")
			if err != nil || len(fields) != 3 ||
				subtle.ConstantTimeCompare([]byte(fields[1]), []byte(user)) != 1 ||
				subtle.ConstantTimeCompare([]byte(fields[2]), []byte(pass)) != 1 {
				say("535 authentication failed")
				continue
			}
			authed = true
			say("235 authenticated")
		case "MAIL":
			if !authed {
				say("530 authentication required")
				continue
			}
			from, rcpt = line, nil
			say("250 ok")
		case "RCPT":
			rcpt = append(rcpt, line)
			say("250 ok")
		case "DATA":
			if !authed || from == "" || len(rcpt) == 0 {
				say("503 bad sequence")
				continue
			}
			say("354 end with <CRLF>.<CRLF>")
			var body strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" || l == ".\n" {
					break
				}
				body.WriteString(strings.TrimPrefix(l, "."))
			}
			name := filepath.Join(dir, fmt.Sprintf("%d-%03d.eml", time.Now().UnixNano(), seq.Add(1)))
			content := "X-Sink-Envelope: " + from + " " + strings.Join(rcpt, " ") + "\r\n" + body.String()
			if err := os.WriteFile(name, []byte(content), 0o600); err != nil {
				say("451 cannot store")
				continue
			}
			from, rcpt = "", nil
			say("250 stored")
		case "RSET":
			from, rcpt = "", nil
			say("250 ok")
		case "NOOP":
			say("250 ok")
		case "QUIT":
			say("221 bye")
			return
		default:
			say("502 not implemented")
		}
	}
}
