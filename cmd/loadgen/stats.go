package main

import (
	"math"
	"slices"
	"strconv"
	"time"
)

// sample is one arrival: sent and answered (or failed), or dropped.
type sample struct {
	Stage   int           // index into the run's stages; -1 = warmup
	Kind    OpKind        //
	Offset  time.Duration // scheduled arrival, since the run started
	Latency time.Duration // scheduled arrival → response (0 if dropped)
	Status  int           // HTTP status; 0 = no response (transport error or dropped)
	Dropped bool          // never sent: max_in_flight was reached
}

// answered: the engine produced a definite answer. 404 (unknown order),
// 409 (key conflict) and 422 (business rejection) are answers too: the
// command was sequenced, journaled and applied.
func answered(status int) bool {
	switch status {
	case 200, 404, 409, 422:
		return true
	}
	return false
}

// Percentiles are latencies in milliseconds.
type Percentiles struct {
	P50  float64 `json:"p50_ms"`
	P90  float64 `json:"p90_ms"`
	P95  float64 `json:"p95_ms"`
	P99  float64 `json:"p99_ms"`
	P999 float64 `json:"p99_9_ms"`
	Max  float64 `json:"max_ms"`
}

// percentiles computes EXACT percentiles by sorting every latency (nearest
// rank). The load generator keeps all samples, so it does not need the
// bucket approximation a server-side histogram uses. Averages are not
// reported: one stalled request in a thousand barely moves a mean but is
// exactly what users notice.
func percentiles(d []time.Duration) Percentiles {
	if len(d) == 0 {
		return Percentiles{}
	}
	s := slices.Clone(d)
	slices.Sort(s)
	at := func(q float64) float64 {
		i := int(math.Ceil(q*float64(len(s)))) - 1 // nearest rank: the smallest value >= q of the data
		return ms(s[max(0, min(i, len(s)-1))])
	}
	return Percentiles{P50: at(0.50), P90: at(0.90), P95: at(0.95), P99: at(0.99), P999: at(0.999), Max: ms(s[len(s)-1])}
}

func ms(d time.Duration) float64 { return math.Round(float64(d)/1e3) / 1e3 } // to milliseconds, µs precision

// StageStats is everything the client saw in one stage.
type StageStats struct {
	Name          string         `json:"name"`
	OfferedRate   float64        `json:"offered_rate_per_s"`
	Duration      Duration       `json:"duration"`
	Arrivals      int            `json:"arrivals"`
	Dropped       int            `json:"dropped"` // not sent: max_in_flight reached
	Sent          int            `json:"sent"`
	CompletedRate float64        `json:"completed_rate_per_s"` // answered per second
	ByStatus      map[string]int `json:"by_status"`            // "200", "503", … and "error" for no response
	Answered      int            `json:"answered"`             // 200/404/409/422
	Errors        int            `json:"errors"`               // 429, 5xx, transport errors
	ErrorRate     float64        `json:"error_rate"`           // errors / sent
	Latency       Percentiles    `json:"latency_answered"`     // from SCHEDULED arrival, answered only
	LatencyErrors Percentiles    `json:"latency_errors"`       // how fast errors came back
	Server        *ServerStats   `json:"server,omitempty"`
}

// summarize builds the stats for one stage from its samples.
func summarize(name string, st Stage, samples []sample) StageStats {
	s := StageStats{Name: name, OfferedRate: st.Rate, Duration: st.Duration, ByStatus: map[string]int{}}
	var ok, bad []time.Duration
	for _, x := range samples {
		s.Arrivals++
		if x.Dropped {
			s.Dropped++
			continue
		}
		s.Sent++
		key := "error"
		if x.Status != 0 {
			key = strconv.Itoa(x.Status)
		}
		s.ByStatus[key]++
		if answered(x.Status) {
			s.Answered++
			ok = append(ok, x.Latency)
		} else {
			s.Errors++
			bad = append(bad, x.Latency)
		}
	}
	if secs := st.Duration.Seconds(); secs > 0 {
		s.CompletedRate = math.Round(float64(s.Answered)/secs*10) / 10
	}
	if s.Sent > 0 {
		s.ErrorRate = math.Round(float64(s.Errors)/float64(s.Sent)*1e4) / 1e4
	}
	s.Latency, s.LatencyErrors = percentiles(ok), percentiles(bad)
	return s
}
