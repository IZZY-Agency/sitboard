// Package sitboard carries the licence texts inside the binary: sitboard's
// own licence and the third-party notices (make notices writes them). It sits
// at the repository root because go:embed only reaches files below a package.
// It is internal to sitboard and not a stable API.
package sitboard

import _ "embed"

//go:embed LICENSE
var license string

//go:embed THIRD_PARTY_NOTICES.md
var notices string

// Text is sitboard's licence (LICENSE) followed by the third-party notices
// (THIRD_PARTY_NOTICES.md): what `sitboard --licenses` prints and
// /licenses.txt serves.
func Text() string {
	return license + "\n" + notices
}
