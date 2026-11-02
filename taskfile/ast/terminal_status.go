package ast

import (
	"slices"

	"go.yaml.in/yaml/v3"

	"github.com/go-task/task/v3/errors"
)

// TerminalStatus controls presentation, never execution or exit codes.
// It is local to a task and is not inherited by dependencies or task calls.
type TerminalStatus struct {
	Suppress []string `yaml:"suppress"`
}

func (s *TerminalStatus) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return errors.NewTaskfileDecodeError(nil, node).WithTypeMessage("terminal_status")
	}
	var decoded struct {
		Suppress []string `yaml:"suppress"`
	}
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		if key.Value != "suppress" {
			return errors.NewTaskfileDecodeError(nil, key).WithMessage("unknown terminal_status property: %s", key.Value)
		}
		if value.Kind != yaml.SequenceNode {
			return errors.NewTaskfileDecodeError(nil, value).WithMessage("terminal_status.suppress must be an array of states")
		}
	}
	if err := node.Decode(&decoded); err != nil {
		return errors.NewTaskfileDecodeError(err, node)
	}
	for i, state := range decoded.Suppress {
		switch state {
		case "idle", "working", "blocked", "done", "error":
		default:
			return errors.NewTaskfileDecodeError(nil, node).WithMessage("unknown terminal status state %q", state)
		}
		if slices.Contains(decoded.Suppress[:i], state) {
			return errors.NewTaskfileDecodeError(nil, node).WithMessage("duplicate terminal status state %q", state)
		}
	}
	s.Suppress = decoded.Suppress
	return nil
}

func (s TerminalStatus) DeepCopy() TerminalStatus {
	return TerminalStatus{Suppress: slices.Clone(s.Suppress)}
}
