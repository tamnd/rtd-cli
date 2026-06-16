package rtd

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strings"

	"github.com/tamnd/any-cli/kit"
	"github.com/tamnd/any-cli/kit/errs"
)

// domain.go exposes rtd as a kit Domain: a driver that a multi-domain
// host (ant) enables with a single blank import,
//
//	import _ "github.com/tamnd/rtd-cli/rtd"
//
// The init below registers it; the host then dereferences rtd:// URIs by
// routing to the operations Register installs. The same Domain also builds the
// standalone rtd binary (see cli.NewApp), so the binary and a host share one
// source of truth.
func init() { kit.Register(Domain{}) }

// Domain is the rtd driver. It carries no state; the per-run client is
// built by the factory Register hands kit.
type Domain struct{}

// Info describes the scheme, the hostnames a pasted link is matched against, and
// the identity reused for the binary's help and version.
func (Domain) Info() kit.DomainInfo {
	return kit.DomainInfo{
		Scheme: "rtd",
		Hosts:  []string{Host},
		Identity: kit.Identity{
			Binary: "rtd",
			Short:  "Browse and search ReadTheDocs documentation projects",
			Long: `rtd reads ReadTheDocs data over the public API v3, shapes it into clean records,
and prints output that pipes into the rest of your tools.

Most commands require an API token from https://readthedocs.org/accounts/tokens/.
Set it via the RTD_TOKEN environment variable.

Quick start:
  rtd search django                  search docs projects
  rtd project django                 project details
  rtd list --limit 20                list your own projects (token required)`,
			Site: Host,
			Repo: "https://github.com/tamnd/rtd-cli",
		},
	}
}

// Register installs the client factory and every operation onto app.
func (Domain) Register(app *kit.App) {
	app.SetClient(newClient)

	kit.Handle(app, kit.OpMeta{
		Name:    "search",
		Group:   "discover",
		Summary: "Search ReadTheDocs projects and pages",
		Args:    []kit.Arg{{Name: "query", Help: "search query"}},
	}, searchDocs)

	kit.Handle(app, kit.OpMeta{
		Name:     "project",
		Group:    "read",
		Single:   true,
		Resolver: true,
		URIType:  "project",
		Summary:  "Show details of a ReadTheDocs project",
		Args:     []kit.Arg{{Name: "slug", Help: "project slug"}},
	}, getProject)

	kit.Handle(app, kit.OpMeta{
		Name:    "list",
		Group:   "read",
		Summary: "List your ReadTheDocs projects (token required)",
		Args:    []kit.Arg{{Name: "limit", Help: "max results", Optional: true}},
	}, listProjects)

	// Fallback scaffold op: raw page navigation
	kit.Handle(app, kit.OpMeta{
		Name:     "page",
		Group:    "read",
		Single:   true,
		Resolver: true,
		URIType:  "page",
		Summary:  "Fetch a raw page by path",
		Args:     []kit.Arg{{Name: "ref", Help: "page path or URL"}},
	}, getPage)
}

// newClient builds the client from the host-resolved config.
func newClient(_ context.Context, cfg kit.Config) (any, error) {
	c := NewClient()
	if cfg.UserAgent != "" {
		c.UserAgent = cfg.UserAgent
	}
	if cfg.Rate > 0 {
		c.Rate = cfg.Rate
	}
	if cfg.Retries > 0 {
		c.Retries = cfg.Retries
	}
	if cfg.Timeout > 0 {
		c.HTTP.Timeout = cfg.Timeout
	}
	if tok := os.Getenv("RTD_TOKEN"); tok != "" {
		c.Token = tok
	}
	return c, nil
}

// --- input structs ---

type searchInput struct {
	Query  string  `kit:"arg" help:"search query"`
	Limit  int     `kit:"flag,inherit" help:"max results" default:"25"`
	Client *Client `kit:"inject"`
}

type projectInput struct {
	Slug   string  `kit:"arg" help:"project slug"`
	Client *Client `kit:"inject"`
}

type listInput struct {
	Limit  int     `kit:"flag,inherit" help:"max results" default:"25"`
	Client *Client `kit:"inject"`
}

type pageRef struct {
	Ref    string  `kit:"arg" help:"page path or URL"`
	Client *Client `kit:"inject"`
}

// --- handlers ---

func searchDocs(ctx context.Context, in searchInput, emit func(SearchResult) error) error {
	results, err := in.Client.Search(ctx, in.Query, in.Limit)
	if err != nil {
		return mapErr(err)
	}
	for _, r := range results {
		if err := emit(r); err != nil {
			return err
		}
	}
	return nil
}

func getProject(ctx context.Context, in projectInput, emit func(*Project) error) error {
	p, err := in.Client.GetProject(ctx, in.Slug)
	if err != nil {
		return mapErr(err)
	}
	return emit(p)
}

func listProjects(ctx context.Context, in listInput, emit func(Project) error) error {
	projects, err := in.Client.ListProjects(ctx, in.Limit)
	if err != nil {
		return mapErr(err)
	}
	for _, p := range projects {
		if err := emit(p); err != nil {
			return err
		}
	}
	return nil
}

func getPage(ctx context.Context, in pageRef, emit func(*Page) error) error {
	p, err := in.Client.GetPage(ctx, pagePath(in.Ref))
	if err != nil {
		return mapErr(err)
	}
	return emit(p)
}

// --- Resolver ---

// Classify turns any accepted input into the canonical (type, id).
func (Domain) Classify(input string) (uriType, id string, err error) {
	id = pagePath(input)
	if id == "" {
		return "", "", errs.Usage("unrecognized rtd reference: %q", input)
	}
	return "page", id, nil
}

// Locate returns the live https URL for a (type, id).
func (Domain) Locate(uriType, id string) (string, error) {
	switch uriType {
	case "page":
		return BaseURL + "/" + strings.Trim(id, "/"), nil
	case "project":
		return BaseURL + "/projects/" + strings.Trim(id, "/") + "/", nil
	}
	return "", errs.Usage("rtd has no resource type %q", uriType)
}

// --- helpers ---

// pagePath turns any accepted input into the canonical page id.
func pagePath(input string) string {
	input = strings.TrimSpace(input)
	if u, err := url.Parse(input); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
		return strings.Trim(u.Path, "/")
	}
	return strings.Trim(input, "/")
}

// mapErr translates library errors into kit error kinds.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrNotFound) {
		return errs.NotFound("%s", err.Error())
	}
	if errors.Is(err, ErrUnauthorized) {
		return errs.Usage("%s", err.Error())
	}
	return err
}
