package testtime

import (
	"encoding/json"
	"time"
)

// Time keeps the scenario result shape while using only the standard library.
type Time struct {
	time.Time
}

func NewTime(value time.Time) Time {
	return Time{Time: value}
}

func Now() Time {
	return NewTime(time.Now())
}

func (value *Time) Equal(other *Time) bool {
	if value == nil || other == nil {
		return value == other
	}
	return value.Time.Equal(other.Time)
}

func (value Time) MarshalJSON() ([]byte, error) {
	if value.IsZero() {
		return []byte("null"), nil
	}
	return json.Marshal(value.UTC().Format(time.RFC3339))
}

func (value *Time) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		value.Time = time.Time{}
		return nil
	}
	var encoded string
	if err := json.Unmarshal(data, &encoded); err != nil {
		return err
	}
	parsed, err := time.Parse(time.RFC3339, encoded)
	if err != nil {
		return err
	}
	value.Time = parsed.Local()
	return nil
}

// Duration marshals elapsed time as a Go duration string.
type Duration struct {
	time.Duration
}

func (value Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(value.String())
}

func (value *Duration) UnmarshalJSON(data []byte) error {
	var encoded string
	if err := json.Unmarshal(data, &encoded); err != nil {
		return err
	}
	return value.UnmarshalText([]byte(encoded))
}

func (value Duration) MarshalText() ([]byte, error) {
	return []byte(value.String()), nil
}

func (value *Duration) UnmarshalText(data []byte) error {
	parsed, err := time.ParseDuration(string(data))
	if err != nil {
		return err
	}
	value.Duration = parsed
	return nil
}
