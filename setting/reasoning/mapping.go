package reasoning

// ResolveChannelModel follows channel aliases exactly as the relay will send them. An empty target signals a cycle for a non-empty input.
// The second result distinguishes an explicit identity rule from no rule, as required by existing request adapters.
func ResolveChannelModel(mapping map[string]string, name string) (string, bool) {
	if len(mapping) == 0 {
		return name, false
	}
	current := name
	visited := map[string]bool{}
	for {
		next := mapping[current]
		if next == "" {
			next = mapping[BaseModelName(current)]
		}
		if next == "" || next == current {
			return current, next != "" && next == name && current == name
		}
		if visited[current] {
			return "", false
		}
		visited[current] = true
		current = next
	}
}
