package role

import _ "embed"

//go:embed worker.md
var workerMD string

func Worker() string { return workerMD }
