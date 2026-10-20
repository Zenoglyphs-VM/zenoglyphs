package main

import (
	"encoding/json"
	"math/big"
	"os"
	"sync"
	"time"
)

// benchRecorder collects one row per EVM call a swarm submits, for
// `evm swarm -report`. It measures what a user of the architecture would feel
// — how long from signing to a block in a momentum, and from there to the
// public API showing the receipt — and what each call paid, so a run can be
// read back as a latency and cost curve rather than as log lines.
//
// A nil *benchRecorder is valid and records nothing, so the non-reporting
// paths carry no conditionals.
type benchRecorder struct {
	mu   sync.Mutex
	rows []*benchRow
}

// benchRow is one call. Stage is how far it got: "publish", "confirm" and
// "index" name the step it failed at, "ok" means a receipt came back (which
// may itself be a revert — Success says which).
type benchRow struct {
	Wallet    int    `json:"wallet"`
	Op        string `json:"op"`
	DataBytes int    `json:"dataBytes"`
	StartMs   int64  `json:"startMs"` // unix milliseconds

	PublishMs int64 `json:"publishMs"` // quote + plasma + sign + publish RPC
	ConfirmMs int64 `json:"confirmMs"` // published → in a momentum
	IndexMs   int64 `json:"indexMs"`   // in a momentum → receipt on the API
	TotalMs   int64 `json:"totalMs"`

	Momentum    uint64 `json:"momentum"`
	Rebids      int    `json:"rebids"` // replacements published before it confirmed
	FeeRaw      string `json:"feeRaw"`
	BasePlasma  uint64 `json:"basePlasma"`
	FusedPlasma uint64 `json:"fusedPlasma"`
	Difficulty  uint64 `json:"difficulty"`
	GasUsed     uint64 `json:"gasUsed"`
	Success     bool   `json:"success"`
	Stage       string `json:"stage"`
	Err         string `json:"err,omitempty"`

	start, mark time.Time
}

func (r *benchRecorder) begin(wallet int, op string, dataBytes int, start time.Time) *benchRow {
	if r == nil {
		return nil
	}
	return &benchRow{Wallet: wallet, Op: op, DataBytes: dataBytes, StartMs: start.UnixMilli(),
		Stage: "publish", start: start, mark: start}
}

func (b *benchRow) lap() int64 {
	now := time.Now()
	d := now.Sub(b.mark).Milliseconds()
	b.mark = now
	b.TotalMs = now.Sub(b.start).Milliseconds()
	return d
}

func (b *benchRow) published(fee *big.Int, err error) {
	if b == nil {
		return
	}
	b.PublishMs = b.lap()
	if fee != nil {
		b.FeeRaw = fee.String()
	}
	if err != nil {
		b.Err = err.Error()
		return
	}
	b.Stage = "confirm"
}

func (b *benchRow) confirmed(momentum uint64, rebids int, err error) {
	if b == nil {
		return
	}
	b.ConfirmMs = b.lap()
	b.Momentum = momentum
	b.Rebids = rebids
	if err != nil {
		b.Err = err.Error()
		return
	}
	b.Stage = "index"
}

func (b *benchRow) indexed(rec map[string]any, err error) {
	if b == nil {
		return
	}
	b.IndexMs = b.lap()
	if err != nil {
		b.Err = err.Error()
		return
	}
	b.Stage = "ok"
	b.Success = boolField(rec, "success")
	if g, ok := rec["gasUsed"].(float64); ok {
		b.GasUsed = uint64(g)
	}
	if !b.Success {
		b.Err = str(rec["error"])
	}
}

func (r *benchRecorder) add(b *benchRow) {
	if r == nil || b == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows = append(r.rows, b)
}

// benchReport is the file -report writes: the run's shape, then every row.
type benchReport struct {
	Run        string      `json:"run"`
	Wallets    int         `json:"wallets"`
	Duration   string      `json:"duration,omitempty"`
	Rounds     int         `json:"rounds,omitempty"`
	FailRate   float64     `json:"failRate"`
	CallGas    uint64      `json:"callGas"`
	FeeToken   string      `json:"feeToken"`
	Counter    string      `json:"counter"`
	Token      string      `json:"token"`
	FundStart  int64       `json:"fundStartMs"`
	LoadStart  int64       `json:"loadStartMs"`
	LoadEnd    int64       `json:"loadEndMs"`
	Failures   int         `json:"failures"`
	Mismatches int         `json:"mismatches"`
	Rows       []*benchRow `json:"rows"`
}

func (r *benchRecorder) write(path string, rep benchReport) error {
	r.mu.Lock()
	rep.Rows = r.rows
	data, err := json.MarshalIndent(rep, "", " ")
	r.mu.Unlock()
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
