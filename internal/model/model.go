package model

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"google.golang.org/protobuf/encoding/protojson"
	"sort"

	pb "sdn-agent/api/agent/v1"
)

func ID() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

type Undo struct {
	Commands [][]string        `json:"commands,omitempty"`
	Files    map[string]string `json:"files,omitempty"`
	Data     json.RawMessage   `json:"data,omitempty"`
	Noop     bool              `json:"noop,omitempty"`
}
type Op struct{ *pb.Operation }

func (o Op) MarshalJSON() ([]byte, error) { return protojson.Marshal(o.Operation) }
func (o *Op) UnmarshalJSON(b []byte) error {
	o.Operation = &pb.Operation{}
	return protojson.Unmarshal(b, o.Operation)
}
func Wrap(ops []*pb.Operation) []*Op {
	out := make([]*Op, 0, len(ops))
	for _, o := range ops {
		out = append(out, &Op{o})
	}
	return out
}

type Step struct {
	ID        string `json:"id"`
	Backend   string `json:"backend"`
	Operation *Op    `json:"operation"`
	Undo      *Undo  `json:"undo,omitempty"`
	Started   bool   `json:"started"`
	Done      bool   `json:"done"`
	Restored  bool   `json:"restored"`
}
type Record struct {
	Task       *pb.Task `json:"task"`
	Plan       []*Step  `json:"plan"`
	Before     *Running `json:"before"`
	Operations []*Op    `json:"operations"`
	Created    int64    `json:"created"`
}
type Running struct {
	Revision     uint64   `json:"revision"`
	Transactions []string `json:"transactions"`
	Operations   []*Op    `json:"operations"`
}

// Order validates the graph before execution. Stable ordering makes recovery reproducible.
func Order(ops []*pb.Operation) ([]*pb.Operation, error) {
	by := map[string]*pb.Operation{}
	for _, o := range ops {
		if o == nil || o.Id == "" {
			return nil, fmt.Errorf("operation id required")
		}
		if _, ok := by[o.Id]; ok {
			return nil, fmt.Errorf("duplicate operation %s", o.Id)
		}
		by[o.Id] = o
	}
	indeg := map[string]int{}
	edges := map[string][]string{}
	for _, o := range ops {
		seen := map[string]bool{}
		for _, d := range o.DependsOn {
			if by[d] == nil {
				return nil, fmt.Errorf("missing dependency %s", d)
			}
			if seen[d] {
				continue
			}
			seen[d] = true
			indeg[o.Id]++
			edges[d] = append(edges[d], o.Id)
		}
	}
	var ready []string
	for k := range by {
		if indeg[k] == 0 {
			ready = append(ready, k)
		}
	}
	var out []*pb.Operation
	for len(ready) > 0 {
		sort.Strings(ready)
		k := ready[0]
		ready = ready[1:]
		out = append(out, by[k])
		for _, n := range edges[k] {
			indeg[n]--
			if indeg[n] == 0 {
				ready = append(ready, n)
			}
		}
	}
	if len(out) != len(ops) {
		return nil, fmt.Errorf("dependency cycle")
	}
	return out, nil
}
func Terminal(s string) bool {
	return s == "SUCCEEDED" || s == "FAILED_NO_CHANGE" || s == "FAILED_ROLLED_BACK" || s == "ROLLBACK_FAILED"
}
