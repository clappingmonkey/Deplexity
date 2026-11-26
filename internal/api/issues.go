package api

import (
	"context"
	"errors"
	"log"

	"github.com/clappingmonkey/deplexity/internal/client"
	"github.com/clappingmonkey/deplexity/internal/models"
)

// IsFatal reports whether err must abort the export instead of being recorded
// as a partial failure: cancellation, an expired deadline, or a session the
// server no longer accepts (every later request would fail the same way).
func IsFatal(err error) bool {
	return errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, client.ErrNotAuthenticated)
}

// Issues collects the non-fatal failures of fail-soft enrichment, so the
// export can record them in its manifest and exit non-zero after writing
// everything else. A nil *Issues only logs.
type Issues struct {
	list []models.SectionFailure
}

// Add logs a warning and records the failure.
func (i *Issues) Add(section, item string, err error) {
	if err == nil {
		return
	}
	if item != "" {
		log.Printf("warning: %s %s: %v", section, item, err)
	} else {
		log.Printf("warning: %s: %v", section, err)
	}
	if i != nil {
		i.list = append(i.list, models.SectionFailure{Section: section, Item: item, Error: err.Error()})
	}
}

// List returns the recorded failures in the order they happened.
func (i *Issues) List() []models.SectionFailure {
	if i == nil {
		return nil
	}
	return i.list
}
