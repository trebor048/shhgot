package notify

import (
	"bufio"
	"net"
	"strings"
	"testing"
	"time"
)

// fakeSMTP is an in-memory SMTP sink. Its Dial never touches the network: it
// returns one end of a net.Pipe and speaks the minimal SMTP dialogue on the
// other, recording each DATA payload.
type fakeSMTP struct {
	msgs chan string
}

func newFakeSMTP() *fakeSMTP {
	return &fakeSMTP{msgs: make(chan string, 4)}
}

// Dial implements SMTPDialer.
func (f *fakeSMTP) Dial(_, _ string) (net.Conn, error) {
	client, server := net.Pipe()
	go f.serve(server)
	return client, nil
}

func (f *fakeSMTP) serve(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)
	write := func(s string) {
		_, _ = w.WriteString(s)
		_ = w.Flush()
	}

	write("220 fake ESMTP ready\r\n")

	var data strings.Builder
	inData := false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		if inData {
			if line == ".\r\n" || line == ".\n" {
				inData = false
				f.msgs <- data.String()
				write("250 OK queued\r\n")
				continue
			}
			data.WriteString(line)
			continue
		}

		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"):
			write("250-fake\r\n250 OK\r\n")
		case strings.HasPrefix(cmd, "HELO"):
			write("250 OK\r\n")
		case strings.HasPrefix(cmd, "MAIL"), strings.HasPrefix(cmd, "RCPT"):
			write("250 OK\r\n")
		case cmd == "DATA":
			inData = true
			write("354 End data with <CR><LF>.<CR><LF>\r\n")
		case cmd == "QUIT":
			write("221 Bye\r\n")
			return
		default:
			write("250 OK\r\n")
		}
	}
}

func TestEmailReachesFakeSMTPSink(t *testing.T) {
	sink := newFakeSMTP()
	ch := Email{
		Addr:   "smtp.example:25",
		From:   "shhgit@example.com",
		To:     []string{"ops@example.com"},
		Dialer: sink,
		MinSev: SevLow,
	}
	ev := Event{
		Signature: "OpenAI API Key",
		Severity:  SevHigh,
		Repo:      "acme/app",
		File:      "config.py",
		Line:      12,
		Secret:    "sk-abcdef0123456789",
	}

	p, err := ch.Build(ev)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if p.Method != "SMTP" {
		t.Fatalf("method = %q, want SMTP", p.Method)
	}
	if err := ch.Send(p); err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case msg := <-sink.msgs:
		if !strings.Contains(msg, "OpenAI API Key") {
			t.Fatalf("message missing signature: %q", msg)
		}
		if !strings.Contains(msg, "high") {
			t.Fatalf("message missing severity: %q", msg)
		}
		if !strings.Contains(msg, "config.py:12") {
			t.Fatalf("message missing location: %q", msg)
		}
		if strings.Contains(msg, "sk-abcdef0123456789") {
			t.Fatalf("message leaks the raw secret: %q", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("fake SMTP sink received no message")
	}

	select {
	case extra := <-sink.msgs:
		t.Fatalf("sink received more than one message: %q", extra)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestEmailBuildRejectsIncompleteConfig(t *testing.T) {
	cases := []Email{
		{From: "a@b", To: []string{"c@d"}},
		{Addr: "smtp:25", To: []string{"c@d"}},
		{Addr: "smtp:25", From: "a@b"},
	}
	for i, ch := range cases {
		if _, err := ch.Build(Event{Severity: SevHigh}); err == nil {
			t.Fatalf("case %d: expected an error", i)
		}
	}
}
