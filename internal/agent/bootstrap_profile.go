package agent

type BootstrapProfile string

const (
	BootstrapProfileAtlasOnly            BootstrapProfile = "atlas-only-v1"
	BootstrapProfileAtlasLexical         BootstrapProfile = "atlas-lexical-v1"
	BootstrapProfileAtlasLexicalPrefetch BootstrapProfile = "atlas-lexical-prefetch-v1"
)

func DefaultBootstrapProfile() BootstrapProfile {
	return BootstrapProfileAtlasLexicalPrefetch
}

func ResolveBootstrapProfile(profile BootstrapProfile) (BootstrapProfile, bool) {
	if profile == "" {
		return DefaultBootstrapProfile(), true
	}
	switch profile {
	case BootstrapProfileAtlasOnly, BootstrapProfileAtlasLexical, BootstrapProfileAtlasLexicalPrefetch:
		return profile, true
	default:
		return "", false
	}
}

func (profile BootstrapProfile) usesLexical() bool {
	return profile == BootstrapProfileAtlasLexical || profile == BootstrapProfileAtlasLexicalPrefetch
}

func (profile BootstrapProfile) usesPrefetch() bool {
	return profile == BootstrapProfileAtlasLexicalPrefetch
}
