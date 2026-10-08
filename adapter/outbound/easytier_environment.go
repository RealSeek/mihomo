//go:build !no_easytier

package outbound

import (
	"context"

	"github.com/easytier/easytier/easytier-go/platform"
)

// UpdateEnvironment only refreshes a running owner; network changes never start it.
func (e *EasyTier) UpdateEnvironment(ctx context.Context, snapshot platform.EnvironmentSnapshot) error {
	e.mu.Lock()
	host := e.host
	e.mu.Unlock()
	if host == nil {
		return nil
	}
	return host.UpdateEnvironment(ctx, snapshot)
}
