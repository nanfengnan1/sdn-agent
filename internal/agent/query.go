package agent

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
	pb "sdn-agent/api/agent/v1"
	"sdn-agent/internal/model"
)

func (a *Agent) Query(ctx context.Context, q *pb.QueryRequest) (*pb.QueryResponse, error) {
	if q == nil {
		return nil, status.Error(codes.InvalidArgument, "query required")
	}
	if e := a.check(q.NodeId); e != nil {
		return nil, e
	}
	if q.Kind == pb.QueryKind_QUERY_UNSPECIFIED || q.PageSize > 1000 {
		return nil, status.Error(codes.InvalidArgument, "query kind required; page_size must be <= 1000")
	}
	if _, ok := pb.AddressFamily_name[int32(q.Family)]; !ok {
		return nil, status.Error(codes.InvalidArgument, "unknown address family")
	}
	if q.OspfVersion != 0 && q.OspfVersion != 2 && q.OspfVersion != 3 || q.RouteType > 5 {
		return nil, status.Error(codes.InvalidArgument, "invalid OSPF version or EVPN route type")
	}
	for _, s := range []string{q.Vrf, q.Bridge, q.Interface} {
		if s != "" && !regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_.:-]{0,63}$`).MatchString(s) {
			return nil, status.Error(codes.InvalidArgument, "invalid resource filter")
		}
	}
	if q.Prefix != "" {
		if _, e := netip.ParsePrefix(q.Prefix); e != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid prefix")
		}
	}
	if q.Neighbor != "" {
		if _, e := netip.ParseAddr(q.Neighbor); e != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid neighbor")
		}
	}
	if q.RouteType > 0 && q.Kind != pb.QueryKind_EVPN_ROUTES && !(q.Kind == pb.QueryKind_BGP_ROUTES && q.Family == pb.AddressFamily_L2VPN_EVPN) {
		return nil, status.Error(codes.InvalidArgument, "route_type requires an EVPN route query")
	}
	supported := false
	for _, cap := range a.Registry.Capabilities(a.NodeID).Queries {
		if cap.Kind == q.Kind {
			supported = true
		}
	}
	if !supported {
		return nil, status.Error(codes.Unimplemented, "query is not supported by installed backends")
	}
	records, source, e := a.Registry.Query(ctx, q)
	if e != nil {
		if ctx.Err() != nil {
			return nil, status.FromContextError(ctx.Err()).Err()
		}
		return nil, status.Error(codes.FailedPrecondition, e.Error())
	}
	// Order by the same stable projection used by the token. Sorting the raw
	// rows would let a changing timer move a record across page boundaries.
	type encodedRecord struct{ stable, raw string }
	encoded := make([]encodedRecord, len(records))
	if len(records) > 100000 {
		return nil, status.Error(codes.ResourceExhausted, "query exceeds 100000 records; narrow filters")
	}
	for i, row := range records {
		b, err := json.Marshal(row)
		if err != nil {
			return nil, status.Error(codes.Internal, "query record encoding failed")
		}
		stable, err := json.Marshal(stableQueryValue(row))
		if err != nil {
			return nil, status.Error(codes.Internal, "query fingerprint encoding failed")
		}
		encoded[i] = encodedRecord{string(stable), string(b)}
	}
	sort.Slice(encoded, func(i, j int) bool {
		if encoded[i].stable == encoded[j].stable {
			return encoded[i].raw < encoded[j].raw
		}
		return encoded[i].stable < encoded[j].stable
	})
	keys := make([]string, len(encoded))
	for i, row := range encoded {
		keys[i] = row.stable
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("%d/%s/%s/%s/%d/%d/%d/%s/%s\n", q.Kind, q.Vrf, q.Bridge, q.Interface, q.Family, q.OspfVersion, q.RouteType, q.Prefix, q.Neighbor) + strings.Join(keys, "\n")))
	fingerprint := hex.EncodeToString(digest[:])
	start := 0
	if q.PageToken != "" {
		token, err := base64.RawURLEncoding.DecodeString(q.PageToken)
		parts := strings.Split(string(token), ":")
		if err != nil || len(parts) != 2 {
			return nil, status.Error(codes.InvalidArgument, "invalid page token")
		}
		if parts[0] != fingerprint {
			return nil, status.Error(codes.Aborted, "query data changed; restart from first page")
		}
		start, err = strconv.Atoi(parts[1])
		if err != nil || start < 0 || start > len(keys) {
			return nil, status.Error(codes.InvalidArgument, "invalid page offset")
		}
	}
	limit := int(q.PageSize)
	if limit == 0 {
		limit = 200
	}
	end := start + limit
	if end > len(keys) {
		end = len(keys)
	}
	var run model.Running
	if err := a.Store.Load(ctx, "running", &run); err != nil {
		return nil, loadError(err)
	}
	out := &pb.QueryResponse{NodeId: a.NodeID, Kind: q.Kind, Source: source, CollectedUnixMs: time.Now().UnixMilli(), TotalCount: uint32(len(keys)), Revision: run.Revision}
	for _, entry := range encoded[start:end] {
		var row map[string]any
		_ = json.Unmarshal([]byte(entry.raw), &row)
		item, err := structpb.NewStruct(row)
		if err != nil {
			return nil, status.Error(codes.Internal, "query record conversion failed")
		}
		out.Records = append(out.Records, item)
	}
	if end < len(keys) {
		out.NextPageToken = base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%s:%d", fingerprint, end)))
	}
	return out, nil
}

func (a *Agent) VerifyResources(ctx context.Context, q *pb.VerifyResourcesRequest) (*pb.VerifyResourcesResponse, error) {
	if err := a.check(q.NodeId); err != nil {
		return nil, err
	}
	if len(q.Operations) > 2048 {
		return nil, status.Error(codes.InvalidArgument, "too many resource operations")
	}
	// Serialize readback with execution and block new commits until it completes.
	a.executionMu.Lock()
	defer a.executionMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	var run model.Running
	if err := a.Store.Load(ctx, "running", &run); err != nil {
		return nil, loadError(err)
	}
	if run.Revision != q.ExpectedRevision {
		return nil, status.Error(codes.Aborted, "agent revision changed before verification")
	}
	keys, err := a.Store.Keys(ctx, "task/")
	if err != nil {
		return nil, loadError(err)
	}
	for _, key := range keys {
		var record model.Record
		if err = a.Store.Load(ctx, key, &record); err != nil {
			return nil, loadError(err)
		}
		if record.Task == nil || !model.Terminal(record.Task.State) || record.Task.State == "ROLLBACK_FAILED" {
			return nil, status.Error(codes.FailedPrecondition, "pending task or failed rollback prevents resource verification")
		}
	}
	failures := a.Registry.Verify(ctx, q.Operations)
	return &pb.VerifyResourcesResponse{NodeId: a.NodeID, Revision: run.Revision, Verified: len(failures) == 0, Errors: failures}, nil
}
