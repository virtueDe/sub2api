package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type imageRequestTypeLogRepo struct {
	UsageLogRepository
	log *UsageLog
}

func (r *imageRequestTypeLogRepo) Create(_ context.Context, log *UsageLog) (bool, error) {
	r.log = log
	return true, nil
}

func TestWriteUsageLogImageRequestType(t *testing.T) {
	for _, tc := range []struct {
		name  string
		log   UsageLog
		async bool
		want  RequestType
	}{
		{name: "sync images", log: UsageLog{ImageCount: 1}, want: RequestTypeImageSync},
		{name: "async images", log: UsageLog{ImageCount: 1}, async: true, want: RequestTypeImageAsync},
		{name: "token billed image", log: UsageLog{ImageOutputTokens: 50}, want: RequestTypeImageSync},
		{name: "captured image", log: UsageLog{ImageGeneration: &ImageGenerationDetail{}}, want: RequestTypeImageSync},
		{name: "streaming image", log: UsageLog{ImageCount: 1, Stream: true, OpenAIWSMode: true}, want: RequestTypeImageSync},
		{name: "image endpoint", log: UsageLog{InboundEndpoint: optionalTrimmedStringPtr("/v1/images/edits")}, want: RequestTypeImageSync},
		{name: "batch", log: UsageLog{InboundEndpoint: optionalTrimmedStringPtr("/v1/images/batches"), ImageCount: 2}, want: RequestTypeImageAsync},
		{name: "text", log: UsageLog{RequestType: RequestTypeStream, Stream: true}, want: RequestTypeStream},
		{name: "image input only", log: UsageLog{RequestType: RequestTypeSync, ImageInputTokens: 30}, want: RequestTypeSync},
		{name: "video", log: UsageLog{RequestType: RequestTypeSync, VideoCount: 1, ImageCount: 1}, want: RequestTypeSync},
		{name: "cyber keeps meaning", log: UsageLog{RequestType: RequestTypeCyberBlocked, ImageGeneration: &ImageGenerationDetail{}}, want: RequestTypeCyberBlocked},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.async {
				ctx = WithAsyncImageTaskExecution(ctx)
			}
			repo := &imageRequestTypeLogRepo{}
			wantStream, wantWS := tc.log.Stream, tc.log.OpenAIWSMode
			writeUsageLogBestEffort(ctx, repo, &tc.log, "test.images")
			require.NotNil(t, repo.log)
			require.Equal(t, tc.want, repo.log.RequestType)
			repo.log.SyncRequestTypeAndLegacyFields()
			require.Equal(t, wantStream, repo.log.Stream)
			require.Equal(t, wantWS, repo.log.OpenAIWSMode)
		})
	}
	// Reading a pre-feature record must not invent a historical execution mode.
	historical := &UsageLog{RequestType: RequestTypeSync, ImageCount: 1}
	require.Equal(t, RequestTypeSync, historical.EffectiveRequestType())
}
