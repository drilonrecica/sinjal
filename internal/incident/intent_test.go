package incident

import "testing"

// Every kind under every combination of conditions.
func TestSuppression(t *testing.T) {
	kinds := []IntentKind{IntentDown, IntentRecovery, IntentFlapping, IntentStable, IntentTLSWarning, IntentReminder}
	// Rows: maintenance, parent down, flapping. Columns: kinds, in order.
	cases := []struct {
		c    Conditions
		want [6]Reason
	}{
		{Conditions{}, [6]Reason{"", "", "", "", "", ""}},
		{Conditions{Flapping: true}, [6]Reason{ByFlapping, ByFlapping, "", "", "", ByFlapping}},
		{Conditions{ParentDown: true}, [6]Reason{ByParent, ByParent, ByParent, ByParent, "", ByParent}},
		{Conditions{ParentDown: true, Flapping: true}, [6]Reason{ByParent, ByParent, ByParent, ByParent, "", ByParent}},
		{Conditions{Maintenance: true}, [6]Reason{ByMaintenance, ByMaintenance, ByMaintenance, ByMaintenance, ByMaintenance, ByMaintenance}},
		{Conditions{Maintenance: true, Flapping: true}, [6]Reason{ByMaintenance, ByMaintenance, ByMaintenance, ByMaintenance, ByMaintenance, ByMaintenance}},
		{Conditions{Maintenance: true, ParentDown: true}, [6]Reason{ByMaintenance, ByMaintenance, ByMaintenance, ByMaintenance, ByMaintenance, ByMaintenance}},
		{Conditions{Maintenance: true, ParentDown: true, Flapping: true}, [6]Reason{ByMaintenance, ByMaintenance, ByMaintenance, ByMaintenance, ByMaintenance, ByMaintenance}},
	}
	if len(cases) != 8 {
		t.Fatal("the table must cover all eight combinations")
	}
	for _, tc := range cases {
		for i, kind := range kinds {
			if got := Suppression(kind, tc.c); got != tc.want[i] {
				t.Errorf("Suppression(%s, %+v) = %q, want %q", kind, tc.c, got, tc.want[i])
			}
		}
	}
}
