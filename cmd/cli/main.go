//go:build grpc

// Command cli is a gRPC client that exercises the gateway's unary and
// server-streaming AI query RPCs (and can hit the inter-agent bidi/batch RPCs).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/LZY-keeper/agentcomm/internal/rpc"
	"github.com/LZY-keeper/agentcomm/internal/rpc/pb"
)

func main() {
	addr := flag.String("addr", "localhost:50051", "gateway gRPC address")
	question := flag.String("q", "计算 7*6", "question")
	flag.Parse()

	c, err := rpc.Dial(*addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dial:", err)
		os.Exit(1)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fmt.Println("== unary Query ==")
	resp, err := c.QueryUnary(ctx, *question)
	if err != nil {
		fmt.Fprintln(os.Stderr, "query:", err)
		os.Exit(1)
	}
	fmt.Printf("answer: %s  (agent=%s, %dms)\n", resp.GetAnswer(), resp.GetAgentId(), resp.GetProcessingTimeMs())

	fmt.Println("== server-stream QueryStream (real tokens) ==")
	if err := c.QueryStream(ctx, *question, func(ev *pb.AIStreamEvent) {
		switch ev.EventType {
		case "decision":
			fmt.Printf("\n[routing decision] %s\n", ev.Content)
		case "token":
			fmt.Print(ev.Content)
		case "done":
			fmt.Println()
		}
	}); err != nil {
		fmt.Fprintln(os.Stderr, "stream:", err)
		os.Exit(1)
	}
}
