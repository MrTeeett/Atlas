package buildinfo

// These variables are set via -ldflags at build time.
// Defaults are suitable for local/dev builds.

var (
	Version = "dev"
	Channel = "dev" // dev|stable
	Commit  = ""
	BuiltAt = ""
	Repo    = "MrTeeett/Atlas"
)
