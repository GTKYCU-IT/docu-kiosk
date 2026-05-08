package main

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
)

type OTP struct {
	Key     uuid.UUID
	Created time.Time
}

type RetentionMap struct {
	mu   sync.Mutex
	otps map[uuid.UUID]OTP
}

func NewRetentionMap(ctx context.Context, retentionPeriod time.Duration) *RetentionMap {
	rm := &RetentionMap{
		otps: make(map[uuid.UUID]OTP),
	}
	go rm.Retention(ctx, retentionPeriod)
	return rm
}

func (rm *RetentionMap) NewOTP() OTP {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	o := OTP{
		Key:     uuid.New(),
		Created: time.Now(),
	}
	rm.otps[o.Key] = o
	return o
}

func (rm *RetentionMap) VerifyOTP(otp uuid.UUID) bool {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	if _, ok := rm.otps[otp]; !ok {
		return false
	}
	delete(rm.otps, otp)
	return true
}

func (rm *RetentionMap) Retention(ctx context.Context, retentionPeriod time.Duration) {
	ticker := time.NewTicker(400 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			rm.mu.Lock()
			for _, otp := range rm.otps {
				if otp.Created.Add(retentionPeriod).Before(time.Now()) {
					delete(rm.otps, otp.Key)
				}
			}
			rm.mu.Unlock()
		case <-ctx.Done():
			return
		}
	}
}
