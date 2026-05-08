package google

//go:generate go run ../../../scripts/models-gen -provider google -api google-generative-ai -input models.json -output models_gen.go -package google

import "github.com/yuri/y/pkg/ai"

// CuratedModels returns a copy of the build-time curated model list.
func CuratedModels() []ai.Model {
	out := make([]ai.Model, len(curatedModels))
	copy(out, curatedModels)
	return out
}
