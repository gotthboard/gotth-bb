package config

import "fmt"

// Environment is the closed runtime posture selected at startup.
type Environment string

const (
	EnvironmentDevelopment Environment = "development"
	EnvironmentTest        Environment = "test"
	EnvironmentProduction  Environment = "production"
)

// ParseEnvironment accepts only the three documented exact environment names.
//
// Complexity: time O(1), Omega(1), and tight Theta(1); auxiliary space O(1),
// Omega(1), and tight Theta(1). The accepted keyword set and maximum keyword
// length are fixed constants; Go string length checks and at most 11 compared
// bytes are therefore bounded independently of hostile input length.
func ParseEnvironment(raw string) (Environment, error) {
	switch Environment(raw) {
	case EnvironmentDevelopment, EnvironmentTest, EnvironmentProduction:
		return Environment(raw), nil
	default:
		return "", fmt.Errorf("APP_ENV must be development, test, or production")
	}
}
