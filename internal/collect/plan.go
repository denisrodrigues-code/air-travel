package collect

import (
	"time"

	"airtravel/internal/models"
)

// Plan descreve o conjunto de coletas a executar.
type Plan struct {
	Origins      []string
	Destinations []string
	// StartDate é a primeira data de partida; Days é quantos dias avançar.
	StartDate time.Time
	Days      int
	// Cabins a percorrer, ex.: []string{"E", "C"}.
	Cabins []string
	Adults int
	// TripDuration, se > 0, gera ida e volta com essa quantidade de noites.
	TripDuration int
	// TripType força O ou R em todas as coletas geradas.
	TripType string
}

// Expand produz o produto cartesiano de rotas, datas e cabines.
//
// Rotas com origem igual ao destino são descartadas por não existirem.
func (p Plan) Expand() []models.SearchParams {
	if p.Days < 1 {
		p.Days = 1
	}
	if p.Adults < 1 {
		p.Adults = 1
	}
	if len(p.Cabins) == 0 {
		p.Cabins = []string{"E"}
	}

	capacity := len(p.Origins) * len(p.Destinations) * p.Days * len(p.Cabins)
	jobs := make([]models.SearchParams, 0, capacity)

	for _, origin := range p.Origins {
		for _, destination := range p.Destinations {
			if origin == destination {
				continue
			}
			for day := range p.Days {
				departure := p.StartDate.AddDate(0, 0, day)

				returnDate := ""
				if p.TripDuration > 0 {
					returnDate = departure.AddDate(0, 0, p.TripDuration).Format(models.DateLayout)
				}

				for _, cabin := range p.Cabins {
					jobs = append(jobs, models.SearchParams{
						Origin:      origin,
						Destination: destination,
						DepartDate:  departure.Format(models.DateLayout),
						ReturnDate:  returnDate,
						Adults:      p.Adults,
						CabinClass:  cabin,
						TripType:    p.TripType,
					})
				}
			}
		}
	}
	return jobs
}

// DedupeRoutes reduz o plano a uma entrada por rota, cabine e tipo de viagem,
// descartando a dimensão de datas.
//
// É o que o modo calendário precisa: a API devolve um ano inteiro numa
// requisição, então percorrer datas seria desperdício.
func DedupeRoutes(jobs []models.SearchParams) []models.SearchParams {
	seen := make(map[string]struct{}, len(jobs))
	out := make([]models.SearchParams, 0, len(jobs))

	for _, job := range jobs {
		id := job.Origin + "|" + job.Destination + "|" + job.CabinClass + "|" + job.EffectiveTripType()
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, job)
	}
	return out
}
