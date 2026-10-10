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
		ID:             "ch-1",
		Platform:       "feishu",
		Name:           "support",
		CreatedBy:      "user-1",
		AllowGroupChat: true,
		Credentials: types.JSON(`{
			"app_id":"cli_support","app_secret":"top-secret","encrypt_key":"encrypt-secret"
		}`),
	}

	summary, err := SummarizeIMChannel(ch)
	require.NoError(t, err)
	body, err := json.Marshal(summary)
	require.NoError(t, err)

	assert.True(t, summary.CredentialsConfigured)
	assert.True(t, summary.PersonalBinding)
	assert.True(t, summary.AllowGroupChat)
	assert.Equal(t, "user-1", summary.CreatedBy)
	assert.Equal(t, map[string]string{"app_id": "cli_support"}, summary.CredentialIDs)
	assert.NotContains(t, string(body), "top-secret")
	assert.NotContains(t, string(body), "encrypt-secret")
	assert.NotContains(t, string(body), `"credentials":`)
}

func TestSummarizeIMChannel_EmptyCredentialsNotConfigured(t *testing.T) {
	summary, err := SummarizeIMChannel(IMChannel{Credentials: types.JSON("{}")})
	require.NoError(t, err)
	assert.False(t, summary.CredentialsConfigured)
	assert.Empty(t, summary.CredentialIDs)
}
