package requestmodel

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestKlingModelCandidatesAndRewritePath(t *testing.T) {
	body := []byte(`{"model":"ignored","model_name":"allowed","model_name":"blocked","MODEL_NAME":"also-blocked"}`)
	require.Equal(t, []string{"allowed", "blocked", "also-blocked"}, FromBodyCandidates("/v1/videos/text2video", "application/json", body))
	model, path := JSONModelPathForRoute("/v1/videos/image2video", body)
	require.Equal(t, "allowed", model)
	require.Equal(t, "model_name", path)
	require.Equal(t, []string{"ignored"}, FromBodyCandidates("/v1/chat/completions", "application/json", body))
}
