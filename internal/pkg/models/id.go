package models

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

func NewThoughtID(now time.Time, seed string) string {
	hash := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%s", now.UTC().Format(time.RFC3339Nano), seed, randomSuffix())))
	return fmt.Sprintf("%s-%s", now.Format("20060102-150405"), hex.EncodeToString(hash[:])[:6])
}

func NewJobID(jobType string, now time.Time) string {
	cleanType := strings.ReplaceAll(jobType, "_", "-")
	hash := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%s", cleanType, now.UTC().Format(time.RFC3339Nano), randomSuffix())))
	return fmt.Sprintf("job-%s-%s", cleanType, hex.EncodeToString(hash[:])[:10])
}

func NewEventID(now time.Time) string {
	hash := sha256.Sum256([]byte(fmt.Sprintf("%s:%s", now.UTC().Format(time.RFC3339Nano), randomSuffix())))
	return fmt.Sprintf("evt-%s-%s", now.Format("20060102-150405"), hex.EncodeToString(hash[:])[:8])
}

func ContentHash(content string) string {
	hash := sha256.Sum256([]byte(content))
	return "sha256:" + hex.EncodeToString(hash[:])
}

func randomSuffix() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}
