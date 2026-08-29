package cli

import (
	"context"

	"github.com/circlesac/prism-cli/internal/api"
)

var fetchGeminiUsage = func(ctx context.Context, options commonOptions) (api.ProviderUsage, error) {
	client, err := prismClient(ctx, options)
	if err != nil {
		return api.ProviderUsage{}, err
	}
	return client.Usage(ctx, "gemini")
}
