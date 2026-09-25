package main

import (
	"net/http"

	auditpkg "github.com/cyberducttape/StePanel/internal/audit"
)

func (a *App) auditEvents(w http.ResponseWriter, r *http.Request) {
	logger := auditpkg.New(a.Config.AuditLog)
	logger.Events(w, r)
}
