package restResources

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	stringset "github.com/krateoplatformops/rest-dynamic-controller/internal/text"
	restclient "github.com/krateoplatformops/rest-dynamic-controller/internal/tools/client"
	getter "github.com/krateoplatformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/krateoplatformops/unstructured-runtime/pkg/logging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// mockAsyncClient implements restclient.UnstructuredClientInterface, returning a canned sequence of poll
// responses (or an error) from Call. Only Call is exercised by driveAsync's poll loop.
type mockAsyncClient struct {
	pollBodies []map[string]interface{}
	idx        int
	callErr    error
}

func (m *mockAsyncClient) Call(ctx context.Context, cli *http.Client, path string, conf *restclient.RequestConfiguration) (restclient.Response, error) {
	if m.callErr != nil {
		return restclient.Response{}, m.callErr
	}
	if m.idx >= len(m.pollBodies) {
		return restclient.Response{}, fmt.Errorf("unexpected extra poll call")
	}
	b := m.pollBodies[m.idx]
	m.idx++
	return restclient.Response{ResponseBody: b}, nil
}

func (m *mockAsyncClient) ValidateRequest(httpMethod, path string, parameters, query, headers, cookies map[string]string) error {
	return nil
}
func (m *mockAsyncClient) RequestedBody(httpMethod, path string) (stringset.StringSet, error) {
	return nil, nil
}
func (m *mockAsyncClient) RequestedParams(httpMethod, path string) (stringset.StringSet, stringset.StringSet, stringset.StringSet, stringset.StringSet, error) {
	return nil, nil, nil, nil, nil
}
func (m *mockAsyncClient) FindBy(ctx context.Context, cli *http.Client, path string, conf *restclient.RequestConfiguration, findByAction *getter.VerbsDescription) (restclient.Response, error) {
	return restclient.Response{}, nil
}

func TestAsyncConfigForAction(t *testing.T) {
	verbs := []getter.VerbsDescription{
		{Action: "get", Method: "GET", Path: "/x"},
		{Action: "create", Method: "POST", Path: "/x", Async: &getter.AsyncConfig{}},
	}
	assert.NotNil(t, asyncConfigForAction(verbs, "create"))
	assert.NotNil(t, asyncConfigForAction(verbs, "CREATE"), "action match is case-insensitive")
	assert.Nil(t, asyncConfigForAction(verbs, "get"), "verb without async returns nil")
	assert.Nil(t, asyncConfigForAction(verbs, "delete"), "missing verb returns nil")
}

func TestExtractPollStatus(t *testing.T) {
	body := map[string]interface{}{"status": "succeeded", "nested": map[string]interface{}{"s": "running"}}
	s, err := extractPollStatus(body, "status")
	require.NoError(t, err)
	assert.Equal(t, "succeeded", s)

	s, err = extractPollStatus(body, "nested.s")
	require.NoError(t, err)
	assert.Equal(t, "running", s)

	_, err = extractPollStatus(body, "missing")
	assert.Error(t, err)
}

func asyncCfg(postGet bool) *getter.AsyncConfig {
	return &getter.AsyncConfig{
		OperationRef: getter.OperationRef{In: "body", Path: "id"},
		Poll: getter.PollConfig{
			Path:          "/{organization}/_apis/operations/{operationId}",
			StatusPath:    "status",
			SuccessValues: []string{"succeeded"},
			FailureValues: []string{"failed", "cancelled"},
			MaxAttempts:   5,
		},
		PostGet: postGet,
	}
}

func TestDriveAsync_PollsToTerminalSuccess(t *testing.T) {
	cli := &mockAsyncClient{pollBodies: []map[string]interface{}{{"status": "succeeded"}}}
	trigger := restclient.Response{ResponseBody: map[string]interface{}{"id": "op-123", "status": "queued"}}
	triggerReq := &restclient.RequestConfiguration{Parameters: map[string]string{"organization": "acme"}}

	resp, err := driveAsync(context.Background(), cli, nil, &unstructured.Unstructured{}, asyncCfg(false), trigger, triggerReq, logging.NewNopLogger())
	require.NoError(t, err)
	// Without postGet, the (trigger) response is returned for status population.
	assert.Equal(t, trigger.ResponseBody, resp.ResponseBody)
	assert.Equal(t, 1, cli.idx, "polled once to reach terminal success")
}

func TestDriveAsync_TerminalFailure(t *testing.T) {
	cli := &mockAsyncClient{pollBodies: []map[string]interface{}{{"status": "failed"}}}
	trigger := restclient.Response{ResponseBody: map[string]interface{}{"id": "op-9"}}

	_, err := driveAsync(context.Background(), cli, nil, &unstructured.Unstructured{}, asyncCfg(false), trigger, nil, logging.NewNopLogger())
	require.Error(t, err)
	assert.ErrorContains(t, err, "terminal failure")
}

func TestDriveAsync_MissingHandle(t *testing.T) {
	cli := &mockAsyncClient{}
	trigger := restclient.Response{ResponseBody: map[string]interface{}{"no_id_here": true}}

	_, err := driveAsync(context.Background(), cli, nil, &unstructured.Unstructured{}, asyncCfg(false), trigger, nil, logging.NewNopLogger())
	require.Error(t, err)
	assert.ErrorContains(t, err, "operation handle")
}
