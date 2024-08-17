package ci

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"text/template"
	"time"

	"github.com/wneessen/go-mail"
	"golang.org/x/text/message"
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

	args := map[string]any{
		"req":  req,
		"diff": makeDiff(req.PrevNames, req.NewNames),
	}
	err = m.SetBodyTextTemplate(
		template.Must(template.New("email").Funcs(map[string]any{
			"fmt": message.NewPrinter(message.MatchLanguage("en")).Sprint,
		}).Parse(`
{{if .req.Error}}
Charon build error:

{{.req.Error}}
{{else}}
Charon build complete!

Nix channel: {{.req.RelID}}
Styx commit: {{slice .req.StyxCommit 0 8}}

Elapsed time: {{.req.BuildElapsed | fmt}}
Manifests: {{.req.ManifestStats.Manifests | fmt}}
Chunks: {{.req.ManifestStats.NewChunks | fmt}} new ⁄ {{.req.ManifestStats.TotalChunks | fmt}} total
Bytes: {{.req.ManifestStats.NewUncmpBytes | fmt}} new ⁄ {{.req.ManifestStats.TotalUncmpBytes | fmt}} total
Compressed bytes: {{.req.ManifestStats.NewCmpBytes | fmt}} new

Packages:
{{range .diff -}}
{{.}}
{{end}}
{{end}}
`)), args)
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

func makeDiff(a, b []string) []string {
	as := splitNames(a)
	bs := splitNames(b)
	var out []string
	for bk, bv := range bs {
		if av, ok := as[bk]; ok {
			if av != bv {
				out = append(out, fmt.Sprintf("%s:  %s  ⮞  %s", bk, av, bv))
			}
		} else {
			if bv != "" {
				out = append(out, fmt.Sprintf("%s:  ∅  ⮞  %s", bk, bv))
			}
		}
	}
	for ak, av := range as {
		if _, ok := bs[ak]; !ok {
			if av != "" {
				out = append(out, fmt.Sprintf("%s:  %s  ⮞  ∅", ak, av))
			}
		}
	}
	slices.Sort(out)
	return out
}

func splitNames(ns []string) map[string]string {
	out := make(map[string]string)
	for _, n := range ns {
		m := _splitRe.FindStringSubmatch(n)
		if len(m) == 0 {
			continue
		}
		// ignore dups, just overwrite
		out[m[1]] = m[2]
	}
	return out
}

var _splitRe = regexp.MustCompile(`^(.+?)-([0-9].*?)(-man|-bin|-lib|-libgcc|-dev|-doc|-info|-getent|-xz|-zstd|-modules|-modules-shrunk|-drivers|--p11kit|-xxd)?$`)
