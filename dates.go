package main

import (
	"time"

	approxidate "github.com/mattmc3/go-approxidate"
)

// A range is inclusive at the near end and exclusive at the far end, so
// adjacent ranges tile. A day named without a time of day covers all of
// itself, which is what makes one date given to both ends that whole day.

func rangeStart(text string, now time.Time) (time.Time, error) {
	result, err := approxidate.Parse(text, now)
	return result.Time, err
}

func rangeEnd(text string, now time.Time) (time.Time, error) {
	result, err := approxidate.Parse(text, now)
	if err != nil {
		return time.Time{}, err
	}
	if result.DayOnly {
		return result.Time.AddDate(0, 0, 1), nil
	}
	return result.Time, nil
}
