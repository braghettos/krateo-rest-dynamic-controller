package restResources

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/krateoplatformops/rest-dynamic-controller/internal/tools/async"
	restclient "github.com/krateoplatformops/rest-dynamic-controller/internal/tools/client"
	"github.com/krateoplatformops/rest-dynamic-controller/internal/tools/client/apiaction"
	"github.com/krateoplatformops/rest-dynamic-controller/internal/tools/client/builder"
	getter "github.com/krateoplatformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/krateoplatformops/rest-dynamic-controller/internal/tools/pathparsing"
	"github.com/krateoplatformops/unstructured-runtime/pkg/logging"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// asyncConfigForAction returns the Async config declared on the verb matching the given action, or nil.
func asyncConfigForAction(verbs []getter.VerbsDescription, action string) *getter.AsyncConfig {
	for _, v := range verbs {
		if strings.EqualFold(v.Action, action) {
			return v.Async
		}
	}
	return nil
}

// extractPollStatus reads the status value from a poll response body at the given JSONPath.
func extractPollStatus(body map[string]interface{}, statusPath string) (string, error) {
	segs, err := pathparsing.ParsePath(statusPath)
	if err != nil || len(segs) == 0 {
		return "", fmt.Errorf("invalid statusPath %q", statusPath)
	}
	v, found, err := unstructured.NestedFieldNoCopy(body, segs...)
	if err != nil || !found {
		return "", fmt.Errorf("statusPath %q not found in poll response", statusPath)
	}
	return fmt.Sprintf("%v", v), nil
}

// driveAsync drives a long-running operation to completion (design #241, Model A blocking-poll): it
// extracts the operation handle from the trigger response, polls the operations endpoint until it reaches a
// terminal status, and — when PostGet is set — re-reads the resource so status is populated from its real
// state rather than the (transient) operation reference the trigger returned. It returns the response to
// use for subsequent status population.
//
// The poll reuses the trigger call's resolved path parameters (e.g. {organization}) plus the extracted
// {operationId}. The poll path must be declared in the OAS document (it goes through the same client Call).
func driveAsync(ctx context.Context, cli restclient.UnstructuredClientInterface, clientInfo *getter.Info, mg *unstructured.Unstructured, cfg *getter.AsyncConfig, triggerResp restclient.Response, triggerReq *restclient.RequestConfiguration, log logging.Logger) (restclient.Response, error) {
	body, ok := triggerResp.ResponseBody.(map[string]interface{})
	if !ok {
		return triggerResp, fmt.Errorf("async trigger response body is not an object")
	}
	opID, err := async.ExtractOperationHandle(ctx, body, cfg.OperationRef)
	if err != nil {
		return triggerResp, fmt.Errorf("extracting async operation handle: %w", err)
	}
	log.Debug("Async operation started, polling to completion", "operationId", opID, "pollPath", cfg.Poll.Path)

	poll := func(ctx context.Context, id string) (string, error) {
		params := map[string]string{"operationId": id}
		if triggerReq != nil {
			for k, v := range triggerReq.Parameters {
				if _, exists := params[k]; !exists {
					params[k] = v
				}
			}
		}
		pollReq := &restclient.RequestConfiguration{
			Method:     "GET",
			Parameters: params,
			Query:      map[string]string{},
			Headers:    map[string]string{},
			Cookies:    map[string]string{},
		}
		resp, perr := cli.Call(ctx, &http.Client{}, cfg.Poll.Path, pollReq)
		if perr != nil {
			return "", perr
		}
		pb, ok := resp.ResponseBody.(map[string]interface{})
		if !ok {
			return "", fmt.Errorf("poll response body is not an object")
		}
		return extractPollStatus(pb, cfg.Poll.StatusPath)
	}

	if err := async.PollUntilTerminal(ctx, cfg.Poll, opID, poll); err != nil {
		return triggerResp, err
	}
	log.Debug("Async operation reached terminal success", "operationId", opID)

	if !cfg.PostGet {
		return triggerResp, nil
	}

	// Re-read the resource so status reflects its real state.
	getCall, getInfo, gerr := builder.APICallBuilder(cli, clientInfo, apiaction.Get)
	if gerr != nil {
		return triggerResp, fmt.Errorf("async postGet: building get call: %w", gerr)
	}
	if getCall == nil || getInfo == nil {
		return triggerResp, fmt.Errorf("async postGet requested but no get action is defined")
	}
	getReq := builder.BuildCallConfig(getInfo, mg, clientInfo.ConfigurationSpec)
	getResp, gerr := getCall(ctx, &http.Client{}, getInfo.Path, getReq)
	if gerr != nil {
		return triggerResp, fmt.Errorf("async postGet: get call: %w", gerr)
	}
	return getResp, nil
}
