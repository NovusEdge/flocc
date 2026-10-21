package steps

import (
	"reflect"
	"testing"
)

func TestCardinalLineRoundTrips(t *testing.T) {
	r := CardinalReport{Key: "ssh-ed25519 AAAA cardinal@m", Login: "full", Steps: []ReportStep{{Name: "Units", Detail: "running", Done: true}}}
	got, ok := ParseCardinalLine(r.Line())
	if !ok || !reflect.DeepEqual(got, r) {
		t.Fatalf("got %+v, %v", got, ok)
	}
	if _, ok := ParseCardinalLine(SummaryMarker + `{"key":"x"}`); ok {
		t.Fatal("parsed a line with another marker")
	}
}
