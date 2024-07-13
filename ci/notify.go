package ci

import (
	"context"
	"errors"
	"strings"
	"text/template"
	"time"

	"github.com/wneessen/go-mail"
)

func (a *activities) Notify(ctx context.Context, req *notifyReq) error {
	params, err := getParams(a.cfg.SmtpParams)
	if err != nil {
		return err
	}
	parts := strings.SplitN(params, "~", 3)
	if len(parts) < 3 {
		return errors.New("bad params format")
	}

	hostPort, user, pass := parts[0], parts[1], parts[2]
	host := strings.Split(hostPort, ":")[0]

	now := time.Now()

	m := mail.NewMsg()
	if err := m.FromFormat("Charon CI", user); err != nil {
		return err
	} else if err = m.To(user); err != nil {
		return err
	}
	subj := "charon build " + now.Format(time.Stamp) + " "
	if req.Error != "" {
		subj += "error"
	} else {
		subj += "success"
	}
	m.Subject(subj)
	m.SetDateWithValue(now)

	err = m.SetBodyTextTemplate(
		template.Must(template.New("email").Parse(`
{{if .req.Error}}
Charon build error:

{{.req.Error}}
{{else}}
Charon build complete!

Nix channel: {{.req.RelID}}
Styx commit: {{.req.StyxCommit}}
{{end}}
`)),
		map[string]any{"req": req},
	)
	if err != nil {
		return err
	}

	c, err := mail.NewClient(host, mail.WithSSLPort(false), mail.WithSMTPAuth(mail.SMTPAuthPlain),
		mail.WithUsername(user), mail.WithPassword(pass))
	if err != nil {
		return err
	}
	return c.DialAndSendWithContext(ctx, m)
}
