package notify

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// SMTPDialer opens a connection to the SMTP server at addr. *net.Dialer
// satisfies it; tests inject a fake that returns a connection to an in-memory
// sink.
type SMTPDialer interface {
	Dial(network, addr string) (net.Conn, error)
}

// SMTPDialFunc adapts a function to SMTPDialer.
type SMTPDialFunc func(network, addr string) (net.Conn, error)

// Dial implements SMTPDialer.
func (f SMTPDialFunc) Dial(network, addr string) (net.Conn, error) {
	return f(network, addr)
}

// Email composes an RFC 5322 message and delivers it over SMTP. The dialer is
// injectable so the delivery path is testable against a fake SMTP sink.
type Email struct {
	// Addr is the SMTP server as host:port.
	Addr string
	// From is the envelope and header sender.
	From string
	// To is the recipient list.
	To []string
	// Host overrides the EHLO/HELO name; otherwise it is derived from Addr.
	Host string
	// Username and Password, when set, enable PLAIN authentication.
	Username string
	Password string
	// Dialer is the transport. Nil means a real *net.Dialer.
	Dialer SMTPDialer
	// Timeout bounds the whole SMTP conversation; zero means ten seconds.
	Timeout time.Duration
	// MinSev is the channel's built-in minimum severity.
	MinSev Severity
}

// Name implements Channel.
func (e Email) Name() string { return "email" }

// MinSeverity implements Channel.
func (e Email) MinSeverity() Severity { return e.MinSev }

func (e Email) timeout() time.Duration {
	if e.Timeout > 0 {
		return e.Timeout
	}
	return 10 * time.Second
}

func (e Email) heloHost() string {
	if e.Host != "" {
		return e.Host
	}
	if host, _, err := net.SplitHostPort(e.Addr); err == nil && host != "" {
		return host
	}
	return "localhost"
}

// sanitizeHeader removes CR/LF so a signature can never inject a header.
func sanitizeHeader(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}

// Build implements Channel. It composes the message; delivery happens in Send.
func (e Email) Build(ev Event) (Payload, error) {
	if e.Addr == "" {
		return Payload{}, errors.New("email: SMTP address is empty")
	}
	if e.From == "" {
		return Payload{}, errors.New("email: From address is empty")
	}
	if len(e.To) == 0 {
		return Payload{}, errors.New("email: no recipients")
	}

	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", sanitizeHeader(e.From))
	to := make([]string, 0, len(e.To))
	for _, rcpt := range e.To {
		to = append(to, sanitizeHeader(rcpt))
	}
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(to, ", "))
	fmt.Fprintf(&b, "Subject: [shhgit] %s (%s)\r\n", sanitizeHeader(ev.Signature), ev.Severity)
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().UTC().Format(time.RFC1123Z))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=\"utf-8\"\r\n")
	b.WriteString("\r\n")
	b.WriteString("shhgit finding\r\n\r\n")
	for _, line := range detailLines(ev) {
		b.WriteString(line)
		b.WriteString("\r\n")
	}

	return Payload{
		Method: "SMTP",
		URL:    "smtp://" + e.Addr,
		Body:   []byte(b.String()),
	}, nil
}

// Send implements Sender: it dials the SMTP server and delivers p.Body.
func (e Email) Send(p Payload) error {
	if e.Addr == "" {
		return errors.New("email: SMTP address is empty")
	}
	if e.From == "" {
		return errors.New("email: From address is empty")
	}
	if len(e.To) == 0 {
		return errors.New("email: no recipients")
	}

	dialer := e.Dialer
	if dialer == nil {
		d := &net.Dialer{Timeout: e.timeout()}
		dialer = SMTPDialFunc(d.Dial)
	}
	conn, err := dialer.Dial("tcp", e.Addr)
	if err != nil {
		return fmt.Errorf("email: dial %s: %w", e.Addr, err)
	}
	if t := e.timeout(); t > 0 {
		_ = conn.SetDeadline(time.Now().Add(t))
	}

	host := e.heloHost()
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("email: handshake: %w", err)
	}
	defer c.Close()

	// Negotiate STARTTLS before AUTH. smtp.PlainAuth refuses to send credentials
	// over an unencrypted connection, so without this a configured username and
	// password always failed with "unencrypted connection" against a normal
	// submission server.
	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: host}); err != nil {
			return fmt.Errorf("email: starttls: %w", err)
		}
	}

	if e.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", e.Username, e.Password, host)); err != nil {
			return fmt.Errorf("email: auth: %w", err)
		}
	}
	if err := c.Mail(e.From); err != nil {
		return fmt.Errorf("email: MAIL FROM: %w", err)
	}
	for _, to := range e.To {
		if err := c.Rcpt(to); err != nil {
			return fmt.Errorf("email: RCPT TO %s: %w", to, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("email: DATA: %w", err)
	}
	if _, err := w.Write(p.Body); err != nil {
		_ = w.Close()
		return fmt.Errorf("email: write body: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("email: finish message: %w", err)
	}
	if err := c.Quit(); err != nil {
		return fmt.Errorf("email: QUIT: %w", err)
	}
	return nil
}
