package hermetictest

import "io"

// Call is one run of the stand-in, as it recorded it.
type Call = call

// Dir is the folder Seal made, which holds everything it points at.
func Dir(s *Sealed) string { return s.dir }

// Calls returns the runs of the stand-in since Seal, in order.
func Calls(s *Sealed) ([]Call, error) { return s.calls() }

// FinishTo is Finish, saying what it has to say on w.
func FinishTo(s *Sealed, code int, w io.Writer) int { return s.finish(code, w) }

// TryWriteConfig is WriteConfig with claude as the stand-in, returning why
// it wrote no config, or why the one it wrote will not do.
func TryWriteConfig(path, contents string, port int, claude string) error {
	return writeConfig(path, contents, port, claude)
}
