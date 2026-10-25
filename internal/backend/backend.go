package backend

import (
	"context"
	"fmt"
	"os/exec"
	pb "sdn-agent/api/agent/v1"
	"sdn-agent/internal/model"
	"sort"
	"strings"
	"time"
)

type Driver interface {
	Name() string
	Codes() []pb.OpCode
	Validate(*pb.Operation) error
	Prepare(context.Context, *pb.Operation) (*model.Undo, error)
	Apply(context.Context, *pb.Operation) error
	Verify(context.Context, *pb.Operation) error
	Rollback(context.Context, *pb.Operation, *model.Undo) error
	Observe(context.Context) (map[string]string, error)
}
type Registry struct {
	drivers map[string]Driver
	ops     map[pb.OpCode][]Driver
}

func New(ds ...Driver) (*Registry, error) {
	r := &Registry{map[string]Driver{}, map[pb.OpCode][]Driver{}}
	for _, d := range ds {
		if r.drivers[d.Name()] != nil {
			return nil, fmt.Errorf("duplicate backend %s", d.Name())
		}
		r.drivers[d.Name()] = d
		for _, c := range d.Codes() {
			r.ops[c] = append(r.ops[c], d)
		}
	}
	return r, nil
}
func (r *Registry) Driver(name string) Driver { return r.drivers[name] }
func (r *Registry) Plan(ops []*pb.Operation) ([]*model.Step, error) {
	ordered, e := model.Order(ops)
	if e != nil {
		return nil, e
	}
	var out []*model.Step
	for _, o := range ordered {
		ds := append([]Driver(nil), r.ops[o.Code]...)
		// zebra refuses deleting the FRR VRF until its kernel VRF is inactive.
		if (o.Action == pb.Action_DELETE || o.Action == pb.Action_UNBIND) && o.Code != pb.OpCode_TENANT {
			for i, j := 0, len(ds)-1; i < j; i, j = i+1, j-1 {
				ds[i], ds[j] = ds[j], ds[i]
			}
		}
		if len(ds) == 0 {
			return nil, fmt.Errorf("unsupported operation %s", o.Code)
		}
		for _, d := range ds {
			if e = d.Validate(o); e != nil {
				return nil, fmt.Errorf("%s/%s: %w", o.Id, d.Name(), e)
			}
		}
		for _, d := range ds {
			out = append(out, &model.Step{ID: o.Id + "/" + d.Name(), Backend: d.Name(), Operation: &model.Op{Operation: o}})
		}
	}
	return out, nil
}
func (r *Registry) Capabilities(node string) *pb.Capabilities {
	out := &pb.Capabilities{NodeId: node, ProtocolVersion: 1, Features: []string{"structured-query/v1", "verify-resources/v1"}}
	for c, ds := range r.ops {
		x := &pb.Capability{Code: c, Rollback: true}
		for _, d := range ds {
			x.Backends = append(x.Backends, d.Name())
		}
		out.Operations = append(out.Operations, x)
	}
	sort.Slice(out.Operations, func(i, j int) bool { return out.Operations[i].Code < out.Operations[j].Code })
	for _, d := range r.drivers {
		if q, ok := d.(Querier); ok {
			out.Queries = append(out.Queries, q.QueryCapabilities()...)
		}
	}
	sort.Slice(out.Queries, func(i, j int) bool { return out.Queries[i].Kind < out.Queries[j].Kind })
	return out
}
func (r *Registry) Observe(ctx context.Context) (map[string]string, error) {
	out := map[string]string{}
	var errs []string
	for n, d := range r.drivers {
		v, e := d.Observe(ctx)
		for k, x := range v {
			out[n+"/"+k] = x
		}
		if e != nil {
			errs = append(errs, e.Error())
		}
	}
	if len(errs) > 0 {
		return out, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return out, nil
}

type Runner interface {
	Run(context.Context, ...string) (string, error)
}
type Exec struct {
	Prefix  []string
	Timeout time.Duration
}

func (r Exec) Run(ctx context.Context, args ...string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("empty command")
	}
	all := append(append([]string{}, r.Prefix...), args...)
	t := r.Timeout
	if t == 0 {
		t = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, t)
	defer cancel()
	b, e := exec.CommandContext(ctx, all[0], all[1:]...).CombinedOutput()
	if e != nil {
		return string(b), fmt.Errorf("%s: %w: %s", strings.Join(args, " "), e, strings.TrimSpace(string(b)))
	}
	return string(b), nil
}
