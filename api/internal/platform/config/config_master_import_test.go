package config

import "testing"

func TestValidImportSnapshotKeyWajibDiStaging(t *testing.T) {
	valid := "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="
	for _, tc := range []struct {
		name  string
		env   string
		key   string
		apply bool
		want  bool
	}{
		{name: "staging valid", env: "staging", key: valid, want: true},
		{name: "staging kosong", env: "staging", want: false},
		{name: "staging pendek", env: "staging", key: "cGVuZGVr", want: false},
		{name: "local boleh kosong", env: "local", want: true},
		{name: "production import ditolak service", env: "production", want: true},
		{name: "production apply tanpa kunci", env: "production", apply: true, want: false},
		{name: "production apply kunci valid", env: "production", key: valid, apply: true, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := validImportSnapshotKey(tc.env, tc.key, tc.apply); got != tc.want {
				t.Fatalf("validImportSnapshotKey=%v want=%v", got, tc.want)
			}
		})
	}
}
