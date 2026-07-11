package restResources

import (
	"testing"

	getter "github.com/krateoplatformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/stretchr/testify/assert"
)

func TestHasObserveVerb(t *testing.T) {
	assert.False(t, hasObserveVerb(nil))
	assert.False(t, hasObserveVerb([]getter.VerbsDescription{{Action: "create"}, {Action: "delete"}}),
		"create/delete alone cannot verify convergence")
	assert.True(t, hasObserveVerb([]getter.VerbsDescription{{Action: "create"}, {Action: "get"}}))
	assert.True(t, hasObserveVerb([]getter.VerbsDescription{{Action: "findby"}}))
	assert.True(t, hasObserveVerb([]getter.VerbsDescription{{Action: "GET"}}), "match is case-insensitive")
}
