package service

import "context"

type imageUpstreamResponsePassthroughContextKey struct{}
type imageUpstreamResponsePassthroughItemsContextKey struct{}

// WithImageUpstreamResponsePassthrough carries the selected account's response
// policy into task completion without inheriting the execution deadline.
func WithImageUpstreamResponsePassthrough(ctx context.Context, enabled bool) context.Context {
	return context.WithValue(ctx, imageUpstreamResponsePassthroughContextKey{}, enabled)
}

func imageUpstreamResponsePassthrough(ctx context.Context) bool {
	enabled, _ := ctx.Value(imageUpstreamResponsePassthroughContextKey{}).(bool)
	return enabled
}

// WithImageUpstreamResponsePassthroughItems carries per-image policies when a
// single task aggregates multiple upstream requests using different accounts.
func WithImageUpstreamResponsePassthroughItems(ctx context.Context, items []bool) context.Context {
	return context.WithValue(ctx, imageUpstreamResponsePassthroughItemsContextKey{}, append([]bool(nil), items...))
}

func imageUpstreamResponsePassthroughItems(ctx context.Context) []bool {
	items, _ := ctx.Value(imageUpstreamResponsePassthroughItemsContextKey{}).([]bool)
	return items
}

func (s *OpenAIGatewayService) IsImageUpstreamResponsePassthroughEnabledForAccount(ctx context.Context, accountID int64) bool {
	return s != nil && s.settingService.IsImageUpstreamResponsePassthroughEnabledForAccount(ctx, accountID)
}
