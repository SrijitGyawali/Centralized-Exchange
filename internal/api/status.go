package api

import (
	"net/http"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
)

// ProjectionStatus is what the API needs to know about the SQL read model.
// It is declared here, by its consumer; projector.Status satisfies it.
// A nil ProjectionStatus in Config means no read model is configured.
type ProjectionStatus interface {
	ProjectedSeq() domain.Seq
	Healthy() bool
}

// status answers GET /v1/status: how far the engine has applied commands,
// and how far the SQL read model has caught up.
//
//	applied_seq    the last command the engine applied (from its snapshot)
//	projected_seq  the last command committed to PostgreSQL
//	lag            applied_seq - projected_seq: how stale history queries are
//
// Lag is NORMAL and expected: the projector runs behind the engine by
// design, and trading never waits for it (ADR-0009). A growing lag, or
// healthy=false, means the database is slow or down: history is stale,
// matching is unaffected.
func (s *server) status(w http.ResponseWriter, r *http.Request) {
	applied := s.e.Snapshot().Seq
	out := map[string]any{"applied_seq": id(uint64(applied))}
	if p := s.cfg.Projection; p != nil {
		projected := p.ProjectedSeq()
		lag := uint64(0)
		if applied > projected {
			lag = uint64(applied - projected)
		}
		out["projection"] = map[string]any{
			"enabled":       true,
			"healthy":       p.Healthy(),
			"projected_seq": id(uint64(projected)),
			"lag":           id(lag),
		}
	} else {
		out["projection"] = map[string]any{"enabled": false}
	}
	writeJSON(w, http.StatusOK, out)
}
