package pods

import (
	_ "embed"
	"fmt"
	"strings"
)

//go:embed scripts/model_run.sh
var modelRunScriptTemplate string

func buildModelRunScript(modelID, name string, port int, vllmArgs []string) string {
	s := modelRunScriptTemplate
	s = strings.ReplaceAll(s, "${MODEL_ID}", modelID)
	s = strings.ReplaceAll(s, "${NAME}", name)
	s = strings.ReplaceAll(s, "${PORT}", fmt.Sprintf("%d", port))
	s = strings.ReplaceAll(s, "${VLLM_ARGS}", strings.Join(vllmArgs, " "))
	return s
}
