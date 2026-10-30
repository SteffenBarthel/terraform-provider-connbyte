//go:build generate

package tools

import (
	_ "github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs"
)

// Format the examples and generate the documentation in ../docs.
//go:generate terraform fmt -recursive ../examples/
//go:generate go run github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs generate --provider-dir .. --provider-name connbyte
