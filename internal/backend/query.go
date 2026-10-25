package backend

import (
	"context"
	"fmt"
	pb "sdn-agent/api/agent/v1"
	"sort"
)

// Querier is optional so a backend can implement only the operations it owns.
type Querier interface {
	QueryCapabilities() []*pb.QueryCapability
	Query(context.Context, *pb.QueryRequest) ([]map[string]any, error)
}

func (r *Registry) Query(ctx context.Context, request *pb.QueryRequest) ([]map[string]any, string, error) {
	names := make([]string, 0, len(r.drivers))
	for name := range r.drivers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		driver := r.drivers[name]
		q, ok := driver.(Querier)
		if !ok {
			continue
		}
		for _, cap := range q.QueryCapabilities() {
			if cap.Kind == request.Kind {
				records, err := q.Query(ctx, request)
				if err == nil && name == "linux" && request.Kind == pb.QueryKind_TENANTS {
					if frr, ok := r.drivers["frr"].(Querier); ok {
						vnis, e := frr.Query(ctx, &pb.QueryRequest{Kind: pb.QueryKind_EVPN_VNI, Vrf: request.Vrf})
						if e != nil {
							return nil, "linux+frr", fmt.Errorf("tenant L3VNI readback: %w", e)
						}
						for _, record := range records {
							record["l3vni"] = nil
							for _, vni := range vnis {
								if vni["type"] == "L3" && (vni["vrf"] == record["name"] || vni["tenantVrf"] == record["name"]) {
									record["l3vni"] = vni["vni"]
									record["l3Bridge"] = vni["sviIntf"]
									record["l3Vxlan"] = vni["vxlanIntf"]
								}
							}
						}
						return records, "linux+frr", nil
					}
				}
				return records, name, err
			}
		}
	}
	return nil, "", fmt.Errorf("query %s is not supported by installed backends", request.Kind)
}

func (r *Registry) Verify(ctx context.Context, operations []*pb.Operation) []string {
	var failures []string
	for _, operation := range operations {
		if operation == nil {
			failures = append(failures, "nil operation")
			continue
		}
		if operation.Action != pb.Action_CREATE && operation.Action != pb.Action_SET && operation.Action != pb.Action_BIND {
			failures = append(failures, operation.Id+": verification accepts only present desired resources")
			continue
		}
		drivers := r.ops[operation.Code]
		if len(drivers) == 0 {
			failures = append(failures, operation.Id+": unsupported operation")
			continue
		}
		for _, driver := range drivers {
			if err := driver.Validate(operation); err != nil {
				failures = append(failures, operation.Id+"/"+driver.Name()+": "+err.Error())
				continue
			}
			if err := driver.Verify(ctx, operation); err != nil {
				failures = append(failures, operation.Id+"/"+driver.Name()+": "+err.Error())
			}
		}
	}
	return failures
}
