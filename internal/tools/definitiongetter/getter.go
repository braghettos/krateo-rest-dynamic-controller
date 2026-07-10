package getter

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/krateoplatformops/rest-dynamic-controller/internal/tools/auth"
	"github.com/krateoplatformops/unstructured-runtime/pkg/pluralizer"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

// Pagination defines the pagination strategy for a "findby" action.
// Currently, only 'continuationToken' is supported.
type Pagination struct {
	// Type specifies the pagination strategy. Currently, only 'continuationToken' is supported.
	Type string `json:"type"`
	// Configuration for 'continuationToken' pagination. Required if type is 'continuationToken'.
	ContinuationToken *ContinuationTokenConfig `json:"continuationToken,omitempty"`
	// (Future) Configuration for 'pageNumber' pagination.
	//PageNumber *PageNumberConfig `json:"pageNumber,omitempty"`
	// (Future) Configuration for 'offset' pagination.
	//Offset *OffsetConfig `json:"offset,omitempty"`
}

// ContinuationTokenConfig holds the specific settings for token-based pagination.
type ContinuationTokenConfig struct {
	// Request: defines how to include the pagination token in the API request.
	Request ContinuationTokenRequest `json:"request"`
	// Response: defines how to extract the pagination token from the API response.
	Response ContinuationTokenResponse `json:"response"`
}

// ContinuationTokenRequest defines how to include the pagination token in the API request.
type ContinuationTokenRequest struct {
	// Where the token is located: "query", "header" or "body". Currently, only "query" is supported.
	TokenIn string `json:"tokenIn"`
	// The path or name of the query parameter, header, or body field.
	// For query parameters and headers, this is simply the name.
	// For body fields, this should be a JSON path.
	TokenPath string `json:"tokenPath"`
}

// ContinuationTokenResponse defines how to extract the pagination token from the API response.
type ContinuationTokenResponse struct {
	// Where the token is located: "header" or "body". Currently, only "header" is supported.
	TokenIn string `json:"tokenIn"`
	// The path or name of the header or body field.
	// For headers, this is simply the name.
	// For body fields, this should be a JSON path.
	TokenPath string `json:"tokenPath"`
}

// PageNumberConfig is a placeholder for future page number pagination settings.
//type PageNumberConfig struct{}

// OffsetConfig is a placeholder for future offset pagination settings.
//type OffsetConfig struct{}

// RequestFieldMappingItem defines a single mapping from a path parameter, query parameter or body field
// to a field in the Custom Resource.
type RequestFieldMappingItem struct {
	// InPath defines the name of the path parameter to be mapped.
	// Only one of 'inPath', 'inQuery' or 'inBody' can be set.
	InPath string `json:"inPath,omitempty"`
	// InQuery defines the name of the query parameter to be mapped.
	// Only one of 'inPath', 'inQuery' or 'inBody' can be set.
	InQuery string `json:"inQuery,omitempty"`
	// InBody defines the name of the body parameter to be mapped.
	// Only one of 'inPath', 'inQuery' or 'inBody' can be set.
	InBody string `json:"inBody,omitempty"`
	// InCustomResource defines the JSONPath to the field within the Custom Resource that holds the value.
	// For example: 'spec.name' or 'status.metadata.id'.
	InCustomResource string `json:"inCustomResource"`
}

// FieldMappingItem is the runtime mirror of the RestDefinition FieldMappingItem (unified request/response
// mapping). Exactly one API-side anchor is set: inPath/inQuery/inBody select a request parameter,
// inResponse selects a response body field to be normalized into the CR-domain shape.
type FieldMappingItem struct {
	InPath           string        `json:"inPath,omitempty"`
	InQuery          string        `json:"inQuery,omitempty"`
	InBody           string        `json:"inBody,omitempty"`
	InResponse       string        `json:"inResponse,omitempty"`
	InCustomResource string        `json:"inCustomResource,omitempty"`
	ValueMapping     *ValueMapping `json:"valueMapping,omitempty"`
	// DefaultIfAbsent (response entries only) is the JSON value injected at the CR-domain destination when
	// the API omits the inResponse source field entirely.
	DefaultIfAbsent json.RawMessage `json:"defaultIfAbsent,omitempty"`
}

// ValueMapping is the runtime mirror of a value transform. Tier-1 'alias' is applied by the fieldmapping
// package; Tier-2 'jq' is carried here but only executed once the jq engine lands.
type ValueMapping struct {
	Type    string       `json:"type"`
	Aliases []ValueAlias `json:"aliases,omitempty"`
	JQ      *JQProgram   `json:"jq,omitempty"`
}

// ValueAlias is a single bidirectional CR<->API value pair.
type ValueAlias struct {
	CustomResourceValue string `json:"customResourceValue"`
	APIValue            string `json:"apiValue"`
}

// JQProgram mirrors a gojq program supplied inline or as a module reference.
type JQProgram struct {
	Inline     string `json:"inline,omitempty"`
	Ref        string `json:"ref,omitempty"`
	Entrypoint string `json:"entrypoint,omitempty"`
}

type VerbsDescription struct {
	// Name of the action to perform when this api is called
	Action string `json:"action"`
	// Method: the http method to use [GET, POST, PUT, DELETE, PATCH]
	Method string `json:"method"`
	// Path: the path to the api
	Path string `json:"path"`
	// RequestFieldMapping provides explicit mapping from API parameters (path, query, or body)
	// to fields in the Custom Resource. Deprecated: mirrored for backward compatibility; prefer FieldMapping.
	RequestFieldMapping []RequestFieldMappingItem `json:"requestFieldMapping,omitempty"`
	// FieldMapping is the unified request/response field mapping (see FieldMappingItem). Response-direction
	// entries (inResponse) are applied to the observed body before status population and drift comparison.
	FieldMapping []FieldMappingItem `json:"fieldMapping,omitempty"`
	// RequestTransform / ResponseTransform are whole-document jq programs (executed once the jq engine lands).
	RequestTransform  *JQProgram `json:"requestTransform,omitempty"`
	ResponseTransform *JQProgram `json:"responseTransform,omitempty"`
	// IdentifiersMatchPolicy defines how to match identifiers for the 'findby' action. To be set only for 'findby' actions.
	// If not set, defaults to 'OR'.
	// Possible values are 'AND' or 'OR'.
	// - 'AND': all identifiers must match.
	// - 'OR': at least one identifier must match (the default behavior).
	IdentifiersMatchPolicy string `json:"identifiersMatchPolicy,omitempty"`
	// Pagination defines the pagination strategy for 'findby' actions. To be set only for 'findby' actions.
	// If not set, no pagination will be used.
	Pagination *Pagination `json:"pagination,omitempty"`
	// SuccessCodes lists additional HTTP status codes treated as success for this verb, merged with the
	// OAS-declared 2xx codes.
	SuccessCodes []int `json:"successCodes,omitempty"`
	// Headers are static HTTP headers injected on every request for this verb.
	Headers []HeaderItem `json:"headers,omitempty"`
	// TolerateCodes are HTTP status codes treated as a successful empty response for this verb.
	TolerateCodes []int `json:"tolerateCodes,omitempty"`
}

// HeaderItem is a single static HTTP header injected on every request for a verb.
type HeaderItem struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type Resource struct {
	// Name: the name of the resource to manage
	Kind string `json:"kind"`
	// Identifiers: the list of fields to use as identifiers
	Identifiers []string `json:"identifiers"`
	// AdditionalStatusFields: the list of additional status fields to use
	AdditionalStatusFields []string `json:"additionalStatusFields"`
	// ConfigurationFields: the list of fields to use as configuration fields
	ConfigurationFields []ConfigurationField `json:"configurationFields,omitempty"`
	// VerbsDescription: the list of verbs to use on this resource
	VerbsDescription []VerbsDescription `json:"verbsDescription"`
}

type ConfigurationField struct {
	FromOpenAPI        FromOpenAPI        `json:"fromOpenAPI"`
	FromRestDefinition FromRestDefinition `json:"fromRestDefinition"`
}

type FromOpenAPI struct {
	Name string `json:"name"`
	In   string `json:"in"` // "query", "path", "header", "cookie"
}

type FromRestDefinition struct {
	Action string `json:"action"`
}

type Info struct {
	// URL of the OAS 3.0 JSON file that is being requested.
	URL string `json:"url"`

	// The resource to manage
	Resource Resource `json:"resources,omitempty"`

	// The spec of the configuration resource
	ConfigurationSpec map[string]interface{}

	// SetAuth function, when called, sets the authentication for the request.
	SetAuth func(req *http.Request)
}

type Getter interface {
	Get(un *unstructured.Unstructured) (*Info, error)
}

func Dynamic(cfg *rest.Config, pluralizer pluralizer.PluralizerInterface) (Getter, error) {
	if cfg == nil {
		return nil, fmt.Errorf("rest config is nil")
	}

	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}

	return &dynamicGetter{
		pluralizer:    pluralizer,
		dynamicClient: dyn,
	}, nil
}

var _ Getter = (*dynamicGetter)(nil)

type dynamicGetter struct {
	dynamicClient dynamic.Interface
	pluralizer    pluralizer.PluralizerInterface
}

// Get retrieves the related RestDefinition for the given unstructured object.
// The information is extracted from the RestDefinition and returned as an Info struct.
func (g *dynamicGetter) Get(un *unstructured.Unstructured) (*Info, error) {
	gvr, err := g.pluralizer.GVKtoGVR(un.GroupVersionKind())
	if err != nil {
		return nil, fmt.Errorf("getting GVR for '%v' in namespace: %s", un.GetKind(), un.GetNamespace())
	}

	gvrForDefinitions := schema.GroupVersionResource{
		Group:    "ogen.krateo.io",
		Version:  "v1alpha1",
		Resource: "restdefinitions",
	}

	all, err := g.dynamicClient.Resource(gvrForDefinitions).
		List(context.Background(), metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("getting definitions for '%v' in namespace: %s - %w", gvr.String(), un.GetNamespace(), err)
	}
	if len(all.Items) == 0 {
		return nil, fmt.Errorf("no definitions found for '%v' in namespace: %s", gvr, un.GetNamespace())
	}

	for _, item := range all.Items {
		res, ok, err := unstructured.NestedFieldNoCopy(item.Object, "spec", "resource")
		if !ok {
			return nil, fmt.Errorf("missing spec.resource in definition for '%v' in namespace: %s", gvr, un.GetNamespace())
		}
		if err != nil {
			return nil, err
		}

		group, ok, err := unstructured.NestedString(item.Object, "spec", "resourceGroup")
		if !ok {
			return nil, fmt.Errorf("missing spec.resourceGroup in definition for '%v' in namespace: %s", gvr, un.GetNamespace())
		}
		if err != nil {
			return nil, err
		}

		kind, ok, err := unstructured.NestedString(item.Object, "spec", "resource", "kind")
		if !ok {
			return nil, fmt.Errorf("missing kind in definition for '%v' in namespace: %s", gvr, un.GetNamespace())
		}
		if err != nil {
			return nil, err
		}
		if kind != un.GetKind() {
			continue
		}

		oasPath, ok, err := unstructured.NestedString(item.Object, "spec", "oasPath")
		if !ok {
			return nil, fmt.Errorf("missing spec.oasPath in definition for '%v' in namespace: %s", gvr, un.GetNamespace())
		}
		if err != nil {
			return nil, err
		}

		if group == gvr.Group {
			// Convert the map to JSON
			jsonData, err := json.Marshal(res)
			if err != nil {
				return nil, err
			}
			// Convert the JSON to a struct
			var resource Resource
			err = json.Unmarshal(jsonData, &resource)
			if err != nil {
				return nil, err
			}

			info := &Info{
				URL:      oasPath,
				Resource: resource,
			}

			//log.Printf("Found definition for '%v' in namespace: %s", gvr, un.GetNamespace())
			// print info as json for debugging
			//infoJson, _ := json.MarshalIndent(info, "", "  ")
			//log.Printf("Definition info: %s", string(infoJson))

			err = g.processConfigurationRef(un, info)
			if err != nil {
				return nil, err
			}

			return info, nil
		}
	}
	return nil, fmt.Errorf("no definitions found for '%v' in namespace: %s", gvr, un.GetNamespace())
}

// processConfigurationRef processes the configuration reference for the given unstructured object.
// It retrieves the configuration spec and authentication methods from the Configuration CR.
// It returns an error if the configuration reference is not valid or if the retrieval fails.
func (g *dynamicGetter) processConfigurationRef(un *unstructured.Unstructured, info *Info) error {
	configRef, ok, err := unstructured.NestedStringMap(un.Object, "spec", "configurationRef")
	if err != nil {
		return fmt.Errorf("getting spec.configurationRef for resource of kind '%v' in namespace: %s", un.GetKind(), un.GetNamespace())
	}
	if !ok {
		return nil // No auth or configuration defined
	}

	// The default namespace used to search the Configuration CR is the same namespace as the unstructured object
	namespace := un.GetNamespace()
	if val, ok := configRef["namespace"]; ok { // if the namespace is specified in the configRef field, use it to search the Configuration CR
		namespace = val
	}

	gvk := un.GroupVersionKind()
	gvk.Kind = fmt.Sprintf("%sConfiguration", gvk.Kind) // e.g., "WorkflowConfiguration"

	gvr, err := g.pluralizer.GVKtoGVR(gvk)
	if err != nil {
		return err
	}

	config, err := g.dynamicClient.Resource(gvr).
		Namespace(namespace).
		Get(context.Background(), configRef["name"], metav1.GetOptions{})
	if err != nil {
		return err
	}

	configSpec, ok, err := unstructured.NestedMap(config.Object, "spec", "configuration")
	if err != nil {
		return err
	}
	if ok {
		info.ConfigurationSpec = configSpec
	}

	authMethods, ok, err := unstructured.NestedMap(config.Object, "spec", "authentication")
	if err != nil {
		return err
	}
	if !ok {
		return nil // No auth methods defined
	}

	return parseAuthentication(authMethods, g.dynamicClient, info)
}

// parseAuthentication parses the authentication object and returns the appropriate AuthMethod for the given AuthType.
// It returns an error if the authentication object is not valid.
func parseAuthentication(authMethods map[string]interface{}, dyn dynamic.Interface, info *Info) error {
	for authTypeStr, authMethod := range authMethods {
		authType, err := auth.ToType(authTypeStr)
		if err != nil {
			return err
		}

		authMethodMap, ok := authMethod.(map[string]interface{})
		if !ok {
			return fmt.Errorf("invalid auth method format for type: %s", authTypeStr)
		}

		switch authType {
		case auth.AuthTypeBasic:
			usernameRef, ok, err := unstructured.NestedStringMap(authMethodMap, "usernameRef")
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("missing usernameRef in basic auth")
			}

			username, err := GetSecret(context.Background(), dyn, SecretKeySelector{
				Name:      usernameRef["name"],
				Namespace: usernameRef["namespace"],
				Key:       usernameRef["key"],
			})
			if err != nil {
				return err
			}

			passwordRef, ok, err := unstructured.NestedStringMap(authMethodMap, "passwordRef")
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("missing passwordRef in basic auth")
			}

			password, err := GetSecret(context.Background(), dyn, SecretKeySelector{
				Name:      passwordRef["name"],
				Namespace: passwordRef["namespace"],
				Key:       passwordRef["key"],
			})
			if err != nil {
				return err
			}

			info.SetAuth = func(req *http.Request) {
				req.SetBasicAuth(username, password)
			}

			return nil
		case auth.AuthTypeBearer:
			tokenRef, ok, err := unstructured.NestedStringMap(authMethodMap, "tokenRef")
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("missing tokenRef in bearer auth")
			}
			token, err := GetSecret(context.Background(), dyn, SecretKeySelector{
				Name:      tokenRef["name"],
				Namespace: tokenRef["namespace"],
				Key:       tokenRef["key"],
			})
			if err != nil {
				return err
			}

			info.SetAuth = func(req *http.Request) {
				req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
			}
			return nil
		}
	}
	return fmt.Errorf("no supported auth method found")
}

type SecretKeySelector struct {
	Name      string
	Namespace string
	Key       string
}

func GetSecret(ctx context.Context, client dynamic.Interface, secretKeySelector SecretKeySelector) (string, error) {
	gvr := schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "secrets",
	}

	sec, err := client.Resource(gvr).Namespace(secretKeySelector.Namespace).Get(ctx, secretKeySelector.Name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	data, _, err := unstructured.NestedMap(sec.Object, "data")
	if err != nil {
		return "", err
	}
	// Check if the key exists in the data
	value, exists := data[secretKeySelector.Key]
	if !exists {
		return "", fmt.Errorf("key %s not found in secret %s/%s", secretKeySelector.Key, secretKeySelector.Namespace, secretKeySelector.Name)
	}

	// Check if the value is a string
	bsec, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("value for key %s in secret %s/%s is not a string", secretKeySelector.Key, secretKeySelector.Namespace, secretKeySelector.Name)
	}
	bkey, err := base64.StdEncoding.DecodeString(bsec)
	if err != nil {
		return "", fmt.Errorf("failed to decode secret key: %w", err)
	}
	return string(bkey), nil
}
