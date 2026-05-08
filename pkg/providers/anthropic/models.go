package anthropic

//go:generate go run ../../../scripts/models-gen -provider anthropic -api anthropic-messages -input models.json -output models_gen.go -package anthropic

import "github.com/yuri/y/pkg/ai"

// CuratedModels returns a copy of the build-time curated model list. It is
// the canonical fallback list used when the live API is unreachable. Each
// Model has BaseURL left empty; callers may set it when constructing a
// Provider so that subsequent requests pin the correct endpoint.
func CuratedModels() []ai.Model {
	out := make([]ai.Model, len(curatedModels))
	copy(out, curatedModels)
	return out
}
