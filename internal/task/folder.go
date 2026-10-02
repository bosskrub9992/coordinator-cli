package task

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var ticketPattern = regexp.MustCompile(`^[A-Z][A-Z0-9]*-[0-9]+$`)

func ValidateTicket(ticket string) error {
	if !ticketPattern.MatchString(ticket) {
		return fmt.Errorf("ticket %q is not like PROJ-1234", ticket)
	}
	return nil
}

func ResolveFolder(launchFolder, ticket, ticketFolder, homeWork string) (string, error) {
	if ticket != "" {
		if err := ValidateTicket(ticket); err != nil {
			return "", err
		}
	}
	if ticketFolder != "" {
		if filepath.IsAbs(ticketFolder) || filepath.VolumeName(ticketFolder) != "" {
			return "", fmt.Errorf("ticket folder %q must be a folder name inside the Launch folder", ticketFolder)
		}
		clean := filepath.Clean(ticketFolder)
		if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("ticket folder %q must be inside the Launch folder", ticketFolder)
		}
		return filepath.Join(launchFolder, clean), nil
	}
	if ticket == "" {
		return homeWork, nil
	}
	entries, err := os.ReadDir(launchFolder)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read Launch folder: %w", err)
	}
	var matches []string
	for _, e := range entries {
		if e.IsDir() && (e.Name() == ticket || strings.HasPrefix(e.Name(), ticket+"-")) {
			matches = append(matches, e.Name())
		}
	}
	switch len(matches) {
	case 0:
		return homeWork, nil
	case 1:
		return filepath.Join(launchFolder, matches[0]), nil
	default:
		return "", fmt.Errorf("several ticket folders for %s in the Launch folder (%s); name one", ticket, strings.Join(matches, ", "))
	}
}
