package localapp

import (
	_ "embed"
	"strings"
)

//go:embed seeds.txt
var mainnetSeedText string

// MainnetSeeds returns a fresh copy of the built-in QDAY bootstrap names.
// The packager reads the same source file for distribution.json.
func MainnetSeeds() []string { return strings.Fields(mainnetSeedText) }
