package receivable

import "time"

var jakarta = time.FixedZone("WIB", 7*60*60)

// DueDate uses the pay day snapshotted at submit. An approval after its
// original due date moves the obligation to the following monthly pay day.
func DueDate(submitted, approved time.Time, payDay int) time.Time {
	y, m, _ := submitted.In(jakarta).Date()
	m++
	if m > 12 {
		m, y = 1, y+1
	}
	due := time.Date(y, m, payDay, 0, 0, 0, 0, jakarta)
	ay, am, ad := approved.In(jakarta).Date()
	approvalDay := time.Date(ay, am, ad, 0, 0, 0, 0, jakarta)
	if approvalDay.After(due) {
		return NextPayDay(approved, payDay)
	}
	return due
}

// GraceEnd counts the next two Monday–Friday business days. The calendar of
// public holidays is kept separately from payment due dates, which never move.
func GraceEnd(start time.Time) time.Time {
	d := start.In(jakarta)
	d = time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, jakarta)
	for n := 0; n < 2; {
		d = d.AddDate(0, 0, 1)
		if d.Weekday() != time.Saturday && d.Weekday() != time.Sunday {
			n++
		}
	}
	return d
}

// NextPayDay returns the first monthly pay day strictly after a correction.
func NextPayDay(after time.Time, payDay int) time.Time {
	y, m, d := after.In(jakarta).Date()
	if d >= payDay {
		m++
		if m > 12 {
			m = 1
			y++
		}
	}
	return time.Date(y, m, payDay, 0, 0, 0, 0, jakarta)
}
