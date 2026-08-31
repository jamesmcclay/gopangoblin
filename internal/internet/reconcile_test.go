package internet

import "testing"

func TestResolveDHCPPoolAutoDerivesFromLiteralCIDR(t *testing.T) {
	it := ResolvedItem{LANCIDR: "10.0.0.1/24"}
	pool, err := resolveDHCPPool(it)
	if err != nil {
		t.Fatalf("resolveDHCPPool: %v", err)
	}
	if pool != "10.0.0.128-10.0.0.254" {
		t.Errorf("pool = %q, want 10.0.0.128-10.0.0.254", pool)
	}
}

func TestResolveDHCPPoolExplicitOverridesAutoDerive(t *testing.T) {
	it := ResolvedItem{LANCIDR: "10.0.0.1/24", DHCPPool: "10.0.0.50-10.0.0.60"}
	pool, err := resolveDHCPPool(it)
	if err != nil {
		t.Fatalf("resolveDHCPPool: %v", err)
	}
	if pool != "10.0.0.50-10.0.0.60" {
		t.Errorf("pool = %q, want explicit override", pool)
	}
}

func TestResolveDHCPPoolErrorsWhenLANCIDRIsVariableAndPoolUnset(t *testing.T) {
	it := ResolvedItem{LANCIDR: "$lan_cidr"}
	if _, err := resolveDHCPPool(it); err == nil {
		t.Fatal("expected error when lan_cidr is a $variable and dhcp_pool is unset")
	}
}

func TestResolveLANGatewayAutoDerivesFromLiteralCIDR(t *testing.T) {
	it := ResolvedItem{LANCIDR: "10.0.0.1/24"}
	gw, err := resolveLANGateway(it)
	if err != nil {
		t.Fatalf("resolveLANGateway: %v", err)
	}
	if gw != "10.0.0.1" {
		t.Errorf("gateway = %q, want 10.0.0.1 (bare IP, no mask)", gw)
	}
}

func TestResolveLANGatewayExplicitOverridesAutoDerive(t *testing.T) {
	it := ResolvedItem{LANCIDR: "10.0.0.1/24", LANGateway: "10.0.0.254"}
	gw, err := resolveLANGateway(it)
	if err != nil {
		t.Fatalf("resolveLANGateway: %v", err)
	}
	if gw != "10.0.0.254" {
		t.Errorf("gateway = %q, want explicit override", gw)
	}
}

func TestResolveLANGatewayErrorsWhenLANCIDRIsVariableAndGatewayUnset(t *testing.T) {
	it := ResolvedItem{LANCIDR: "$lan_cidr"}
	if _, err := resolveLANGateway(it); err == nil {
		t.Fatal("expected error when lan_cidr is a $variable and lan_gw is unset")
	}
}

func TestInferVariableType(t *testing.T) {
	cases := map[string]string{
		"10.0.0.1/24":             "ip-netmask",
		"10.0.0.1":                "ip-netmask",
		"10.0.0.128-10.0.0.254":   "ip-range",
		"192.168.1.1-192.168.1.5": "ip-range",
	}
	for value, want := range cases {
		if got := inferVariableType(value); got != want {
			t.Errorf("inferVariableType(%q) = %q, want %q", value, got, want)
		}
	}
}
