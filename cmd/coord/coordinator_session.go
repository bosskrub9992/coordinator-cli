package main

import "github.com/bosskrub9992/coordinator-cli/internal/home"

func trackSession(h home.Home, token, sessionID string) {
	if token == "" || sessionID == "" {
		return
	}
	l, err := h.ReadLock()
	if err != nil || l == nil || l.Token != token || l.SessionID == sessionID {
		return
	}
	if _, err := h.UpdateLock(token, func(l *home.Lock) { l.SessionID = sessionID }); err != nil {
		return
	}
	var rec sessionRecord
	if found, err := home.ReadJSON(sessionPath(h), &rec); err == nil && found && rec.LaunchFolder == l.LaunchFolder && rec.SessionID != sessionID {
		rec.SessionID = sessionID
		home.WriteJSONAtomic(sessionPath(h), rec)
	}
}
