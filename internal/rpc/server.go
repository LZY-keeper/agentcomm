//go:build grpc

// Package rpc exposes the super-agent and inter-agent messaging over gRPC.
// It demonstrates the four real gRPC interaction patterns (unary, server-,
// client-, and bidi-streaming); notably RealTimeCommunication performs genuine
// cross-client message routing via a broker (v1's was an echo stub). Requires
// generated stubs: run `make proto` first.
package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"agentcomm/internal/logx"
	"agentcomm/internal/orchestrator"
	"agentcomm/internal/registry"
	"agentcomm/internal/rpc/pb"
	"agentcomm/internal/types"
	"google.golang.org/grpc"
)

// Server implements all gRPC services backed by the orchestrator + registry.
type Server struct {
	pb.UnimplementedAIQueryServiceServer
	pb.UnimplementedAgentCommunicationServiceServer
	pb.UnimplementedHealthServiceServer

	orch *orchestrator.Orchestrator
	reg  *registry.Registry
	bk   *broker
}

func New(orch *orchestrator.Orchestrator, reg *registry.Registry) *Server {
	return &Server{orch: orch, reg: reg, bk: newBroker()}
}

// Register wires the services onto a gRPC server.
func (s *Server) Register(g *grpc.Server) {
	pb.RegisterAIQueryServiceServer(g, s)
	pb.RegisterAgentCommunicationServiceServer(g, s)
	pb.RegisterHealthServiceServer(g, s)
}

// --- AIQueryService ---

// Query: unary — orchestrates and returns the full answer + routing decision.
func (s *Server) Query(ctx context.Context, req *pb.AIQueryRequest) (*pb.AIQueryResponse, error) {
	start := time.Now()
	ch, dec, err := s.orch.Stream(ctx, req.Question, nil, req.ContextId)
	if err != nil {
		return nil, err
	}
	text := drain(ch, ctx)
	decJSON, _ := json.Marshal(dec)
	return &pb.AIQueryResponse{
		RequestId: req.RequestId, Answer: text,
		AgentId: dec.Agent.ID, AgentName: dec.Agent.Name,
		ContextId: req.ContextId, ProcessingTimeMs: time.Since(start).Milliseconds(),
		RoutingDecisionJson: string(decJSON),
		Status: &pb.Status{Code: 0, Message: "ok"},
	}, nil
}

// QueryStream: server-streaming — emits a decision event, then real token stream.
func (s *Server) QueryStream(req *pb.AIQueryRequest, stream pb.AIQueryService_QueryStreamServer) error {
	ctx := stream.Context()
	ch, dec, err := s.orch.Stream(ctx, req.Question, nil, req.ContextId)
	if err != nil {
		return err
	}
	decJSON, _ := json.Marshal(dec)
	if err := stream.Send(&pb.AIStreamEvent{
		EventId: "dec", EventType: "decision", Content: string(decJSON),
		ContextId: req.ContextId, Timestamp: time.Now().UnixMilli(),
	}); err != nil {
		return err
	}
	for tok := range ch {
		if err := stream.Send(&pb.AIStreamEvent{
			EventId: "tok", EventType: "token", Content: tok.Text,
			ContextId: req.ContextId, Timestamp: time.Now().UnixMilli(),
		}); err != nil {
			return err
		}
	}
	return stream.Send(&pb.AIStreamEvent{EventId: "done", EventType: "done", ContextId: req.ContextId})
}

func (s *Server) GetQueryStatus(ctx context.Context, req *pb.QueryStatusRequest) (*pb.QueryStatusResponse, error) {
	return &pb.QueryStatusResponse{Status: &pb.Status{Code: 0}, TaskState: "completed"}, nil
}

// --- AgentCommunicationService: four real modes ---

// SendMessage: unary — routes a single message to a target agent via the broker.
func (s *Server) SendMessage(ctx context.Context, req *pb.SendMessageRequest) (*pb.SendMessageResponse, error) {
	m := req.Message
	if m.TargetAgent == "" {
		// no explicit target: orchestrate as a query
		ch, _, err := s.orch.Stream(ctx, m.Content, nil, m.SourceAgent)
		if err != nil {
			return nil, err
		}
		text := drain(ch, ctx)
		s.bk.deliver(m.TargetAgent, &pb.Message{Id: m.Id, Content: text, SourceAgent: "super-agent", TargetAgent: m.SourceAgent})
		return &pb.SendMessageResponse{Status: &pb.Status{Code: 0}, MessageId: m.Id}, nil
	}
	s.bk.deliver(m.TargetAgent, m)
	return &pb.SendMessageResponse{Status: &pb.Status{Code: 0}, MessageId: m.Id}, nil
}

func (s *Server) GetAgents(ctx context.Context, req *pb.GetAgentsRequest) (*pb.GetAgentsResponse, error) {
	agents := s.reg.Discover(req.Skill)
	out := make([]*pb.ServiceInfo, 0, len(agents))
	for _, a := range agents {
		out = append(out, &pb.ServiceInfo{ServiceName: a.Name, Version: a.Version, Tags: a.Tags, Metadata: map[string]string{"id": a.ID, "url": a.URL}})
	}
	return &pb.GetAgentsResponse{Agents: out}, nil
}

// ListenMessages: server-streaming — streams messages addressed to an agent.
func (s *Server) ListenMessages(req *pb.ListenRequest, stream pb.AgentCommunicationService_ListenMessagesServer) error {
	sub := s.bk.subscribe(req.AgentId)
	defer s.bk.unsubscribe(req.AgentId, sub)
	for {
		select {
		case <-stream.Context().Done():
			return nil
		case m, ok := <-sub:
			if !ok {
				return nil
			}
			if err := stream.Send(m); err != nil {
				return err
			}
		}
	}
}

// BatchSendMessages: client-streaming — receive a batch, route each, summarize.
func (s *Server) BatchSendMessages(stream pb.AgentCommunicationService_BatchSendMessagesServer) error {
	n := 0
	for {
		req, err := stream.Recv()
		if err == io.EOF {
			return stream.SendAndClose(&pb.BatchSendResponse{Status: &pb.Status{Code: 0}, Accepted: int32(n)})
		}
		if err != nil {
			return err
		}
		s.bk.deliver(req.Message.TargetAgent, req.Message)
		n++
	}
}

// RealTimeCommunication: bidi-streaming — genuine routing between connected
// peers. Each inbound message is forwarded to its target_agent's stream.
func (s *Server) RealTimeCommunication(stream pb.AgentCommunicationService_RealTimeCommunicationServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	selfID := first.SourceAgent
	if selfID == "" {
		selfID = fmt.Sprintf("anon-%d", time.Now().UnixNano())
	}
	sub := s.bk.subscribe(selfID)
	defer s.bk.unsubscribe(selfID, sub)
	logx.Info(stream.Context(), "bidi peer connected", "agent", selfID)

	// forward the first message, then pump both directions
	s.bk.deliver(first.TargetAgent, first)
	errs := make(chan error, 2)
	// inbound -> broker
	go func() {
		for {
			m, e := stream.Recv()
			if e != nil {
				errs <- e
				return
			}
			s.bk.deliver(m.TargetAgent, m)
		}
	}()
	// broker -> outbound
	go func() {
		for m := range sub {
			if e := stream.Send(m); e != nil {
				errs <- e
				return
			}
		}
		errs <- io.EOF
	}()
	return <-errs
}

// --- HealthService ---
func (s *Server) Check(ctx context.Context, req *pb.HealthCheckRequest) (*pb.HealthCheckResponse, error) {
	return &pb.HealthCheckResponse{Status: pb.HealthCheckResponse_SERVING}, nil
}

func drain(ch <-chan types.Token, ctx context.Context) string {
	var sb []byte
	for t := range ch {
		sb = append(sb, t.Text...)
	}
	return string(sb)
}

// --- broker: real cross-client message routing ---

type broker struct {
	mu   sync.RWMutex
	subs map[string]map[chan *pb.Message]struct{}
}

func newBroker() *broker { return &broker{subs: map[string]map[chan *pb.Message]struct{}{}} }

func (b *broker) subscribe(agentID string) chan *pb.Message {
	ch := make(chan *pb.Message, 16)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.subs[agentID] == nil {
		b.subs[agentID] = map[chan *pb.Message]struct{}{}
	}
	b.subs[agentID][ch] = struct{}{}
	return ch
}

func (b *broker) unsubscribe(agentID string, ch chan *pb.Message) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if set, ok := b.subs[agentID]; ok {
		delete(set, ch)
		if len(set) == 0 {
			delete(b.subs, agentID)
		}
	}
}

func (b *broker) deliver(target string, m *pb.Message) {
	if m == nil {
		return
	}
	b.mu.RLock()
	set := b.subs[target]
	b.mu.RUnlock()
	for ch := range set {
		select {
		case ch <- m:
		default: // drop if subscriber slow (back-pressure safety)
		}
	}
}
