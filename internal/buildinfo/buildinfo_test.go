package buildinfo

import (
	"reflect"
	"testing"
)

func TestParseTags(t *testing.T) {
	got := parseTags(" feature_rpc,feature_openai,, feature_git ")
	want := []string{"feature_rpc", "feature_openai", "feature_git"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseTags returned %#v, want %#v", got, want)
	}
}

func TestParseTagsEmpty(t *testing.T) {
	if got := parseTags(""); got != nil {
		t.Fatalf("parseTags returned %#v, want nil", got)
	}
}
