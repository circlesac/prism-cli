//go:build !darwin

package opencodego

import "time"

func browserSessions(time.Time) sessionScan {
	return sessionScan{unsupported: true}
}
