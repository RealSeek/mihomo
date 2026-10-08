package coreabi

import (
	"context"
	"fmt"
)

// UpdateEnvironment publishes process-wide connector facts without replacing
// an instance or WebClient. The caller serializes access to the guest module.
func (core *Core) UpdateEnvironment(ctx context.Context, envelope []byte) (err error) {
	pointer, err := core.allocate(ctx, uint32(len(envelope)))
	if err != nil {
		return err
	}
	defer core.cleanupBuffer(ctx, pointer, &err)
	if !core.module.Memory().Write(pointer, envelope) {
		return fmt.Errorf("write EasyTier environment update to guest memory")
	}
	result, err := core.callOne(
		ctx,
		"easytier_environment_update",
		uint64(pointer),
		uint64(len(envelope)),
	)
	if err != nil {
		return err
	}
	if status := int32(result); status != 0 {
		message, readErr := core.errorMessage(ctx, 0)
		if readErr != nil {
			return fmt.Errorf("update EasyTier environment: status=%d: %w", status, readErr)
		}
		return fmt.Errorf("update EasyTier environment: status=%d: %s", status, message)
	}
	return nil
}
