package tool

import "testing"

func TestCheckNoUnquotedLeadingZerosCatchesPlainValue(t *testing.T) {
	err := checkNoUnquotedLeadingZeros([]byte("serial: 007954000891379\n"))
	if err == nil {
		t.Fatal("expected an error for an unquoted leading-zero value")
	}
}

func TestCheckNoUnquotedLeadingZerosCatchesSequenceItem(t *testing.T) {
	tests := []string{
		"- 0123\n",
		"  - serial: 0123\n",
	}
	for _, in := range tests {
		if err := checkNoUnquotedLeadingZeros([]byte(in)); err == nil {
			t.Errorf("expected an error for %q", in)
		}
	}
}

func TestCheckNoUnquotedLeadingZerosAllowsQuoted(t *testing.T) {
	tests := []string{
		`serial: "007954000891379"` + "\n",
		"serial: '007954000891379'\n",
	}
	for _, in := range tests {
		if err := checkNoUnquotedLeadingZeros([]byte(in)); err != nil {
			t.Errorf("unexpected error for %q: %v", in, err)
		}
	}
}

func TestCheckNoUnquotedLeadingZerosAllowsNonNumeric(t *testing.T) {
	tests := []string{
		"name: Hub A\n",
		"- name: Hub A\n",
		"router_id: 1.1.1.1\n",
		"default_ha_netmask: 255.255.255.252\n",
		"group_id: 0\n",
		"priority: \"1\"\n",
		"# example: 007954000891379\n",
	}
	for _, in := range tests {
		if err := checkNoUnquotedLeadingZeros([]byte(in)); err != nil {
			t.Errorf("unexpected error for %q: %v", in, err)
		}
	}
}
