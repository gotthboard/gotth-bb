package buildinfo

import "fmt"

const (
	developmentVersion  = "development"
	unknownCommit       = "unknown"
	maximumVersionBytes = 64
)

// version and commit are package-private release-linker injection points.
// Development builds retain explicit non-release values rather than
// fabricating provenance.
var (
	version = developmentVersion
	commit  = unknownCommit
)

// Info is the bounded immutable identity emitted by operator and service
// boundaries.
type Info struct {
	Version string
	Commit  string
}

// Current validates the link-time release identity before it reaches output
// or structured logs.
//
// Complexity: for v <= 64 version bytes and c <= 40 commit bytes, time is
// O(v+c), Omega(1), and auxiliary space is tight Theta(1).
func Current() (Info, error) {
	return validate(version, commit)
}

// validate accepts the explicit development sentinel or a bounded release
// version paired with an exact lowercase full Git object name.
//
// Complexity: for v <= 64 version bytes and c <= 40 commit bytes, time is
// O(v+c), Omega(1), and auxiliary space is tight Theta(1).
func validate(version, commit string) (Info, error) {
	if version == developmentVersion && commit == unknownCommit {
		return Info{Version: version, Commit: commit}, nil
	}
	if len(version) == 0 || len(version) > maximumVersionBytes || version[0] < '0' || version[0] > '9' {
		return Info{}, fmt.Errorf("release version is invalid")
	}
	for _, character := range []byte(version) {
		if character >= '0' && character <= '9' || character >= 'A' && character <= 'Z' ||
			character >= 'a' && character <= 'z' || character == '.' || character == '-' || character == '+' {
			continue
		}
		return Info{}, fmt.Errorf("release version is invalid")
	}
	if len(commit) != 40 {
		return Info{}, fmt.Errorf("release commit is invalid")
	}
	for _, character := range []byte(commit) {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return Info{}, fmt.Errorf("release commit is invalid")
		}
	}
	return Info{Version: version, Commit: commit}, nil
}
