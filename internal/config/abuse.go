package config

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	maximumRateCount      = 100_000
	maximumClientCapacity = 65_536
)

// AbuseConfig is the immutable, bounded startup configuration for AN-05.
type AbuseConfig struct {
	RulesFile              string
	RequestLimit           uint32
	RequestWindow          time.Duration
	RequestClientCapacity  uint32
	PublishLimit           uint32
	NewAccountPublishLimit uint32
	PublishWindow          time.Duration
	NewAccountPeriod       time.Duration
}

func loadAbuseConfig(required func(string) (string, error)) (AbuseConfig, error) {
	rulesFileRaw, err := required("ABUSE_RULES_FILE")
	if err != nil {
		return AbuseConfig{}, err
	}
	if rulesFileRaw == "" || len(rulesFileRaw) > 4096 || strings.IndexByte(rulesFileRaw, 0) >= 0 ||
		!filepath.IsAbs(rulesFileRaw) || filepath.Clean(rulesFileRaw) != rulesFileRaw || rulesFileRaw == string(filepath.Separator) {
		return AbuseConfig{}, fmt.Errorf("ABUSE_RULES_FILE must be an absolute clean non-root path of at most 4096 bytes without NUL")
	}
	requestLimit, err := requiredBoundedUint(required, "REQUEST_RATE_LIMIT", maximumRateCount)
	if err != nil {
		return AbuseConfig{}, err
	}
	requestWindow, err := requiredBoundedDuration(required, "REQUEST_RATE_WINDOW", time.Second, 24*time.Hour)
	if err != nil {
		return AbuseConfig{}, err
	}
	capacity, err := requiredBoundedUint(required, "REQUEST_RATE_CLIENT_CAPACITY", maximumClientCapacity)
	if err != nil {
		return AbuseConfig{}, err
	}
	publishLimit, err := requiredBoundedUint(required, "PUBLISH_RATE_LIMIT", maximumRateCount)
	if err != nil {
		return AbuseConfig{}, err
	}
	newPublishLimit, err := requiredBoundedUint(required, "NEW_ACCOUNT_PUBLISH_RATE_LIMIT", maximumRateCount)
	if err != nil {
		return AbuseConfig{}, err
	}
	if newPublishLimit > publishLimit {
		return AbuseConfig{}, fmt.Errorf("NEW_ACCOUNT_PUBLISH_RATE_LIMIT must not exceed PUBLISH_RATE_LIMIT")
	}
	publishWindow, err := requiredBoundedDuration(required, "PUBLISH_RATE_WINDOW", time.Second, 24*time.Hour)
	if err != nil {
		return AbuseConfig{}, err
	}
	newAccountPeriod, err := requiredBoundedDuration(required, "NEW_ACCOUNT_PERIOD", time.Minute, 30*24*time.Hour)
	if err != nil {
		return AbuseConfig{}, err
	}
	return AbuseConfig{
		RulesFile: rulesFileRaw, RequestLimit: requestLimit, RequestWindow: requestWindow,
		RequestClientCapacity: capacity, PublishLimit: publishLimit,
		NewAccountPublishLimit: newPublishLimit, PublishWindow: publishWindow,
		NewAccountPeriod: newAccountPeriod,
	}, nil
}

func requiredBoundedUint(required func(string) (string, error), name string, maximum uint32) (uint32, error) {
	raw, err := required(name)
	if err != nil {
		return 0, err
	}
	parsed, err := strconv.ParseUint(raw, 10, 32)
	if err != nil || parsed == 0 || parsed > uint64(maximum) || strconv.FormatUint(parsed, 10) != raw {
		return 0, fmt.Errorf("%s must be canonical decimal from 1 through %d", name, maximum)
	}
	return uint32(parsed), nil
}

func requiredBoundedDuration(required func(string) (string, error), name string, minimum, maximum time.Duration) (time.Duration, error) {
	raw, err := required(name)
	if err != nil {
		return 0, err
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil || parsed < minimum || parsed > maximum {
		return 0, fmt.Errorf("%s must be a duration from %s through %s", name, minimum, maximum)
	}
	return parsed, nil
}
