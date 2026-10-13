package types

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
)

// IntegrationRetrievalConfig is the retrieval policy shared by an application's connections.
// An absent configuration uses the system built-in quick-answer template.
type IntegrationRetrievalConfig struct {
	EmbeddingTopK          int     `json:"embedding_top_k"`
	VectorThreshold        float64 `json:"vector_threshold"`
	KeywordThreshold       float64 `json:"keyword_threshold"`
	RerankTopK             int     `json:"rerank_top_k"`
	RerankThreshold        float64 `json:"rerank_threshold"`
	RerankModelID          string  `json:"rerank_model_id"`
	EnableRewrite          bool    `json:"enable_rewrite"`
	QueryUnderstandModelID string  `json:"query_understand_model_id"`
	EnableQueryExpansion   bool    `json:"enable_query_expansion"`
	FAQPriorityEnabled     bool    `json:"faq_priority_enabled"`
	FAQScoreBoost          float64 `json:"faq_score_boost"`
}

// DefaultIntegrationRetrievalConfig reads the system template, without tenant overrides.
func DefaultIntegrationRetrievalConfig() (*IntegrationRetrievalConfig, error) {
	agent := GetBuiltinAgent(BuiltinQuickAnswerID, 0)
	if agent == nil {
		return nil, fmt.Errorf("system quick-answer template is unavailable")
	}
	c := agent.Config
	queryModelID := c.QueryUnderstandModelID
	if queryModelID == "" {
		queryModelID = c.ModelID
	}
	return &IntegrationRetrievalConfig{
		EmbeddingTopK: c.EmbeddingTopK, VectorThreshold: c.VectorThreshold,
		KeywordThreshold: c.KeywordThreshold, RerankTopK: c.RerankTopK,
		RerankThreshold: c.RerankThreshold, RerankModelID: c.RerankModelID,
		EnableRewrite: c.EnableRewrite, QueryUnderstandModelID: queryModelID,
		EnableQueryExpansion: c.EnableQueryExpansion,
		FAQPriorityEnabled:   c.FAQPriorityEnabled,
		FAQScoreBoost:        c.FAQScoreBoost,
	}, nil
}

// Value implements database serialization.
func (c IntegrationRetrievalConfig) Value() (driver.Value, error) {
	return json.Marshal(c)
}

// Scan implements database deserialization for PostgreSQL and SQLite.
func (c *IntegrationRetrievalConfig) Scan(value interface{}) error {
	switch value := value.(type) {
	case nil:
		return nil
	case []byte:
		return json.Unmarshal(value, c)
	case string:
		return json.Unmarshal([]byte(value), c)
	default:
		return fmt.Errorf("invalid integration retrieval configuration type: %T", value)
	}
}
