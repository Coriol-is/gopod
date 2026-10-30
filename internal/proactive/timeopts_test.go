package proactive

import (
	"reflect"
	"testing"
)

func TestTimeOptionsAreHourlyWithinRangeAndSkipQuietHours(t *testing.T) {
	p, _ := Lookup("gmail.morning") // 0600-1100
	got := p.TimeOptions(nil)
	want := []string{"0600", "0700", "0800", "0900", "1000", "1100"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
	q := Quiet{StartMin: 22 * 60, EndMin: 7*60 + 30}
	if got := p.TimeOptions(&q); !reflect.DeepEqual(got, []string{"0800", "0900", "1000", "1100"}) {
		t.Fatalf("with quiet: %v", got)
	}
	poll, _ := Lookup("gmail.urgent")
	if got := poll.TimeOptions(nil); got != nil {
		t.Fatalf("a polling preset has no time of day: %v", got)
	}
}
