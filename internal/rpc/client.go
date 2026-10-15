//go:build grpc

package rpc

import (
	"context"
	"io"
	"time"

	"agentcomm/internal/rpc/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Client is a gRPC client exercising all four interaction patterns.
type Client struct {
	cc *grpc.ClientConn
	ai pb.AIQueryServiceClient
	ag pb.AgentCommunicationServiceClient
}

func Dial(addr string) (*Client, error) {
	cc, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	return &Client{cc: cc, ai: pb.NewAIQueryServiceClient(cc), ag: pb.NewAgentCommunicationServiceClient(cc)}, nil
}

func (c *Client) Close() error { return c.cc.Close() }

// QueryUnary: unary mode.
func (c *Client) QueryUnary(ctx context.Context, question string) (*pb.AIQueryResponse, error) {
	return c.ai.Query(ctx, &pb.AIQueryRequest{RequestId: "q1", Question: question, ContextId: "cli"})
}

// QueryStream: server-streaming — real token stream.
func (c *Client) QueryStream(ctx context.Context, question string, onEvent func(*pb.AIStreamEvent)) error {
	stream, err := c.ai.QueryStream(ctx, &pb.AIQueryRequest{RequestId: "qs1", Question: question, ContextId: "cli"})
	if err != nil {
		return err
	}
	for {
		ev, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if onEvent != nil {
			onEvent(ev)
		}
	}
}

// BatchSend: client-streaming.
func (c *Client) BatchSend(ctx context.Context, msgs []*pb.Message) (int32, error) {
	stream, err := c.ag.BatchSendMessages(ctx)
	if err != nil {
		return 0, err
	}
	for _, m := range msgs {
		if err := stream.Send(&pb.SendMessageRequest{Message: m}); err != nil {
			return 0, err
		}
	}
	resp, err := stream.CloseAndRecv()
	if err != nil {
		return 0, err
	}
	return resp.Accepted, nil
}

// ChatBidi: bidi-streaming — send one message, receive routed replies until
// the peer side closes (timeout-bounded for the CLI demo).
func (c *Client) ChatBidi(ctx context.Context, selfID, targetID, content string, onMsg func(*pb.Message)) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	stream, err := c.ag.RealTimeCommunication(ctx)
	if err != nil {
		return err
	}
	if err := stream.Send(&pb.Message{SourceAgent: selfID, TargetAgent: targetID, Content: content}); err != nil {
		return err
	}
	for {
		m, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if onMsg != nil {
			onMsg(m)
		}
	}
}
