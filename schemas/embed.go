package schemas

import "embed"

// FS contains all JSON schema files embedded at build time.
//
//go:embed *.json
var FS embed.FS
