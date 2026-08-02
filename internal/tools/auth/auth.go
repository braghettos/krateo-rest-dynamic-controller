package auth

import "fmt"

type AuthType string

const (
	AuthTypeBasic  AuthType = "basic"
	AuthTypeBearer AuthType = "bearer"
	// AuthTypeAPIKey is an OAS `type: apiKey, in: header` scheme: a credential sent verbatim in a
	// document-declared header. Unlike bearer, neither the header name nor any value prefix is fixed here —
	// both are carried on the Configuration CR, because this package's caller resolves authentication
	// before the OAS document is parsed (definitiongetter has no OpenAPI dependency; DocScheme is built
	// later, on the client) and so cannot read the declared header name itself.
	AuthTypeAPIKey AuthType = "apiKey"
)

func (a AuthType) String() string {
	return string(a)
}

func ToType(ty string) (AuthType, error) {
	switch ty {
	case "basic":
		return AuthTypeBasic, nil
	case "bearer":
		return AuthTypeBearer, nil
	case "apiKey":
		return AuthTypeAPIKey, nil
	}
	return "", fmt.Errorf("unknown auth type: %s", ty)
}
