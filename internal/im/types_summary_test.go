package im

import (
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSummarizeIMChannel_OmitsCredentials(t *testing.T) {
	ch := IMChannel{
		ID:          "ch-1",
		Platform:    "feishu",
		Name:        "support",
		CreatedBy:   "user-1",
		Credentials: types.JSON(`{"app_secret":"top-secret"}`),
	}

	summary := SummarizeIMChannel(ch)
	body, err := json.Marshal(summary)
	require.NoError(t, err)

	assert.True(t, summary.CredentialsConfigured)
	assert.True(t, summary.PersonalBinding)
	assert.Equal(t, "user-1", summary.CreatedBy)
	assert.NotContains(t, string(body), "top-secret")
	assert.NotContains(t, string(body), `"credentials":`)
}

func TestSummarizeIMChannel_EmptyCredentialsNotConfigured(t *testing.T) {
	summary := SummarizeIMChannel(IMChannel{Credentials: types.JSON("{}")})
	assert.False(t, summary.CredentialsConfigured)
}
