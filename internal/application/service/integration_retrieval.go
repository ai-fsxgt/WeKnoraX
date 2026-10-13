package service

import (
	"context"
	"fmt"

	"github.com/Tencent/WeKnora/internal/types"
)

func (s *IntegrationService) prepareRetrievalConfig(
	ctx context.Context, input *types.IntegrationRetrievalConfig,
) (*types.IntegrationRetrievalConfig, error) {
	if input == nil {
		var err error
		input, err = types.DefaultIntegrationRetrievalConfig()
		if err != nil {
			return nil, err
		}
	}
	c := *input
	if c.EmbeddingTopK < 1 || c.EmbeddingTopK > 50 ||
		c.VectorThreshold < 0 || c.VectorThreshold > 1 ||
		c.KeywordThreshold < 0 || c.KeywordThreshold > 1 ||
		c.RerankTopK < 1 || c.RerankTopK > 20 ||
		c.RerankThreshold < -10 || c.RerankThreshold > 10 ||
		c.FAQScoreBoost < 1 || c.FAQScoreBoost > 2 {
		return nil, fmt.Errorf("%w: invalid retrieval parameters", ErrIntegrationInvalidApplication)
	}
	models, err := s.modelService.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	if err := resolveIntegrationRetrievalModels(&c, models); err != nil {
		return nil, err
	}
	return &c, nil
}

// Applications are global: their model choices must be active system models,
// accessible in every authorized workspace. Blank IDs select the system default.
func resolveIntegrationRetrievalModels(c *types.IntegrationRetrievalConfig, models []*types.Model) error {
	builtinModels := make([]*types.Model, 0, len(models))
	for _, model := range models {
		if model != nil && model.IsBuiltin && model.Status == types.ModelStatusActive {
			builtinModels = append(builtinModels, model)
		}
	}
	for _, field := range []struct {
		id        *string
		modelType types.ModelType
	}{
		{&c.RerankModelID, types.ModelTypeRerank},
		{&c.QueryUnderstandModelID, types.ModelTypeKnowledgeQA},
	} {
		if field.modelType == types.ModelTypeKnowledgeQA && !c.EnableRewrite {
			continue
		}
		if *field.id == "" {
			*field.id = selectDefaultModelID(builtinModels, field.modelType)
			continue
		}
		found := false
		for _, model := range builtinModels {
			if model.ID == *field.id && model.Type == field.modelType {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%w: retrieval model must be an active system %s model",
				ErrIntegrationInvalidApplication, field.modelType)
		}
	}
	if c.EnableRewrite && c.QueryUnderstandModelID == "" {
		return fmt.Errorf("%w: query rewriting requires an active system chat model", ErrIntegrationInvalidApplication)
	}
	return nil
}
