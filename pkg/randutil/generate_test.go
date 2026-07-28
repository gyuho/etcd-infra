package randutil_test

import (
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	rand "git.tbd/etcd-infra/pkg/randutil"
)

func TestRand(t *testing.T) {
	t.Parallel()

	now := time.Now()
	t.Logf("seeding with %v", now)

	rand.SetSeed(now.UnixNano())

	prev := ""
	for range 10 {
		v := string(rand.BytesAlphabetsLowerCase(5))
		t.Log(v)

		if prev == "" {
			prev = v

			continue
		}
		assert.NotEqual(t, prev, v, "not random")
	}
}

func TestBytesAlphabetsLowerCase(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		length int
	}{
		{"zero length", 0},
		{"single char", 1},
		{"short string", 5},
		{"medium string", 20},
		{"long string", 100},
	}

	lowerCasePattern := regexp.MustCompile("^[a-z]*$")

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := rand.BytesAlphabetsLowerCase(tt.length)
			assert.Len(t, result, tt.length)
			assert.True(t, lowerCasePattern.Match(result), "result should only contain lowercase letters: %s", result)
		})
	}
}

func TestStringAlphabetsLowerCase(t *testing.T) {
	t.Parallel()

	result := rand.StringAlphabetsLowerCase(10)
	assert.Len(t, result, 10)

	lowerCasePattern := regexp.MustCompile("^[a-z]+$")
	assert.True(t, lowerCasePattern.MatchString(result), "result should only contain lowercase letters: %s", result)
}

func TestBytesAlphabetsLowerCaseNumeric(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		length int
	}{
		{"zero length", 0},
		{"single char", 1},
		{"short string", 5},
		{"medium string", 20},
		{"long string", 100},
	}

	alphaNumPattern := regexp.MustCompile("^[a-z0-9]*$")

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := rand.BytesAlphabetsLowerCaseNumeric(tt.length)
			assert.Len(t, result, tt.length)
			assert.True(t, alphaNumPattern.Match(result), "result should only contain lowercase letters and digits: %s", result)
		})
	}
}

func TestStringAlphabetsLowerCaseNumeric(t *testing.T) {
	t.Parallel()

	result := rand.StringAlphabetsLowerCaseNumeric(15)
	assert.Len(t, result, 15)

	alphaNumPattern := regexp.MustCompile("^[a-z0-9]+$")
	//nolint:lll // Error message is long
	assert.True(t, alphaNumPattern.MatchString(result), "result should only contain lowercase letters and digits: %s", result)
}

func TestBytesAlphabetsNumericWithSpecialCharacters(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		length int
	}{
		{"zero length", 0},
		{"single char", 1},
		{"short string", 5},
		{"medium string", 20},
		{"long string", 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := rand.BytesAlphabetsNumericWithSpecialCharacters(tt.length)
			assert.Len(t, result, tt.length)
		})
	}
}

func TestStringAlphabetsNumericWithSpecialCharacters(t *testing.T) {
	t.Parallel()

	result := rand.StringAlphabetsNumericWithSpecialCharacters(20)
	assert.Len(t, result, 20)
}

func TestIntn(t *testing.T) {
	t.Parallel()

	// Test that Intn returns values within range
	for range 100 {
		n := rand.Intn(10)
		assert.GreaterOrEqual(t, n, 0, "Intn should return non-negative")
		assert.Less(t, n, 10, "Intn should return value less than n")
	}

	// Test with different bounds
	bounds := []int{1, 5, 100, 1000}
	for _, bound := range bounds {
		t.Run("bound_"+string(rune(bound)), func(t *testing.T) {
			t.Parallel()
			n := rand.Intn(bound)
			assert.GreaterOrEqual(t, n, 0)
			assert.Less(t, n, bound)
		})
	}
}

func TestShuffle(t *testing.T) {
	t.Parallel()

	// Create a slice to shuffle
	original := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	shuffled := make([]int, len(original))
	copy(shuffled, original)

	rand.Shuffle(len(shuffled), func(i, j int) {
		shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
	})

	// Verify all elements are still present (just reordered)
	assert.ElementsMatch(t, original, shuffled, "shuffle should preserve all elements")

	// Note: There's a small chance the shuffle produces the same order,
	// but with 10 elements, probability is 1/10! ≈ 0.00003%
}

//nolint:paralleltest // Deterministic seed test must not run in parallel with others
func TestSetSeed_Deterministic(t *testing.T) {
	// Set same seed and verify we get same sequence
	seed := int64(12345)

	rand.SetSeed(seed)
	first := rand.StringAlphabetsLowerCase(10)

	rand.SetSeed(seed)
	second := rand.StringAlphabetsLowerCase(10)

	assert.Equal(t, first, second, "same seed should produce same sequence")
}

func TestRandConcurrency(t *testing.T) {
	t.Parallel()

	done := make(chan bool)
	results := make(chan string, 1000)

	// Launch concurrent goroutines
	for range 10 {
		go func() {
			for range 100 {
				s := rand.StringAlphabetsLowerCase(5)
				results <- s
				_ = rand.Intn(100)
			}
			done <- true
		}()
	}

	// Wait for all goroutines
	for range 10 {
		<-done
	}
	close(results)

	// Verify we got all results without panics
	count := 0
	for range results {
		count++
	}
	require.Equal(t, 1000, count, "should have received all results")
}

func TestRandomnessDistribution(t *testing.T) {
	t.Parallel()

	// Test that Intn has reasonable distribution
	counts := make(map[int]int)
	iterations := 10000
	bound := 10

	for range iterations {
		n := rand.Intn(bound)
		counts[n]++
	}

	// Each bucket should have roughly iterations/bound occurrences
	// Allow 50% deviation for statistical variance
	expectedPerBucket := iterations / bound
	minExpected := expectedPerBucket / 2
	maxExpected := expectedPerBucket * 2

	for i := range bound {
		count := counts[i]
		assert.Greater(t, count, minExpected, "bucket %d has too few hits: %d", i, count)
		assert.Less(t, count, maxExpected, "bucket %d has too many hits: %d", i, count)
	}
}
