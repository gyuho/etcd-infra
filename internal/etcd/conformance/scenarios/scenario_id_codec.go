package scenarios

// ListAllIDs returns all registered scenario IDs paired with their string names.
// The returned slices have matching indices: ids[i].String() == names[i].
func ListAllIDs() ([]ID, []string) {
	n := len(_IDIndex) - 1
	ids := make([]ID, 0, n)
	names := make([]string, 0, n)
	for i := range n {
		ids = append(ids, ID(i))
		names = append(names, _IDName[_IDIndex[i]:_IDIndex[i+1]])
	}

	return ids, names
}

// ValidID checks whether the given string is a registered scenario ID.
func ValidID(id string) bool {
	_, ok := IDStringToID[id]
	return ok
}
