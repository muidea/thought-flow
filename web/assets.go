package web

import "embed"

// Assets contains the ThoughtFlow browser UI files served by the
// application module.
//
//go:embed *
var Assets embed.FS
