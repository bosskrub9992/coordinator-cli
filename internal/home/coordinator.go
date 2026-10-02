package home

import "path/filepath"

func (h Home) CoordinatorRunDir() string { return filepath.Join(h.Root, "run", "coordinator") }
func (h Home) FleetEventsPath() string   { return filepath.Join(h.Root, "fleet-events.jsonl") }
