package agent

type BootstrapProfile string

const (
	BootstrapProfileAtlasOnly            BootstrapProfile = "atlas-only-v1"
	BootstrapProfileAtlasLexical         BootstrapProfile = "atlas-lexical-v1"
	BootstrapProfileAtlasLexicalPrefetch BootstrapProfile = "atlas-lexical-prefetch-v1"
)
