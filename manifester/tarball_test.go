package manifester

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetSpNameFromUrl(t *testing.T) {
	assert.Equal(t,
		"nixexprs-nixos-25.11.1056",
		getSpNameFromUrl("https://releases.nixos.org/nixos/25.11/nixos-25.11.1056.d9bc5c7dceb3/nixexprs.tar.xz"))
	assert.Equal(t,
		"nixexprs-nixpkgs-26.05pre910304",
		getSpNameFromUrl("https://releases.nixos.org/nixpkgs/nixpkgs-26.05pre910304.f997fa0f94fb/nixexprs.tar.xz"))
}
