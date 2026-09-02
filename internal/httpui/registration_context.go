package httpui

import "context"

type registrationEnabledContextKey struct{}

func registrationEnabledFromContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	enabled, _ := ctx.Value(registrationEnabledContextKey{}).(bool)
	return enabled
}
