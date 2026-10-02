package role

import _ "embed"

//go:embed coordinator.md
var coordinator string

func Coordinator() string { return coordinator }
