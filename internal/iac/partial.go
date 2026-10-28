package iac

import (
	"fmt"
	"regexp"
	"strings"
)

// ProjectPartial is the owner recorded for resources applied from a file
// that declares no partial.
const ProjectPartial = "*"

var partialName = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,64}$`)

// Partials maps resource address to owning partial name.
type Partials map[string]string

func ParsePartialName(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if value == ProjectPartial || !partialName.MatchString(value) {
		return "", fmt.Errorf("Invalid partial export: %q. Use a 1–64 character name matching [a-zA-Z0-9._-]+.", value)
	}
	return value, nil
}

func HasNamedPartials(owners Partials) bool {
	for _, owner := range owners {
		if owner != ProjectPartial {
			return true
		}
	}
	return false
}

func OwnerOf(owners Partials, address string) (string, bool) {
	owner, ok := owners[address]
	return owner, ok
}

func EffectivePartial(partial string) string {
	if partial == "" {
		return ProjectPartial
	}
	return partial
}

func ForeignResourceMessage(address, owner string) string {
	kind, name, ok := strings.Cut(address, ".")
	if !ok {
		kind, name = "resource", address
	}
	return fmt.Sprintf("Cannot manage %s %q: already managed by partial %q.", kind, name, owner)
}

func NamelessFileMessage() string {
	return "This environment already has named IaC partials. Export `const partial = \"<name>\"` from this file instead of managing the whole project."
}

func NeedsPartialClaimApply(declared []string, owners Partials, partial string) bool {
	effective := EffectivePartial(partial)
	for _, address := range declared {
		if owner, ok := owners[address]; !ok || owner != effective {
			return true
		}
	}
	return false
}
