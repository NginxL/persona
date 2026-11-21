package sessionlist

import "slices"

// maxProviders bounds a supplied provider OR set before duplicate canonicalization.
const maxProviders = 64

// normalizeProviders gives every spelling of one provider set the same options
// value. Provider identities remain exact and case-sensitive.
func normalizeProviders(providers []string) []string {
	if len(providers) == 0 {
		return nil
	}
	providers = slices.Clone(providers)
	slices.Sort(providers)
	return slices.Compact(providers)
}

// providersKey reuses the length-prefixed set encoding and canonical cursor
// binding of places, with an empty machine part for each provider identity.
func providersKey(providers []string) string {
	if len(providers) == 0 {
		return ""
	}
	asPlaces := make([]Place, len(providers))
	for n, provider := range providers {
		asPlaces[n].Key = provider
	}
	return placesKey(asPlaces)
}

func inProviders(spelled, provider string) bool {
	return inPlaces(spelled, "", provider)
}
