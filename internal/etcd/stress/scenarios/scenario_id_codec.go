package scenarios

// ListAllIDs lists all stress scenario IDs.
// Same pattern as conformance ListAllIDs.
func ListAllIDs() ([]StressID, []string) {
	count := len(StressIDStringToID)
	ids := make([]StressID, 0, count)
	ss := make([]string, 0, count)
	for i := range count {
		id := StressID(i)
		ids = append(ids, id)
		ss = append(ss, id.String())
	}

	return ids, ss
}

// ValidStressID validates a stress scenario ID string
// Same pattern as conformance ValidID.
func ValidStressID(id string) bool {
	_, ok := StressIDStringToID[id]

	return ok
}
