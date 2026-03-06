package addons

import (
	"context"
	"time"
)

func init() { Register(&certManager{}) }

type certManager struct{}

func (c *certManager) Name() string           { return "cert-manager" }
func (c *certManager) Dependencies() []string { return nil }

func (c *certManager) Install(ctx context.Context, cfg *Config) error {
	return installOperator(ctx, cfg, subscriptionOpts{
		name:    "cert-manager",
		channel: "stable",
		catalog: communityCatalog,
	})
}

func (c *certManager) Ready(ctx context.Context, cfg *Config) error {
	return waitForCSV(ctx, cfg, "cert-manager", 5*time.Minute)
}
