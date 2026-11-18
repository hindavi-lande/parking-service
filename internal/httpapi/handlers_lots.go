package httpapi

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/techverito/parking-service/internal/domain"
	"github.com/techverito/parking-service/internal/service"
)

type createLotRequest struct {
	Name       string `json:"name"`
	Address    string `json:"address"`
	Capacity   int    `json:"capacity"`
	HourlyRate *int   `json:"hourlyRate"`
}

type lotResponse struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Address    string    `json:"address"`
	Capacity   int       `json:"capacity"`
	HourlyRate *int      `json:"hourlyRate"`
	CreatedAt  time.Time `json:"createdAt"`
}

func toLotResponse(lot domain.Lot) lotResponse {
	return lotResponse{
		ID:         lot.ID,
		Name:       lot.Name,
		Address:    lot.Address,
		Capacity:   lot.Capacity,
		HourlyRate: lot.HourlyRate,
		CreatedAt:  lot.CreatedAt,
	}
}

func (a *API) createLot(w http.ResponseWriter, r *http.Request) {
	var request createLotRequest
	if err := decodeJSON(w, r, &request); err != nil {
		a.badRequest(w, r, err)
		return
	}

	lot, err := a.lots.Create(r.Context(), service.CreateLotInput{
		Name:       request.Name,
		Address:    request.Address,
		Capacity:   request.Capacity,
		HourlyRate: request.HourlyRate,
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}

	w.Header().Set("Location", "/api/v1/lots/"+lot.ID)
	writeJSON(w, http.StatusCreated, toLotResponse(lot))
}

func (a *API) listLots(w http.ResponseWriter, r *http.Request) {
	lots, err := a.lots.List(r.Context())
	if err != nil {
		a.fail(w, r, err)
		return
	}

	response := make([]lotResponse, 0, len(lots))
	for _, lot := range lots {
		response = append(response, toLotResponse(lot))
	}
	writeJSON(w, http.StatusOK, response)
}

func (a *API) getLot(w http.ResponseWriter, r *http.Request) {
	lot, err := a.lots.Get(r.Context(), chi.URLParam(r, "lotID"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toLotResponse(lot))
}
