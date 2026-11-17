package service

import (
	"context"

	"github.com/techverito/parking-service/internal/domain"
)

// Notifier delivers a check-in confirmation to the driver. Implementations live
// outside this package so business code never imports a provider SDK.
//
// Notifying is best effort: the error is handed back so the caller can log it,
// never so it can fail the check-in.
type Notifier interface {
	NotifyCheckIn(ctx context.Context, notice domain.CheckInNotice) error
}
