package branchnaming

import (
	"context"
	"fmt"

	"github.com/holark-ai/holark/internal/commandprefix"
	"github.com/holark-ai/holark/internal/protocol"
)

type Dispatcher struct {
	namers map[protocol.HarnessType]Namer
}

func NewDispatcher() *Dispatcher { return NewDispatcherWithCommands(nil) }

func NewDispatcherWithCommands(commands commandprefix.Resolver) *Dispatcher {
	return NewDispatcherWithNamers(map[protocol.HarnessType]Namer{
		protocol.HarnessCodex:      NewCodexNamer(func(n *CodexNamer) { n.commands = commands }),
		protocol.HarnessClaudeCode: NewClaudeCodeNamer(func(n *ClaudeCodeNamer) { n.commands = commands }),
		protocol.HarnessOpenCode:   NewOpenCodeNamer(func(n *OpenCodeNamer) { n.commands = commands }),
	})
}

func NewDispatcherWithNamers(namers map[protocol.HarnessType]Namer) *Dispatcher {
	copy := make(map[protocol.HarnessType]Namer, len(namers))
	for harnessType, namer := range namers {
		if namer != nil {
			copy[harnessType] = namer
		}
	}
	return &Dispatcher{namers: copy}
}

func (dispatcher *Dispatcher) NameFor(ctx context.Context, harnessType protocol.HarnessType, input Input) (Proposal, error) {
	if dispatcher == nil {
		return Proposal{}, fmt.Errorf("branch naming is unavailable for %s", harnessType)
	}
	namer, ok := dispatcher.namers[harnessType]
	if !ok {
		return Proposal{}, fmt.Errorf("branch naming is unavailable for %s", harnessType)
	}
	return namer.Name(ctx, input)
}
