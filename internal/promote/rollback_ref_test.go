package promote

import "testing"

func TestIsRollbackRef(t *testing.T) {
	cases := []struct {
		name string
		ref  string
		want bool
	}{
		{name: "RollbackPrefixTrue", ref: RollbackRefPrefix + "prod", want: true},
		{name: "HotfixPrefixFalse", ref: "hotfix/login-fix", want: false},
		{name: "EmptyFalse", ref: "", want: false},
		{name: "PlainEnvBranchFalse", ref: "env/prod", want: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsRollbackRef(c.ref); got != c.want {
				t.Errorf("IsRollbackRef(%q) = %v, want %v", c.ref, got, c.want)
			}
		})
	}
}
