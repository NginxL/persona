package repositorywire

// Identity names a repository by its host and path, without credentials.
type Identity struct {
	Provider     string `json:"provider"`
	InstanceURL  string `json:"instance_url"`
	Path         string `json:"path"`
	CanonicalURL string `json:"canonical_url"`
}
