package evidence

import (
	"bufio"
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Gate is one P0 release requirement and its reviewed status from the source
// commit's V1_PRODUCTION_GATES.md.
type Gate struct {
	Requirement string
	Status      string
	Complete    bool
}

// GateReport keeps the human-reviewed release decision and P0 gate table
// together with the executable workflow evidence.
type GateReport struct {
	ProductionStatus string
	LastUpdated      time.Time
	Gates            []Gate
}

var updatedPattern = regexp.MustCompile(`^\*\*Last Updated:\*\*\s*(\d{4}-\d{2}-\d{2})`)

// ParseGateReport reads the current status and P0 table from the gate
// document at the commit being reported. A requirement counts as complete
// only when its status explicitly says implemented without open follow-up.
func ParseGateReport(markdown []byte) (GateReport, error) {
	var report GateReport
	scanner := bufio.NewScanner(bytes.NewReader(markdown))
	inP0 := false
	currentStatusFound, updatedFound := false, false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if matches := updatedPattern.FindStringSubmatch(line); len(matches) == 2 {
			date, err := time.Parse("2006-01-02", matches[1])
			if err != nil {
				return GateReport{}, fmt.Errorf("parse release gate last-updated date: %w", err)
			}
			report.LastUpdated = date.UTC()
			updatedFound = true
		}
		if strings.HasPrefix(line, "Production is **") {
			remainder := strings.TrimPrefix(line, "Production is **")
			status, _, found := strings.Cut(remainder, "**")
			if found {
				report.ProductionStatus = status
				currentStatusFound = true
			}
		}
		if line == "### P0 — required before 1.0" {
			inP0 = true
			continue
		}
		if inP0 && strings.HasPrefix(line, "### ") {
			break
		}
		if !inP0 || !strings.HasPrefix(line, "|") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		if len(cells) < 3 {
			continue
		}
		requirement, status := strings.TrimSpace(cells[0]), strings.TrimSpace(cells[1])
		if requirement == "Requirement" || strings.HasPrefix(requirement, "-") {
			continue
		}
		report.Gates = append(report.Gates, Gate{
			Requirement: requirement,
			Status:      status,
			Complete:    isCompleteGateStatus(status),
		})
	}
	if err := scanner.Err(); err != nil {
		return GateReport{}, fmt.Errorf("read release gate document: %w", err)
	}
	switch {
	case !updatedFound:
		return GateReport{}, fmt.Errorf("release gate document has no last-updated date")
	case !currentStatusFound:
		return GateReport{}, fmt.Errorf("release gate document has no production status")
	case len(report.Gates) == 0:
		return GateReport{}, fmt.Errorf("release gate document has no P0 requirements")
	}
	return report, nil
}

func isCompleteGateStatus(status string) bool {
	if !strings.HasPrefix(status, "✅") {
		return false
	}
	lower := strings.ToLower(status)
	for _, openTerm := range []string{"open", "partial", "incomplete", "remain", "pending", "outstanding", "still required", "not yet", "need"} {
		if strings.Contains(lower, openTerm) {
			return false
		}
	}
	return true
}
