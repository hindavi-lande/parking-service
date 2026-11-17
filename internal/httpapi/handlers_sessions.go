package httpapi

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/techverito/parking-service/internal/domain"
	"github.com/techverito/parking-service/internal/service"
)

type checkInRequest struct {
	Plate string `json:"plate"`
	Kind  string `json:"kind"`
	Phone string `json:"phone"`
}

type sessionResponse struct {
	ID           string     `json:"id"`
	LotID        string     `json:"lotId"`
	Plate        string     `json:"plate"`
	Phone        string     `json:"phone,omitempty"`
	Kind         string     `json:"kind"`
	Status       string     `json:"status"`
	CheckedInAt  time.Time  `json:"checkedInAt"`
	CheckedOutAt *time.Time `json:"checkedOutAt,omitempty"`
}

func toSessionResponse(session domain.Session) sessionResponse {
	return sessionResponse{
		ID:           session.ID,
		LotID:        session.LotID,
		Plate:        session.Plate,
		Phone:        session.Phone,
		Kind:         string(session.Kind),
		Status:       string(session.Status),
		CheckedInAt:  session.CheckedInAt,
		CheckedOutAt: session.CheckedOutAt,
	}
}

func (a *API) checkIn(w http.ResponseWriter, r *http.Request) {
	var request checkInRequest
	if err := decodeJSON(w, r, &request); err != nil {
		a.badRequest(w, r, err)
		return
	}

	session, err := a.sessions.CheckIn(r.Context(), chi.URLParam(r, "lotID"), service.CheckInInput{
		Plate: request.Plate,
		Kind:  request.Kind,
		Phone: request.Phone,
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}

	w.Header().Set("Location", "/api/v1/sessions/"+session.ID)
	writeJSON(w, http.StatusCreated, toSessionResponse(session))
}

func (a *API) getSession(w http.ResponseWriter, r *http.Request) {
	session, err := a.sessions.Get(r.Context(), chi.URLParam(r, "sessionID"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toSessionResponse(session))
}

func (a *API) checkout(w http.ResponseWriter, r *http.Request) {
	session, err := a.sessions.Checkout(r.Context(), chi.URLParam(r, "sessionID"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toSessionResponse(session))
}
