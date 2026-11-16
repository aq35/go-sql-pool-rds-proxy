package main

import "testing"

func TestEffectiveTLS(t *testing.T) {
	cases := []struct {
		name       string
		requireTLS string // REQUIRE_TLS in /etc/exp.env ("" = unset)
		requested  string
		want       string
	}{
		{"default follows a Proxy that requires TLS", "true", "", "skip-verify"},
		{"default stays plain when the Proxy does not require TLS", "false", "", ""},
		{"default stays plain when the stack did not say", "", "", ""},
		{"none is plain on purpose (the Proxy will refuse it if it requires TLS)", "true", "none", ""},
		{"none with a plain Proxy", "false", "none", ""},
		{"skip-verify is used as given", "false", "skip-verify", "skip-verify"},
		{"skip-verify with a TLS-only Proxy", "true", "skip-verify", "skip-verify"},
		{"a registered TLS config name is used as given", "true", "custom", "custom"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("REQUIRE_TLS", c.requireTLS)
			if got := effectiveTLS(c.requested); got != c.want {
				t.Fatalf("effectiveTLS(%q) with REQUIRE_TLS=%q = %q, want %q", c.requested, c.requireTLS, got, c.want)
			}
		})
	}
}
