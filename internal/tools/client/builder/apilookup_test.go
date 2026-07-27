package builder

import (
	"context"
	"net/http"
	"testing"

	restclient "github.com/krateoplatformops/rest-dynamic-controller/internal/tools/client"
	getter "github.com/krateoplatformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/krateoplatformops/rest-dynamic-controller/internal/text"
)

// scriptedLookupClient is a purpose-built UnstructuredClientInterface fake for apiLookup tests: it records
// the RequestConfiguration Call receives and returns a scripted Response/error, without needing an actual
// OAS document or HTTP server.
type scriptedLookupClient struct {
	params, query text.StringSet
	response      restclient.Response
	err           error

	gotConf *restclient.RequestConfiguration
}

func (s *scriptedLookupClient) RequestedParams(method, path string) (text.StringSet, text.StringSet, text.StringSet, text.StringSet, error) {
	return s.params, s.query, nil, nil, nil
}
func (s *scriptedLookupClient) RequestedBody(method, path string) (text.StringSet, error) {
	return nil, nil
}
func (s *scriptedLookupClient) ValidateRequest(method, path string, params, query, headers, cookies map[string]string) error {
	return nil
}
func (s *scriptedLookupClient) Call(ctx context.Context, cli *http.Client, path string, conf *restclient.RequestConfiguration) (restclient.Response, error) {
	s.gotConf = conf
	return s.response, s.err
}
func (s *scriptedLookupClient) FindBy(ctx context.Context, cli *http.Client, path string, conf *restclient.RequestConfiguration, findByAction *getter.VerbsDescription) (restclient.Response, error) {
	return s.response, s.err
}

func findbyInfo() *getter.Info {
	return &getter.Info{
		Resource: getter.Resource{
			VerbsDescription: []getter.VerbsDescription{
				{Action: "findby", Method: "GET", Path: "/teams"},
			},
		},
	}
}

func TestResolveAPILookup_QueryParamAndResponsePath(t *testing.T) {
	cli := &scriptedLookupClient{
		query: text.NewStringSet("slug"),
		response: restclient.Response{
			ResponseBody: []interface{}{
				map[string]interface{}{"id": "team-42", "slug": "my-team-slug"},
			},
		},
	}
	r := &getter.APILookupResolver{Action: "findby", RequestParam: "slug", ResponsePath: "id"}

	val, err := ResolveAPILookup(context.Background(), cli, findbyInfo(), r, "my-team-slug")
	if err != nil {
		t.Fatalf("ResolveAPILookup: %v", err)
	}
	if val != "team-42" {
		t.Fatalf("expected resolved value %q, got %v", "team-42", val)
	}
	if cli.gotConf.Query["slug"] != "my-team-slug" {
		t.Fatalf("expected the alias to be injected into the slug query param, got %v", cli.gotConf.Query)
	}
}

func TestResolveAPILookup_PathParam(t *testing.T) {
	cli := &scriptedLookupClient{
		params: text.NewStringSet("slug"),
		response: restclient.Response{
			ResponseBody: map[string]interface{}{"id": "team-42"},
		},
	}
	r := &getter.APILookupResolver{Action: "findby", RequestParam: "slug", ResponsePath: "id"}

	val, err := ResolveAPILookup(context.Background(), cli, findbyInfo(), r, "my-team-slug")
	if err != nil {
		t.Fatalf("ResolveAPILookup: %v", err)
	}
	if val != "team-42" {
		t.Fatalf("expected resolved value %q, got %v", "team-42", val)
	}
	if cli.gotConf.Parameters["slug"] != "my-team-slug" {
		t.Fatalf("expected the alias to be injected into the slug path param, got %v", cli.gotConf.Parameters)
	}
}

func TestResolveAPILookup_UnrecognizedRequestParamIsError(t *testing.T) {
	cli := &scriptedLookupClient{query: text.NewStringSet("other")}
	r := &getter.APILookupResolver{Action: "findby", RequestParam: "slug", ResponsePath: "id"}

	_, err := ResolveAPILookup(context.Background(), cli, findbyInfo(), r, "my-team-slug")
	if err == nil {
		t.Fatal("expected an error when requestParam does not match any recognized path/query parameter")
	}
}

func TestResolveAPILookup_NoMatchIsError(t *testing.T) {
	cli := &scriptedLookupClient{
		query:    text.NewStringSet("slug"),
		response: restclient.Response{ResponseBody: []interface{}{}},
	}
	r := &getter.APILookupResolver{Action: "findby", RequestParam: "slug", ResponsePath: "id"}

	_, err := ResolveAPILookup(context.Background(), cli, findbyInfo(), r, "my-team-slug")
	if err == nil {
		t.Fatal("expected an empty lookup result to be an error, not a nil/zero value")
	}
}

func TestResolveAPILookup_MissingResponsePathIsError(t *testing.T) {
	cli := &scriptedLookupClient{
		query:    text.NewStringSet("slug"),
		response: restclient.Response{ResponseBody: map[string]interface{}{"name": "my-team"}},
	}
	r := &getter.APILookupResolver{Action: "findby", RequestParam: "slug", ResponsePath: "id"}

	_, err := ResolveAPILookup(context.Background(), cli, findbyInfo(), r, "my-team-slug")
	if err == nil {
		t.Fatal("expected a missing responsePath to be an error")
	}
}

func TestResolveAPILookup_ActionNotFoundIsError(t *testing.T) {
	cli := &scriptedLookupClient{}
	r := &getter.APILookupResolver{Action: "does-not-exist", RequestParam: "slug", ResponsePath: "id"}

	_, err := ResolveAPILookup(context.Background(), cli, findbyInfo(), r, "my-team-slug")
	if err == nil {
		t.Fatal("expected an error when the action does not exist in this RestDefinition")
	}
}

func TestResolveAPILookup_PaginatedActionIsError(t *testing.T) {
	cli := &scriptedLookupClient{query: text.NewStringSet("slug")}
	info := &getter.Info{
		Resource: getter.Resource{
			VerbsDescription: []getter.VerbsDescription{
				{Action: "findby", Method: "GET", Path: "/teams", Pagination: &getter.Pagination{}},
			},
		},
	}
	r := &getter.APILookupResolver{Action: "findby", RequestParam: "slug", ResponsePath: "id"}

	_, err := ResolveAPILookup(context.Background(), cli, info, r, "my-team-slug")
	if err == nil {
		t.Fatal("expected a paginated lookup action to be a hard error (not yet supported)")
	}
}

func TestResolveAPILookup_CallErrorPropagates(t *testing.T) {
	cli := &scriptedLookupClient{query: text.NewStringSet("slug"), err: errNotFoundStub{}}
	r := &getter.APILookupResolver{Action: "findby", RequestParam: "slug", ResponsePath: "id"}

	_, err := ResolveAPILookup(context.Background(), cli, findbyInfo(), r, "my-team-slug")
	if err == nil {
		t.Fatal("expected the underlying Call error to propagate")
	}
}

type errNotFoundStub struct{}

func (errNotFoundStub) Error() string { return "not found" }
