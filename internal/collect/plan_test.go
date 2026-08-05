package collect

import (
	"testing"
	"time"
)

// TestPlanExpand cobre o produto cartesiano de rotas, datas e cabines.
func TestPlanExpand(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

	plan := Plan{
		Origins:      []string{"LIS", "OPO"},
		Destinations: []string{"RIO", "LIS"}, // LIS->LIS deve ser descartado
		StartDate:    start,
		Days:         2,
		Cabins:       []string{"E", "C"},
		Adults:       1,
	}

	jobs := plan.Expand()

	// (LIS->RIO) + (OPO->RIO) + (OPO->LIS) = 3 rotas x 2 dias x 2 cabines.
	if got, want := len(jobs), 12; got != want {
		t.Fatalf("len(Expand()) = %d, esperado %d", got, want)
	}

	for _, job := range jobs {
		if job.Origin == job.Destination {
			t.Errorf("rota degenerada gerada: %s->%s", job.Origin, job.Destination)
		}
		if job.ReturnDate != "" {
			t.Errorf("TripDuration=0 deveria gerar só ida, obtido ReturnDate=%q", job.ReturnDate)
		}
		if err := job.Validate(); err != nil {
			t.Errorf("busca gerada é inválida: %v", err)
		}
	}

	if got, want := jobs[0].DepartDate, "01092026"; got != want {
		t.Errorf("DepartDate = %q, esperado %q (DDMMYYYY)", got, want)
	}

	// Ida e volta com 7 noites.
	plan.TripDuration = 7
	roundTrip := plan.Expand()
	if got, want := roundTrip[0].ReturnDate, "08092026"; got != want {
		t.Errorf("ReturnDate = %q, esperado %q", got, want)
	}
}

// TestDedupeRoutes confirma o descarte da dimensão de datas para o calendário.
func TestDedupeRoutes(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

	jobs := Plan{
		Origins:      []string{"LIS"},
		Destinations: []string{"RIO", "GRU"},
		StartDate:    start,
		Days:         10,
		Cabins:       []string{"E"},
		Adults:       1,
	}.Expand()

	if got, want := len(jobs), 20; got != want {
		t.Fatalf("len(Expand()) = %d, esperado %d", got, want)
	}

	// 2 rotas x 1 cabine x 1 tipo de viagem: as 10 datas colapsam.
	deduped := DedupeRoutes(jobs)
	if got, want := len(deduped), 2; got != want {
		t.Errorf("len(DedupeRoutes()) = %d, esperado %d", got, want)
	}

	seen := map[string]bool{}
	for _, j := range deduped {
		id := j.Origin + "-" + j.Destination
		if seen[id] {
			t.Errorf("rota %s duplicada após dedupe", id)
		}
		seen[id] = true
	}
}
