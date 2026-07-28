package randutil

const (
	alphabetsLowerCase        = "abcdefghijklmnopqrstuvwxyz"
	alphabetsLowerCaseNumeric = "0123456789" + alphabetsLowerCase
	//nolint:lll // Constants are allowed to be long
	alphabetsNumericWithSpecialCharacters = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ?!@#$%^&*()~-=_+[]{}';:,.<>|/"
)

// BytesAlphabetsLowerCase returns a random byte slice of length n containing lowercase alphabets.
func BytesAlphabetsLowerCase(n int) []byte {
	return randBytes(alphabetsLowerCase, n)
}

// StringAlphabetsLowerCase returns a random string of length n containing lowercase alphabets.
func StringAlphabetsLowerCase(n int) string {
	return string(BytesAlphabetsLowerCase(n))
}

// BytesAlphabetsLowerCaseNumeric returns a random byte slice of length n
// containing lowercase alphabets and numbers.
func BytesAlphabetsLowerCaseNumeric(n int) []byte {
	return randBytes(alphabetsLowerCaseNumeric, n)
}

// StringAlphabetsLowerCaseNumeric returns a random string of length n
// containing lowercase alphabets and numbers.
func StringAlphabetsLowerCaseNumeric(n int) string {
	return string(BytesAlphabetsLowerCaseNumeric(n))
}

// BytesAlphabetsNumericWithSpecialCharacters returns a random byte slice of length n
// containing alphabets, numbers, and special characters.
func BytesAlphabetsNumericWithSpecialCharacters(n int) []byte {
	return randBytes(alphabetsNumericWithSpecialCharacters, n)
}

// StringAlphabetsNumericWithSpecialCharacters returns a random string of length n
// containing alphabets, numbers, and special characters.
func StringAlphabetsNumericWithSpecialCharacters(n int) string {
	return string(BytesAlphabetsNumericWithSpecialCharacters(n))
}

// SetSeed sets the seed for the global random source.
func SetSeed(seed int64) {
	mu.Lock()
	defer mu.Unlock()
	rnd.Seed(seed)
}

func randBytes(pattern string, n int) []byte {
	mu.Lock()
	defer mu.Unlock()

	b := make([]byte, n)
	for i := range b {
		b[i] = pattern[rnd.Intn(len(pattern))]
	}

	return b
}

// Intn returns a non-negative pseudo-random number in [0,n) using the shared
// pseudo-random source protected by a mutex to avoid data races.
func Intn(n int) int {
	mu.Lock()
	defer mu.Unlock()

	return rnd.Intn(n)
}

// Shuffle pseudo-randomly permutes the sequence by calling swap. The
// operation is protected by a mutex to avoid concurrent access to the shared
// generator.
func Shuffle(n int, swap func(i, j int)) {
	mu.Lock()
	defer mu.Unlock()
	rnd.Shuffle(n, swap)
}
