package music

// Initial retrieval heuristics, independent of model relevance or V2 weights.
const LowGenreConfidence = 0.5

func RetrievalWeight(name string) float64 {
	switch name {
	case "neo-classical":
		return .95
	case "soundtrack":
		return .70
	case "cinematic":
		return .80
	case "drone":
		return .60
	}
	return 1
}
func VocalFriendly(name string) bool {
	g, ok := Lookup(name)
	if !ok {
		return false
	}
	switch g.Category {
	case "pop", "rock", "rnb-soul", "hip-hop", "acoustic-folk", "korean", "city-retro":
		return true
	}
	return false
}
