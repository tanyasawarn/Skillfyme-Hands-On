package orchestrator

import "time"

func expiresAtRFC3339(ttlSeconds int32) string {
	return time.Now().Add(time.Duration(ttlSeconds) * time.Second).UTC().Format(time.RFC3339)
}
