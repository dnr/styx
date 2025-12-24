package resolve

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/dnr/styx/common"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/nix-community/go-nix/pkg/storepath"
)

type Result struct {
	Url           string
	StorePathName string
	Etag          string
}

type handler struct {
	name string
	re   *regexp.Regexp
	f    func(m []string, commit string) string
}

var handlers = []handler{
	{
		name: "github",
		re:   regexp.MustCompile(`^github\.com/([^/]+)/([^/]+)/archive/(.+)\.(tar\.gz)$`),
		f: func(m []string, commit string) string {
			return fmt.Sprintf("https://github.com/%s/%s/archive/%s.%s", m[1], m[2], commit, m[4])
		},
	},
	{
		name: "gitlab",
		re:   regexp.MustCompile(`^gitlab\.com/([^/]+)/([^/]+)/-/archive/([^/]+)/(.+)\.(tar\.gz)$`),
		f: func(m []string, commit string) string {
			return fmt.Sprintf("https://gitlab.com/%s/%s/-/archive/%s/%s-%s.%s", m[1], m[2], commit, m[2], commit, m[5])
		},
	},
	{
		name: "bitbucket",
		re:   regexp.MustCompile(`^bitbucket\.org/([^/]+)/([^/]+)/get/(.+)\.(tar\.gz)$`),
		f: func(m []string, commit string) string {
			return fmt.Sprintf("https://bitbucket.org/%s/%s/get/%s.%s", m[1], m[2], commit, m[4])
		},
	},
	{
		name: "codeberg",
		re:   regexp.MustCompile(`^codeberg\.org/([^/]+)/([^/]+)/archive/(.+)\.(tar\.gz)$`),
		f: func(m []string, commit string) string {
			return fmt.Sprintf("https://codeberg.org/%s/%s/archive/%s.%s", m[1], m[2], commit, m[4])
		},
	},
	{
		name: "sourcehut",
		re:   regexp.MustCompile(`^git\.sr\.ht/~([^/]+)/([^/]+)/archive/(.+)\.(tar\.gz)$`),
		f: func(m []string, commit string) string {
			return fmt.Sprintf("https://git.sr.ht/~%s/%s/archive/%s.%s", m[1], m[2], commit, m[4])
		},
	},
}

// resolveGitRef returns the 40-char SHA for a given ref
func resolveGitRef(ctx context.Context, gitURL, ref string) (string, error) {
	if isGitHash(ref) {
		return ref, nil // already is a hash
	}
	rem := git.NewRemote(memory.NewStorage(), &config.RemoteConfig{
		Name: "origin",
		URLs: []string{gitURL},
	})
	refs, err := rem.ListContext(ctx, &git.ListOptions{})
	if err != nil {
		return "", err
	}
	for _, r := range refs {
		n := r.Name().String()
		if n == "refs/heads/"+ref || n == "refs/tags/"+ref || n == ref {
			return r.Hash().String(), nil
		}
	}
	return "", fmt.Errorf("ref %s not found", ref)
}

func ResolveUrl(ctx context.Context, input string) (Result, error) {
	u, err := url.Parse(input)
	if err != nil {
		return Result{}, err
	}
	hostPath := u.Host + u.Path

	log.Println("resolving url", input)

	// try forges
	for _, h := range handlers {
		if m := h.re.FindStringSubmatch(hostPath); m != nil {
			owner, repo, gitref := m[1], m[2], m[3]

			gitURL := fmt.Sprintf("https://%s/%s/%s.git", u.Host, owner, repo)
			if h.name == "sourcehut" {
				gitURL = fmt.Sprintf("https://git.sr.ht/~%s/%s", owner, repo)
			}

			commit, err := resolveGitRef(ctx, gitURL, gitref)
			if err == nil {
				// FIXME: pass this back through h.re to ensure that it's idempotent
				resolved := h.f(m, commit)
				log.Println("using", h.name, "url pattern ->", resolved)
				// FIXME: add commit date to this so they sort in the right order in the catalog
				name := sanitizeStorePathName(fmt.Sprintf("%s-%s-%s", h.name, owner, repo))
				// note that the url we return here may still redirect. that's okay, as long as
				// we have a git commit in the url we should be confident that it's at least
				// semantically identical (produces the same nar hash). so we can use the
				// commit as a fake etag and not worry about the actual etag.
				fakeEtag := fmt.Sprintf(`G/"%s"`, commit)
				return Result{
					Url:           resolved,
					StorePathName: name,
					Etag:          fakeEtag,
				}, nil
			}
		}
	}

	// follow http redirects (for nix channels, releases, etc.)
	// TODO: actually do lockable tarball protocol here
	log.Println("doing head request on", input)
	res, err := common.RetryHttpRequest(ctx, http.MethodHead, input, "", nil)
	if err != nil {
		return Result{}, err
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()

	resolved := res.Request.URL.String()
	spName := getSpNameFromUrl(resolved)
	// if we don't have an etag then we can't write an etag cache entry, but we can still
	// ingest it and return a manifest.
	etag := res.Header.Get("Etag")
	log.Println("using redirect ->", resolved, "has etag", etag != "")
	return Result{
		Url:           resolved,
		StorePathName: spName,
		Etag:          etag,
	}, nil
}

var reNixExprs = regexp.MustCompile(`^https://releases\.nixos\.org/.*/(nix(os|pkgs)-\d\d\.\d\d(\.|pre)\d+).[a-z0-9]+/nixexprs\.tar`)

func getSpNameFromUrl(url string) string {
	// hack: tweak name, e.g. we want
	//   https://releases.nixos.org/nixos/25.11/nixos-25.11.1056.d9bc5c7dceb3/nixexprs.tar.xz
	// to turn into "nixexprs-nixos-25.11.1056" for better diffing
	if m := reNixExprs.FindStringSubmatch(url); m != nil {
		return "nixexprs-" + m[1]
	}

	name := path.Base(url)
	name = strings.TrimSuffix(name, ".gz")
	name = strings.TrimSuffix(name, ".xz")
	name = strings.TrimSuffix(name, ".tar")
	return name
}

func sanitizeStorePathName(s string) string {
	return strings.Join(storepath.NameRe.FindAllString(s, -1), "_")
}

var reGitHash = regexp.MustCompile(`[0-9a-f]{40}([0-9a-f]{24})?`)

func isGitHash(s string) bool {
	return reGitHash.FindString(s) == s
}
