package command

import (
	"context"

	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// Pwd returns the pwd command, which prints the current virtual path.
func Pwd() Command {
	return Command{
		Name:    "pwd",
		Summary: "Print the current path",
		Usage:   "",
		MinArgs: 0,
		MaxArgs: 0,
		Run: func(_ context.Context, s *session.Session, _ Invocation) (Result, error) {
			return Message{Text: s.Path()}, nil
		},
	}
}
