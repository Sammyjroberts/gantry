package stream

import (
	"reflect"
	"testing"
)

func TestSanitizeToken(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"drive.motor_left.current_a", "drive_motor_left_current_a"},
		{"simple", "simple"},
		{"", "_"},
		{"has space", "has_space"},
		{"star*and>gt", "star_and_gt"},
		{"tab\tnewline\n", "tab_newline_"},
		{"ctrl\x01char", "ctrl_char"},
		{"del\x7f", "del_"},
		{"UPPER_and_123", "UPPER_and_123"},
	}
	for _, c := range cases {
		if got := SanitizeToken(c.in); got != c.want {
			t.Errorf("SanitizeToken(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSubject(t *testing.T) {
	got := Subject("robot 1", "drive.speed")
	want := "tlm.robot_1.drive_speed"
	if got != want {
		t.Errorf("Subject = %q, want %q", got, want)
	}
}

func TestSubjectFilters(t *testing.T) {
	cases := []struct {
		name     string
		device   string
		channels []string
		want     []string
	}{
		{"all", "", nil, []string{"tlm.>"}},
		{"device only", "dev1", nil, []string{"tlm.dev1.>"}},
		{"device + channels", "dev1", []string{"a.b", "c"}, []string{"tlm.dev1.a_b", "tlm.dev1.c"}},
		{"channels any device", "", []string{"a"}, []string{"tlm.*.a"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := SubjectFilters(c.device, c.channels)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("SubjectFilters(%q,%v) = %v, want %v", c.device, c.channels, got, c.want)
			}
		})
	}
}

// A sanitized subject must always route under the stream's tlm.> binding and
// have the fixed three-token arity the scheme promises.
func TestSubjectArityAndBinding(t *testing.T) {
	s := Subject("a.b.c weird>", "x*y.z")
	// tlm + device + channel = exactly 3 dot-separated tokens.
	tokens := 1
	for _, r := range s {
		if r == '.' {
			tokens++
		}
	}
	if tokens != 3 {
		t.Fatalf("subject %q has %d tokens, want 3", s, tokens)
	}
	if s[:4] != "tlm." {
		t.Fatalf("subject %q not under tlm. prefix", s)
	}
}
