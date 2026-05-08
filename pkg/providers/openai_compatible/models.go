package openai_compatible

//go:generate go run ../../../scripts/models-gen -provider openai-compatible -api openai-completions -input models.json -output models_gen.go -package openai_compatible

import "github.com/yuri/y/pkg/ai"

// CuratedModels returns a copy of the build-time curated model list.
func CuratedModels() []ai.Model {
	out := make([]ai.Model, len(curatedModels))
	copy(out, curatedModels)
	return out
}
