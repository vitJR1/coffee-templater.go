// Package wasm embeds the coffee-templater-engine WebAssembly binary.
//
// The binary is a release asset of https://github.com/vitJR1/coffee-templater-engine, pinned by
// version and sha256. Update it with scripts/update-engine.sh; TestEmbeddedEngine checks the pin.
package wasm

import _ "embed"

// Version of the embedded engine release.
const Version = "0.1.2"

// SHA256 of the embedded coffee_core.wasm.
const SHA256 = "870674f108cb8d4f0560c1f55563eeace9759bd99e8c6489502ad5ecaf6d65f5"

// Binary is coffee_core.wasm.
//
//go:embed coffee_core.wasm
var Binary []byte
